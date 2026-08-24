package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Natural language search over work history: "인증 연동하다 막혔던 사례",
// "결산 자동화 실패 원인". The value is in the issues and how they were
// resolved, so the searchable text is the whole life of the task, not its title.
//
// Three signals are combined, in decreasing order of trustworthiness:
//
//  1. literal term hits in the task's text — always available, no extension
//  2. trigram similarity on the title — pg_trgm, catches wording differences
//  3. embedding distance over the task's items — pgvector, catches paraphrase
//
// Signal 1 alone is a working feature. The other two only ever add results, so
// an offline deployment sees fewer matches, never worse ones.

const (
	workSearchLimit      = 25
	workSearchTermWeight = 12
	workSearchTitleBonus = 25
	workSearchIssueBonus = 10
)

type workSearchHit struct {
	workRef
	Score      int      `json:"score"`
	Semantic   bool     `json:"semantic"`
	AgeWeeks   int      `json:"ageWeeks"`
	IssueWeeks int      `json:"issueWeeks"`
	Resolved   bool     `json:"resolved"`
	Matched    []string `json:"matched"`
	Issue      string   `json:"issue"`
	Resolution string   `json:"resolution"`
	Why        string   `json:"why"`
}

type workSearchResponse struct {
	Query    string          `json:"query"`
	Terms    []string        `json:"terms"`
	Semantic bool            `json:"semantic"`
	Hits     []workSearchHit `json:"hits"`
	// SemanticReason says why the meaning-based pass did not run, when it did
	// not. Without it "비슷한 과거 업무를 찾지 못했습니다" is the same sentence
	// whether the search looked and found nothing or never looked at all, and
	// only one of those is something an administrator can change.
	SemanticReason string `json:"semanticReason,omitempty"`
}

// searchableText is everything the task ever reported, lowercased once.
func searchableText(item workItemView) string {
	var builder strings.Builder
	builder.WriteString(item.Title)
	builder.WriteByte('\n')
	builder.WriteString(item.Category)
	for _, week := range item.Weeks {
		builder.WriteByte('\n')
		builder.WriteString(week.CurrentResult)
		builder.WriteByte('\n')
		builder.WriteString(week.NextPlan)
		builder.WriteByte('\n')
		builder.WriteString(week.Issue)
		builder.WriteByte('\n')
		builder.WriteString(week.ManagementAsk)
	}
	return strings.ToLower(builder.String())
}

// resolutionOf finds what was reported after the last issue disappeared, which
// is the closest thing the data has to "how it was fixed".
func resolutionOf(item workItemView) (issue string, resolution string, resolved bool) {
	lastIssueIndex := -1
	for index, week := range item.Weeks {
		if strings.TrimSpace(week.Issue) != "" {
			lastIssueIndex = index
		}
	}
	if lastIssueIndex < 0 {
		return "", "", false
	}
	issue = openingLine(item.Weeks[lastIssueIndex].Issue)
	if lastIssueIndex == len(item.Weeks)-1 {
		// The issue was still open in the most recent report.
		return issue, "", false
	}
	for _, later := range item.Weeks[lastIssueIndex+1:] {
		if text := strings.TrimSpace(later.CurrentResult); text != "" {
			return issue, openingLine(text), true
		}
	}
	return issue, "", true
}

// scoreWorkItem counts literal evidence for the query in one task.
func scoreWorkItem(item workItemView, terms []string) (int, []string) {
	body := searchableText(item)
	title := strings.ToLower(item.Title)
	issues := ""
	for _, week := range item.Weeks {
		issues += strings.ToLower(week.Issue) + "\n"
	}
	score := 0
	matched := []string{}
	for _, term := range terms {
		needle := strings.ToLower(term)
		if !strings.Contains(body, needle) {
			continue
		}
		matched = append(matched, term)
		score += workSearchTermWeight
		if strings.Contains(title, needle) {
			score += workSearchTitleBonus
		}
		if strings.Contains(issues, needle) {
			score += workSearchIssueBonus
		}
	}
	// Every term present is a much stronger match than one of five.
	if len(matched) == len(terms) && len(terms) > 1 {
		score += 20
	}
	return score, matched
}

