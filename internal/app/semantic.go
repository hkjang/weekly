package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Semantic search over report items, using pgvector for storage and the AI
// Gateway for embeddings.
//
// It is strictly optional: without pgvector, without an embedding model, or
// with AI turned off, the feature stays dark and substring plus trigram search
// carry the whole workload. Nothing here is required for the product to work.

const (
	embeddingBatchSize    = 32
	embeddingMaxInput     = 4000
	semanticSearchLimit   = 20
	semanticMinSimilarity = 0.25
)

type embeddingConfig struct {
	Endpoint string
	APIKey   string
	Model    string
	Timeout  time.Duration
}

func (a *App) embeddingConfig(ctx context.Context) (embeddingConfig, error) {
	if !a.capabilities.Vector {
		return embeddingConfig{}, errors.New("pgvector is not installed")
	}
	if !a.settingBool(ctx, "ai.embedding_enabled", false) {
		return embeddingConfig{}, errors.New("embedding is disabled")
	}
	model := strings.TrimSpace(a.setting(ctx, "ai.embedding_model", ""))
	endpoint := strings.TrimSpace(a.setting(ctx, "ai.embedding_endpoint", ""))
	if model == "" || endpoint == "" {
		return embeddingConfig{}, errors.New("embedding endpoint or model is not configured")
	}
	secret, err := a.secretSetting(ctx, "ai.api_key")
	if err != nil {
		return embeddingConfig{}, err
	}
	return embeddingConfig{
		Endpoint: endpoint, APIKey: secret, Model: model,
		Timeout: time.Duration(a.settingInt(ctx, "ai.timeout_seconds", 90)) * time.Second,
	}, nil
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// requestEmbeddings calls an OpenAI compatible embeddings endpoint.
func requestEmbeddings(ctx context.Context, cfg embeddingConfig, inputs []string) ([][]float32, error) {
	payload, err := json.Marshal(embeddingRequest{Model: cfg.Model, Input: inputs})
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, cfg.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("embedding endpoint redirects are not allowed")
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding endpoint returned HTTP %d", response.StatusCode)
	}
	var decoded embeddingResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, errors.New("embedding endpoint returned invalid JSON")
	}
	if decoded.Error != nil {
		return nil, errors.New("embedding endpoint returned an error")
	}
	if len(decoded.Data) != len(inputs) {
		return nil, fmt.Errorf("embedding endpoint returned %d vectors for %d inputs", len(decoded.Data), len(inputs))
	}
	result := make([][]float32, len(inputs))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(inputs) {
			return nil, errors.New("embedding endpoint returned an out of range index")
		}
		result[item.Index] = item.Embedding
	}
	for index, vector := range result {
		if len(vector) == 0 {
			return nil, fmt.Errorf("embedding %d is empty", index)
		}
	}
	return result, nil
}

func vectorLiteral(values []float32) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		fmt.Fprintf(&builder, "%g", value)
	}
	builder.WriteByte(']')
	return builder.String()
}

// embeddableText is what gets embedded for one item. The title carries most of
// the meaning, so it leads.
func embeddableText(title, category, current, next, issue string) string {
	parts := []string{}
	for _, value := range []string{title, category, current, next, issue} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return trimRunes(strings.Join(parts, "\n"), embeddingMaxInput)
}

// embeddingWorker keeps embeddings in step with report content in the
// background. Content that has not changed is never re-embedded, and content
// that has changed is re-embedded on the next tick.
func (a *App) embeddingWorker(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg, err := a.embeddingConfig(ctx)
			if err != nil {
				continue
			}
			if processed, err := a.embedPending(ctx, cfg); err != nil {
				a.logger.Warn("embedding batch failed", "error", err)
			} else if processed > 0 {
				a.logger.Info("embeddings updated", "items", processed, "model", cfg.Model)
			}
		}
	}
}

// pendingEmbedding is one item that has to be embedded, with the digest of the
// exact text it was read as.
type pendingEmbedding struct {
	id     int64
	text   string
	digest string
}

