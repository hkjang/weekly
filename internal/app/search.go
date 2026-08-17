package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Content search across the reports the caller is allowed to read. Korean text
// has no reliable whitespace tokenization without a database extension, so this
// stays on case-insensitive substring matching, which behaves predictably
// offline and needs no contrib modules.

const (
	searchMaxTerms      = 6
	searchScanLimit     = 2000
	searchReportLimit   = 30
	searchSnippetPerHit = 3
	searchSnippetRunes  = 68
)

type searchMatch struct {
	Field   string `json:"field"`
	Label   string `json:"label"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet"`
}

type searchHit struct {
	ReportID    int64         `json:"reportId"`
	UserID      int64         `json:"userId"`
	DisplayName string        `json:"displayName"`
	WeekStart   string        `json:"weekStart"`
	Status      string        `json:"status"`
	SourceType  string        `json:"sourceType"`
	Matches     []searchMatch `json:"matches"`
	Score       int           `json:"score"`
}

type searchResponse struct {
	Query     string      `json:"query"`
	Terms     []string    `json:"terms"`
	Hits      []searchHit `json:"hits"`
	Truncated bool        `json:"truncated"`
}

// searchTerms splits the query into at most searchMaxTerms non-empty terms.
func searchTerms(query string) []string {
	fields := strings.Fields(strings.TrimSpace(query))
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			result = append(result, field)
		}
		if len(result) == searchMaxTerms {
			break
		}
	}
	return result
}

// escapeLikePattern neutralises the wildcards so a query containing % or _ looks
// for those characters instead of matching everything.
func escapeLikePattern(value string) string {
	replaced := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
	return "%" + replaced + "%"
}

func (a *App) searchReports(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	terms := searchTerms(query)
	if len(terms) == 0 || runeLength(query) < 2 {
		writeData(w, http.StatusOK, searchResponse{Query: query, Terms: []string{}, Hits: []searchHit{}})
		return
	}
	p := currentPrincipal(r.Context())

	// Searchable text of one report plus one of its items.
	const fields = `coalesce(r.summary,''),coalesce(i.title,''),coalesce(i.category,''),
		coalesce(i.current_result,''),coalesce(i.next_plan,''),coalesce(i.issue,'')`
	statement := `SELECT r.id,r.user_id,u.display_name,r.week_start,r.status,r.source_type,` + fields + `
		FROM weekly_reports r JOIN users u ON u.id=r.user_id
		LEFT JOIN report_items i ON i.report_id=r.id
		WHERE 1=1`
	args := []any{}
	switch {
	case p.Role == "ADMIN":
		// Every report is visible.
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
	for _, term := range terms {
		args = append(args, escapeLikePattern(term))
		position := len(args)
		statement += fmt.Sprintf(` AND (r.summary ILIKE $%d OR i.title ILIKE $%d OR i.category ILIKE $%d
			OR i.current_result ILIKE $%d OR i.next_plan ILIKE $%d OR i.issue ILIKE $%d)`,
			position, position, position, position, position, position)
	}
	statement += fmt.Sprintf(` ORDER BY r.week_start DESC,r.id DESC,i.sort_order NULLS FIRST LIMIT %d`, searchScanLimit)

	rows, err := a.db.Query(r.Context(), statement, args...)
	if err != nil {
		a.logger.Error("search reports", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "검색을 수행할 수 없습니다.")
		return
	}
	defer rows.Close()

	order := []int64{}
	byReport := map[int64]*searchHit{}
	scanned := 0
	for rows.Next() {
		var reportID, userID int64
		var displayName, status, sourceType string
		var week time.Time
		var summary, title, category, current, next, issue string
		if err := rows.Scan(&reportID, &userID, &displayName, &week, &status, &sourceType,
			&summary, &title, &category, &current, &next, &issue); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "검색 결과를 읽을 수 없습니다.")
			return
		}
		scanned++
		hit, exists := byReport[reportID]
		if !exists {
			hit = &searchHit{
				ReportID: reportID, UserID: userID, DisplayName: displayName,
				WeekStart: week.Format("2006-01-02"), Status: status, SourceType: sourceType,
				Matches: []searchMatch{},
			}
			byReport[reportID] = hit
			order = append(order, reportID)
		}
		appendSearchMatches(hit, terms, summary, title, category, current, next, issue)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "검색 결과를 읽을 수 없습니다.")
		return
	}

	hits := make([]searchHit, 0, len(order))
	for _, reportID := range order {
		hits = append(hits, *byReport[reportID])
	}
	// Newest first, but a report whose titles matched outranks one that only
	// matched deep in a body field.
	sort.SliceStable(hits, func(left, right int) bool {
		if hits[left].Score != hits[right].Score {
			return hits[left].Score > hits[right].Score
		}
		return hits[left].WeekStart > hits[right].WeekStart
	})
	truncated := scanned >= searchScanLimit || len(hits) > searchReportLimit
	if len(hits) > searchReportLimit {
		hits = hits[:searchReportLimit]
	}
	writeData(w, http.StatusOK, searchResponse{Query: query, Terms: terms, Hits: hits, Truncated: truncated})
}

// appendSearchMatches records the highest value snippets for one scanned row.
func appendSearchMatches(hit *searchHit, terms []string, summary, title, category, current, next, issue string) {
	candidates := []struct {
		field, label, value string
		weight              int
	}{
		{"title", "업무", title, 50},
		{"category", "구분", category, 30},
		{"summary", "주간 요약", summary, 25},
		{"currentResult", "실적", current, 20},
		{"nextPlan", "계획", next, 15},
		{"issue", "이슈", issue, 15},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.value) == "" {
			continue
		}
		snippet, ok := buildSnippet(candidate.value, terms)
		if !ok {
			continue
		}
		if candidate.weight > hit.Score {
			hit.Score = candidate.weight
		}
		if len(hit.Matches) >= searchSnippetPerHit {
			continue
		}
		match := searchMatch{Field: candidate.field, Label: candidate.label, Snippet: snippet}
		if candidate.field != "title" && candidate.field != "summary" {
			match.Title = strings.TrimSpace(title)
		}
		// Skip a snippet the caller has already been shown for this report.
		for _, existing := range hit.Matches {
			if existing.Snippet == match.Snippet && existing.Field == match.Field {
				return
			}
		}
		hit.Matches = append(hit.Matches, match)
	}
}

// buildSnippet returns a window of text around the first matching term with
// ellipses marking the trimmed sides.
func buildSnippet(value string, terms []string) (string, bool) {
	flat := strings.Join(strings.Fields(strings.ReplaceAll(value, "\n", " ")), " ")
	if flat == "" {
		return "", false
	}
	lower := strings.ToLower(flat)
	index := -1
	length := 0
	for _, term := range terms {
		if found := strings.Index(lower, strings.ToLower(term)); found >= 0 && (index < 0 || found < index) {
			index = found
			length = len(term)
		}
	}
	if index < 0 {
		return "", false
	}
	runes := []rune(flat)
	// Convert the byte offset of the hit into a rune offset.
	start := len([]rune(flat[:index]))
	end := start + len([]rune(flat[index:index+length]))
	window := searchSnippetRunes
	from := start - window/3
	if from < 0 {
		from = 0
	}
	to := from + window
	if to > len(runes) {
		to = len(runes)
		if from > to-window && to-window > 0 {
			from = to - window
		}
	}
	if end > to {
		to = end
		if to > len(runes) {
			to = len(runes)
		}
	}
	snippet := string(runes[from:to])
	if from > 0 {
		snippet = "…" + snippet
	}
	if to < len(runes) {
		snippet += "…"
	}
	return snippet, true
}
