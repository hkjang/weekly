package app

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Evidence lineage.
//
// A report item says what happened. Until now nothing said where it came from:
// the Confluence pages behind a candidate and the slide numbers behind an
// imported item were both discarded the moment the item was written, so the
// question a reader actually has six months later — 이 문장은 무엇을 근거로
// 적힌 것인가 — had no answer anywhere.
//
// One shape for every origin. Per-connector tables would make "how much
// evidence is behind this" a different query for each kind, and the point of
// the model is that a reader can compare.

const (
	sourceManual     = "MANUAL"
	sourceConfluence = "CONFLUENCE"
	sourcePPTX       = "PPTX"
	sourceAIText     = "AI_TEXT"
)

type itemSource struct {
	Kind       string     `json:"kind"`
	Reference  string     `json:"reference,omitempty"`
	Title      string     `json:"title,omitempty"`
	URL        string     `json:"url,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
}

// recordItemSources writes provenance for one report item. Called at the moment
// the item is created from something, which is the only moment the origin is
// still known.
func recordItemSources(ctx context.Context, tx pgx.Tx, reportItemID int64, sources []itemSource) error {
	for _, source := range sources {
		if strings.TrimSpace(source.Kind) == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO report_item_sources(report_item_id,kind,reference,title,url,detail,occurred_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)`,
			reportItemID, source.Kind, trimRunes(source.Reference, 240), trimRunes(source.Title, 240),
			source.URL, source.Detail, source.OccurredAt); err != nil {
			return err
		}
	}
	return nil
}

// sourcesForReport reads the evidence behind every item of one report, keyed by
// item so the caller does not have to regroup it.
func (a *App) sourcesForReport(ctx context.Context, reportID int64) (map[int64][]itemSource, error) {
	grouped := map[int64][]itemSource{}
	rows, err := a.db.Query(ctx, `SELECT s.report_item_id, s.kind, s.reference, s.title, s.url, s.detail, s.occurred_at
		FROM report_item_sources s JOIN report_items i ON i.id = s.report_item_id
		WHERE i.report_id = $1 ORDER BY s.report_item_id, s.id`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID int64
		var source itemSource
		if err := rows.Scan(&itemID, &source.Kind, &source.Reference, &source.Title,
			&source.URL, &source.Detail, &source.OccurredAt); err != nil {
			return nil, err
		}
		grouped[itemID] = append(grouped[itemID], source)
	}
	return grouped, rows.Err()
}