// pendingEmbeddings lists items whose stored embedding is missing or no longer
// matches the text it was made from.
//
// The digest is computed in SQL rather than in Go so that staleness can be a
// predicate: an item edited after it was embedded has to come back out of the
// database, and Go cannot filter on a hash it has not read the row to compute.
// Report items are updated in place since v0.11, so their identifiers survive a
// save and "no embedding row yet" stopped being a usable test for freshness.
func (a *App) pendingEmbeddings(ctx context.Context, model string, limit int) ([]pendingEmbedding, error) {
	rows, err := a.db.Query(ctx, `SELECT i.id, i.title, i.category, i.current_result, i.next_plan, i.issue, d.digest
		FROM report_items i
		CROSS JOIN LATERAL (SELECT encode(sha256(convert_to(
			concat_ws(E'\n', i.title, i.category, i.current_result, i.next_plan, i.issue), 'UTF8')), 'hex') AS digest) d
		LEFT JOIN report_item_embeddings e ON e.report_item_id = i.id AND e.model = $1
		WHERE length(trim(i.title)) > 0
			AND (e.report_item_id IS NULL OR e.content_hash IS DISTINCT FROM d.digest)
		ORDER BY i.id DESC LIMIT $2`, model, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []pendingEmbedding{}
	for rows.Next() {
		var id int64
		var title, category, current, next, issue, digest string
		if err := rows.Scan(&id, &title, &category, &current, &next, &issue, &digest); err != nil {
			return nil, err
		}
		items = append(items, pendingEmbedding{id: id, text: embeddableText(title, category, current, next, issue), digest: digest})
	}
	return items, rows.Err()
}

// embedPending embeds one batch of items that need it.
func (a *App) embedPending(ctx context.Context, cfg embeddingConfig) (int, error) {
	items, err := a.pendingEmbeddings(ctx, cfg.Model, embeddingBatchSize)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}
	inputs := make([]string, len(items))
	for index, item := range items {
		inputs[index] = item.text
	}
	vectors, err := requestEmbeddings(ctx, cfg, inputs)
	if err != nil {
		return 0, err
	}
	for index, item := range items {
		// The digest read alongside the text is stored, not one recomputed now:
		// if the item was edited while the batch was in flight, storing the
		// digest of the newer text would pair it with the older vector and hide
		// the change for good.
		_, err := a.db.Exec(ctx, `INSERT INTO report_item_embeddings(report_item_id,embedding,model,dimensions,content_hash,updated_at)
			VALUES($1,$2::vector,$3,$4,$5,now())
			ON CONFLICT (report_item_id) DO UPDATE SET embedding=EXCLUDED.embedding, model=EXCLUDED.model,
				dimensions=EXCLUDED.dimensions, content_hash=EXCLUDED.content_hash, updated_at=now()`,
			item.id, vectorLiteral(vectors[index]), cfg.Model, len(vectors[index]), item.digest)
		if err != nil {
			return index, err
		}
	}
	return len(items), nil
}