func (a *App) searchWorkItems(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if runeLength(query) < 2 {
		writeData(w, http.StatusOK, workSearchResponse{Query: query, Terms: []string{}, Hits: []workSearchHit{}})
		return
	}
	scope := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = scopeSelf
	}
	if scope == scopeTeam && p.Role == "USER" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조직 단위 조회는 팀장 이상만 가능합니다.")
		return
	}
	items, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, scope == scopeSelf), "")
	if err != nil {
		a.logger.Error("work search", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 검색할 수 없습니다.")
		return
	}

	terms := searchTerms(query)
	byID := map[int64]*workSearchHit{}
	order := []int64{}
	for _, item := range items {
		score, matched := scoreWorkItem(item, terms)
		if score == 0 {
			continue
		}
		issue, resolution, resolved := resolutionOf(item)
		hit := &workSearchHit{
			workRef: referenceTo(item), Score: score, AgeWeeks: item.AgeWeeks,
			IssueWeeks: item.IssueWeeks, Resolved: resolved, Matched: matched,
			Issue: issue, Resolution: resolution,
		}
		hit.Why = describeWorkHit(*hit, false)
		byID[item.ID] = hit
		order = append(order, item.ID)
	}

	// Paraphrase is exactly what a literal scan cannot do, so the semantic pass
	// runs whenever it is available rather than only on thin results.
	semantic := false
	semanticReason := ""
	if !a.capabilities.Vector {
		semanticReason = "이 데이터베이스에 pgvector 확장이 없어 글자 일치로만 찾았습니다."
	} else if _, cfgErr := a.embeddingConfig(r.Context()); cfgErr != nil {
		semanticReason = "임베딩이 설정되지 않아 글자 일치로만 찾았습니다. 관리자 설정에서 임베딩을 활성화하면 표현이 달라도 찾습니다."
	}
	if a.capabilities.Vector && semanticReason == "" {
		matches, semanticErr := a.semanticWorkItems(r, query, items)
		if semanticErr != nil {
			a.logger.Warn("semantic work search", "error", semanticErr, "trace", traceIDFromContext(r.Context()))
			semanticReason = "의미 검색이 실패해 글자 일치 결과만 보여 줍니다. 서버 로그에서 원인을 확인하세요."
		} else {
			for id, similarity := range matches {
				if existing := byID[id]; existing != nil {
					existing.Semantic = true
					existing.Score += similarity
					existing.Why = describeWorkHit(*existing, true)
					semantic = true
					continue
				}
				for _, item := range items {
					if item.ID != id {
						continue
					}
					issue, resolution, resolved := resolutionOf(item)
					hit := &workSearchHit{
						workRef: referenceTo(item), Score: similarity, Semantic: true,
						AgeWeeks: item.AgeWeeks, IssueWeeks: item.IssueWeeks, Resolved: resolved,
						Matched: []string{}, Issue: issue, Resolution: resolution,
					}
					hit.Why = describeWorkHit(*hit, true)
					byID[id] = hit
					order = append(order, id)
					semantic = true
					break
				}
			}
		}
	}

	hits := make([]workSearchHit, 0, len(order))
	for _, id := range order {
		hits = append(hits, *byID[id])
	}
	sort.SliceStable(hits, func(x, y int) bool {
		if hits[x].Score != hits[y].Score {
			return hits[x].Score > hits[y].Score
		}
		return hits[x].LastWeek > hits[y].LastWeek
	})
	if len(hits) > workSearchLimit {
		hits = hits[:workSearchLimit]
	}
	writeData(w, http.StatusOK, workSearchResponse{Query: query, Terms: terms, Semantic: semantic, SemanticReason: semanticReason, Hits: hits})
}

// describeWorkHit says what this past task offers the person searching.
func describeWorkHit(hit workSearchHit, semantic bool) string {
	switch {
	case hit.Resolution != "":
		return fmt.Sprintf("같은 문제를 겪고 해결한 사례입니다. 해결 경과: %s", hit.Resolution)
	case hit.Issue != "" && !hit.Resolved:
		return fmt.Sprintf("아직 해결되지 않은 같은 계열의 이슈입니다(%d주 지속).", hit.IssueWeeks)
	case semantic && len(hit.Matched) == 0:
		return "입력한 표현과 직접 겹치는 단어는 없지만 내용이 가까운 업무입니다."
	case hit.Completed:
		return fmt.Sprintf("%d주에 걸쳐 완료된 유사 업무입니다.", hit.AgeWeeks)
	default:
		return "진행 중인 유사 업무입니다."
	}
}

// semanticWorkItems maps work item id to a 0-100 similarity for the query,
// using the embeddings built for report items.
func (a *App) semanticWorkItems(r *http.Request, query string, visible []workItemView) (map[int64]int, error) {
	cfg, err := a.embeddingConfig(r.Context())
	if err != nil {
		return nil, nil
	}
	allowed := make([]int64, 0, len(visible))
	for _, item := range visible {
		allowed = append(allowed, item.ID)
	}
	if len(allowed) == 0 {
		return nil, nil
	}
	vectors, err := requestEmbeddings(r.Context(), cfg, []string{query})
	if err != nil {
		return nil, err
	}
	threshold := float64(a.settingInt(r.Context(), "search.semantic_threshold", 25)) / 100
	// Permission is enforced by restricting to work items already loaded under
	// the caller's scope, so this query cannot widen visibility.
	rows, err := a.db.Query(r.Context(), `SELECT i.work_item_id, max(1 - (e.embedding <=> $1::vector))
		FROM report_item_embeddings e
		JOIN report_items i ON i.id = e.report_item_id
		WHERE e.model = $2 AND e.dimensions = $3 AND i.work_item_id = ANY($4)
		GROUP BY i.work_item_id
		HAVING max(1 - (e.embedding <=> $1::vector)) >= $5`,
		vectorLiteral(vectors[0]), cfg.Model, len(vectors[0]), allowed, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]int{}
	for rows.Next() {
		var id int64
		var similarity float64
		if err := rows.Scan(&id, &similarity); err != nil {
			return nil, err
		}
		result[id] = int(similarity * 100)
	}
	return result, rows.Err()
}