// candidateSources reads the Confluence pages behind one auto-draft, shaped for
// the lineage model.
//
// A candidate's pages lived in candidate_sources and stopped there: accepting
// the draft marked the candidate and never carried its evidence onto the line
// it became. This is the copy that closes that gap, made at save time because
// that is when the item first exists.
func (a *App) candidateSources(ctx context.Context, tx pgx.Tx, candidateID, ownerID int64) ([]itemSource, error) {
	rows, err := tx.Query(ctx, `SELECT p.page_id, p.title, p.page_url, cs.page_version, cs.activity_type, cs.source_updated_at
		FROM candidate_sources cs
		JOIN confluence_pages p ON p.id = cs.confluence_page_id
		JOIN report_candidates c ON c.id = cs.candidate_id
		WHERE cs.candidate_id = $1 AND c.user_id = $2
		ORDER BY cs.confluence_page_id`, candidateID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := []itemSource{}
	for rows.Next() {
		var pageID, title, url, activity string
		var version int
		var updated *time.Time
		if err := rows.Scan(&pageID, &title, &url, &version, &activity, &updated); err != nil {
			return nil, err
		}
		detail := "v" + strconv.Itoa(version)
		switch activity {
		case "CREATED":
			detail += " · 신규 작성"
		case "CREATED_AND_MODIFIED":
			detail += " · 작성 후 수정"
		default:
			detail += " · 수정"
		}
		sources = append(sources, itemSource{
			Kind: sourceConfluence, Reference: pageID, Title: trimRunes(title, 240),
			URL: url, Detail: detail, OccurredAt: updated,
		})
	}
	return sources, rows.Err()
}

// sourcesForSavedItem is what a freshly written item should carry, given what
// the request said it came from. Ownership is checked inside candidateSources:
// a candidate identifier is supplied by the client, so it may not name a draft
// that belongs to the caller.
func (a *App) sourcesForSavedItem(ctx context.Context, tx pgx.Tx, item reportItem, ownerID int64) ([]itemSource, error) {
	if item.CandidateID == nil || *item.CandidateID <= 0 {
		return nil, nil
	}
	return a.candidateSources(ctx, tx, *item.CandidateID, ownerID)
}

// evidenceUse is one report line that rests on a given source.
type evidenceUse struct {
	ReportID         int64  `json:"reportId"`
	ReportItemID     int64  `json:"reportItemId"`
	WeekStart        string `json:"weekStart"`
	Title            string `json:"title"`
	Category         string `json:"category"`
	DisplayName      string `json:"displayName"`
	OrganizationName string `json:"organizationName"`
	Detail           string `json:"detail,omitempty"`
}

type evidenceUseView struct {
	Kind      string        `json:"kind"`
	Reference string        `json:"reference"`
	Title     string        `json:"title,omitempty"`
	Uses      []evidenceUse `json:"uses"`
	Total     int           `json:"total"`
	Limit     int           `json:"limit"`
}

const evidenceUseLimit = 50

// evidenceUses answers the question the lineage model was built to make
// askable, from the other end: 이 페이지가 어느 보고에 근거로 쓰였나.
//
// Forward lineage tells a reader of one report where its lines came from.
// This tells the owner of a page, a deck or a commit what was built on it —
// which is who finds out last, and who most needs to know before changing it.
//
// Scoped like everything else: a source may be cited by work the caller cannot
// read, and the count says how many of those there are without naming them.
func (a *App) evidenceUses(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("kind")))
	reference := strings.TrimSpace(r.URL.Query().Get("reference"))
	if kind == "" || reference == "" {
		writeError(w, http.StatusBadRequest, "INVALID_EVIDENCE", "출처 종류와 식별자를 함께 지정하세요.")
		return
	}
	p := currentPrincipal(r.Context())
	limit := clampQueryInt(r, "limit", evidenceUseLimit, 1, evidenceUseLimit)

	// The scope predicate names w and u. The report itself is aliased as w
	// because it carries user_id, and its author as u.
	//
	// An earlier version joined work_items to supply the alias, with
	// `w.id = coalesce(i.work_item_id, w.id)` to tolerate items that have no
	// work item. That degenerates to w.id = w.id and joins every one of that
	// person's tasks, multiplying each citation into as many rows as they have
	// work — a count that would have read as dozens of uses for one.
	scope := scopeForPrincipal(p, false)
	where, args := scope.where(3)
	args = append([]any{kind, reference}, args...)

	total := 0
	if err := a.db.QueryRow(r.Context(), `SELECT count(*)
		FROM report_item_sources s
		JOIN report_items i ON i.id = s.report_item_id
		JOIN weekly_reports w ON w.id = i.report_id
		JOIN users u ON u.id = w.user_id
		WHERE s.kind = $1 AND s.reference = $2`+where, args...).Scan(&total); err != nil {
		a.logger.Error("evidence uses count", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "근거 사용처를 조회할 수 없습니다.")
		return
	}
	args = append(args, limit)
	rows, err := a.db.Query(r.Context(), `SELECT w.id, i.id, w.week_start::text, i.title, i.category,
			coalesce(u.display_name,''), coalesce(o.name,''), s.detail, s.title
		FROM report_item_sources s
		JOIN report_items i ON i.id = s.report_item_id
		JOIN weekly_reports w ON w.id = i.report_id
		JOIN users u ON u.id = w.user_id
		LEFT JOIN organizations o ON o.id = u.organization_id
		WHERE s.kind = $1 AND s.reference = $2`+where+`
		ORDER BY w.week_start DESC, i.id
		LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		a.logger.Error("evidence uses", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "근거 사용처를 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	view := evidenceUseView{Kind: kind, Reference: reference, Uses: []evidenceUse{}, Total: total, Limit: limit}
	for rows.Next() {
		var use evidenceUse
		var sourceTitle string
		if err := rows.Scan(&use.ReportID, &use.ReportItemID, &use.WeekStart, &use.Title, &use.Category,
			&use.DisplayName, &use.OrganizationName, &use.Detail, &sourceTitle); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "근거 사용처를 조회할 수 없습니다.")
			return
		}
		if view.Title == "" {
			view.Title = sourceTitle
		}
		view.Uses = append(view.Uses, use)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "근거 사용처를 조회할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}