// searchSemantic finds reports whose meaning is close to the query even when
// they share no words with it.
func (a *App) searchSemantic(r *http.Request, p *principal, query string, seen map[int64]*searchHit) ([]searchHit, error) {
	cfg, err := a.embeddingConfig(r.Context())
	if err != nil {
		return nil, nil
	}
	vectors, err := requestEmbeddings(r.Context(), cfg, []string{query})
	if err != nil {
		return nil, err
	}
	literal := vectorLiteral(vectors[0])
	statement := `SELECT r.id,r.user_id,u.display_name,r.week_start,r.status,r.source_type,i.title,
			1 - (e.embedding <=> $1::vector) AS score
		FROM report_item_embeddings e
		JOIN report_items i ON i.id = e.report_item_id
		JOIN weekly_reports r ON r.id = i.report_id
		JOIN users u ON u.id = r.user_id
		WHERE e.model = $2 AND e.dimensions = $3`
	args := []any{literal, cfg.Model, len(vectors[0])}
	switch {
	case p.Role == "ADMIN":
	case p.Role == "TEAM_LEADER" || p.Role == "ORG_MANAGER":
		if p.OrganizationID == nil {
			args = append(args, p.ID)
			statement += fmt.Sprintf(" AND r.user_id=$%d", len(args))
		} else {
			args = append(args, p.ID, *p.OrganizationID)
			statement += fmt.Sprintf(` AND (r.user_id=$%d OR u.organization_id IN (WITH RECURSIVE orgs AS
				(SELECT id FROM organizations WHERE id=$%d UNION ALL SELECT o.id FROM organizations o JOIN orgs x ON o.parent_id=x.id)
				SELECT id FROM orgs))`, len(args)-1, len(args))
		}
	default:
		args = append(args, p.ID)
		statement += fmt.Sprintf(" AND r.user_id=$%d", len(args))
	}
	// The cutoff is model dependent: cosine scores from two embedding models are
	// not on the same scale, so a value tuned for one rejects everything or
	// accepts everything on the other. Operators tune it against the coverage
	// reported by GET /api/v1/admin/embeddings.
	threshold := float64(a.settingInt(r.Context(), "search.semantic_threshold", 25)) / 100
	args = append(args, threshold)
	statement += fmt.Sprintf(` AND 1 - (e.embedding <=> $1::vector) >= $%d
		ORDER BY e.embedding <=> $1::vector LIMIT %d`, len(args), semanticSearchLimit)

	rows, err := a.db.Query(r.Context(), statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []searchHit{}
	added := map[int64]bool{}
	for rows.Next() {
		var reportID, userID int64
		var displayName, status, sourceType, title string
		var week time.Time
		var score float64
		if err := rows.Scan(&reportID, &userID, &displayName, &week, &status, &sourceType, &title, &score); err != nil {
			return nil, err
		}
		if seen[reportID] != nil || added[reportID] {
			continue
		}
		added[reportID] = true
		result = append(result, searchHit{
			Approximate: true, Semantic: true, ReportID: reportID, UserID: userID, DisplayName: displayName,
			WeekStart: week.Format("2006-01-02"), Status: status, SourceType: sourceType, Score: int(score * 100),
			Matches: []searchMatch{{Field: "semantic", Label: "의미 유사", Snippet: trimRunes(strings.TrimSpace(title), 80)}},
		})
	}
	return result, rows.Err()
}

// embeddingStatus reports how much of the corpus is embedded, so an operator
// can tell whether semantic search is actually covering their reports.
func (a *App) embeddingStatus(w http.ResponseWriter, r *http.Request) {
	type status struct {
		VectorAvailable bool   `json:"vectorAvailable"`
		Enabled         bool   `json:"enabled"`
		Model           string `json:"model"`
		Items           int    `json:"items"`
		Embedded        int    `json:"embedded"`
		// Stale counts items whose stored vector was made from text the author
		// has since edited. Those items are still searchable, but they answer
		// for wording that no longer exists, so an operator needs to see the
		// number rather than infer it from a coverage percentage that looks full.
		Stale  int    `json:"stale"`
		Reason string `json:"reason,omitempty"`
	}
	result := status{VectorAvailable: a.capabilities.Vector}
	cfg, err := a.embeddingConfig(r.Context())
	if err != nil {
		result.Reason = err.Error()
	} else {
		result.Enabled = true
		result.Model = cfg.Model
	}
	if a.capabilities.Vector {
		_ = a.db.QueryRow(r.Context(), `SELECT
			(SELECT count(*) FROM report_items WHERE length(trim(title)) > 0),
			(SELECT count(*) FROM report_item_embeddings WHERE model = $1),
			(SELECT count(*) FROM report_items i
				JOIN report_item_embeddings e ON e.report_item_id = i.id AND e.model = $1
				WHERE e.content_hash IS DISTINCT FROM encode(sha256(convert_to(
					concat_ws(E'\n', i.title, i.category, i.current_result, i.next_plan, i.issue), 'UTF8')), 'hex'))`, cfg.Model).
			Scan(&result.Items, &result.Embedded, &result.Stale)
	}
	writeData(w, 200, result)
}

// rebuildEmbeddings embeds everything still missing, rather than waiting for the
// background worker to walk the corpus one batch at a time. Turning the feature
// on is exactly when an operator wants the backlog cleared now.
func (a *App) rebuildEmbeddings(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.embeddingConfig(r.Context())
	if err != nil {
		writeError(w, 400, "EMBEDDING_DISABLED", "의미 검색이 활성화돼 있지 않습니다: "+err.Error())
		return
	}
	total := 0
	for range 200 {
		processed, err := a.embedPending(r.Context(), cfg)
		if err != nil {
			a.logger.Warn("embedding rebuild failed", "error", err, "embedded", total)
			writeError(w, 502, "EMBEDDING_FAILED", "임베딩 생성 중 오류가 발생했습니다. 임베딩 엔드포인트 설정을 확인하세요.")
			return
		}
		total += processed
		if processed < embeddingBatchSize {
			break
		}
	}
	a.logger.Info("embedding rebuild complete", "items", total, "model", cfg.Model)
	writeData(w, 200, map[string]any{"embedded": total, "model": cfg.Model})
}
