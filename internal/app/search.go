package app

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Content search across the reports the caller is allowed to read, in three
// passes that run only as far as they need to.
//
//  1. Case-insensitive substring match. Always available, needs no extension,
//     and answers most queries. Korean has no reliable whitespace tokenization
//     without a morphological analyzer, so this stays on substrings.
//  2. Trigram similarity, when pg_trgm is installed. Catches misspellings and
//     the different endings Korean attaches to the same noun.
//  3. Embedding similarity, when pgvector is installed and an embedding model
//     is configured. Catches work described in entirely different words.
//
// Each pass runs only when the previous ones came back thin, and skips the
// reports they already returned. Passes 2 and 3 are optional: with neither, the
// search still works exactly as it did before they existed.

const (
	searchMaxTerms = 6
	// How thin an exact-match result has to be before the approximate and
	// meaning-based passes are worth their cost. Named because the published
	// contract states the figure and TestTheContractsNumbersAreTheOnesTheCodeUses
	// compares the two.
	searchThinResult = 5
	searchScanLimit  = 2000
	// The budget for the title and category pass, sized to fill the response
	// several times over.
	//
	// Lowering it does not make the pass cheaper, which is worth knowing before
	// somebody tries: ordering by date means every title match in the whole
	// history has to be found before the newest can be taken, so the work is the
	// same at 150 as at 500. Measured on 109,175 items, the pass costs about
	// 50–70 ms either way — and that is the price of looking through all of
	// history, which is exactly what the date-ordered scan alone refuses to do.
	searchPriorityScanLimit = 5 * searchReportLimit
	searchReportLimit       = 30
	searchSnippetPerHit     = 3
	searchSnippetRunes      = 68
)

type searchMatch struct {
	Field   string `json:"field"`
	Label   string `json:"label"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet"`
}

type searchHit struct {
	Approximate bool          `json:"approximate"`
	Semantic    bool          `json:"semantic"`
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
	// Fuzzy reports that some hits came from trigram similarity rather than an
	// exact substring, so the caller can say so instead of implying an exact match.
	Fuzzy bool `json:"fuzzy"`
	// Semantic reports that meaning-based matches are included.
	Semantic bool `json:"semantic"`
	// Reason says why a thin result stayed thin: which of the two widening
	// passes could not run, and why. Without it a search whose widening passes
	// both failed looks exactly like a search that widened and still found
	// nothing — and the reader concludes the report does not exist. The work
	// search already answers this question on its own screen; this is the same
	// answer for the report search.
	Reason string `json:"reason,omitempty"`
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
			statement += fmt.Sprintf(` AND (r.user_id=$%d OR u.organization_id IN `, len(args)-1) + orgSubtree(len(args)) + `)`
		}
	default:
		args = append(args, p.ID)
		statement += fmt.Sprintf(" AND r.user_id=$%d", len(args))
	}
	termPositions := make([]int, 0, len(terms))
	for _, term := range terms {
		args = append(args, escapeLikePattern(term))
		position := len(args)
		termPositions = append(termPositions, position)
		statement += fmt.Sprintf(` AND (r.summary ILIKE $%d OR i.title ILIKE $%d OR i.category ILIKE $%d
			OR i.current_result ILIKE $%d OR i.next_plan ILIKE $%d OR i.issue ILIKE $%d)`,
			position, position, position, position, position, position)
	}
	order, byReport, scanned, priorityScanned, err := a.searchScan(r.Context(), statement, args, termPositions, terms)
	if err != nil {
		a.logger.Error("search reports", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "검색을 수행할 수 없습니다.")
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
	// A substring search finds nothing when the query is misspelled or uses a
	// different ending. When trigram similarity is available and the exact pass
	// came back thin, offer approximate matches rather than an empty screen.
	fuzzy := false
	// Only ever said about a thin result: a full page of exact matches did not
	// need widening, and telling that reader which extension is missing is
	// noise about a question they never asked.
	notes := []string{}
	thin := len(hits) < searchThinResult
	if thin && !a.capabilities.Trigram {
		notes = append(notes, "이 데이터베이스에 pg_trgm 확장이 없어 오타나 어미가 다른 표기는 찾지 못했습니다.")
	}
	if a.capabilities.Trigram && thin {
		approximate, approxErr := a.searchApproximate(r, p, terms, byReport)
		if approxErr != nil {
			a.logger.Warn("approximate search", "error", approxErr, "trace", traceIDFromContext(r.Context()))
			notes = append(notes, "유사 검색이 실패해 글자가 그대로 일치하는 결과만 보여 줍니다. 서버 로그에서 원인을 확인하세요.")
		} else if len(approximate) > 0 {
			fuzzy = true
			hits = append(hits, approximate...)
			// The semantic pass skips reports already found, so what the
			// trigram pass returned has to be recorded here too. Without this
			// the same report can appear twice under two different labels.
			for index := range approximate {
				byReport[approximate[index].ReportID] = &approximate[index]
			}
		}
	}

	// Meaning based matching finds work described in different words entirely.
	// It costs an embedding call, so it only runs when the cheaper passes came
	// back thin and the feature is configured.
	semantic := false
	if thin {
		// The configuration layer already writes these sentences for a person
		// to read — the 검색 설정 line prints the same text. Throwing them away
		// here and showing nothing was the whole defect.
		if _, cfgErr := a.embeddingConfig(r.Context()); cfgErr != nil {
			notes = append(notes, cfgErr.Error())
		} else {
			matches, semanticErr := a.searchSemantic(r, p, query, byReport)
			if semanticErr != nil {
				a.logger.Warn("semantic search", "error", semanticErr, "trace", traceIDFromContext(r.Context()))
				notes = append(notes, "의미 검색이 실패해 글자 일치 결과만 보여 줍니다. 서버 로그에서 원인을 확인하세요.")
			} else if len(matches) > 0 {
				semantic = true
				hits = append(hits, matches...)
			}
		}
	}

	truncated := scanned >= searchScanLimit || priorityScanned >= searchPriorityScanLimit || len(hits) > searchReportLimit
	if len(hits) > searchReportLimit {
		hits = hits[:searchReportLimit]
	}
	writeData(w, http.StatusOK, searchResponse{Query: query, Terms: terms, Hits: hits, Truncated: truncated,
		Fuzzy: fuzzy, Semantic: semantic, Reason: strings.Join(notes, " ")})
}

// appendSearchMatches records the highest value snippets for one scanned row.
// values are in searchFieldWeights order, so the score here and the score the
// scan ordered by cannot drift apart.
func appendSearchMatches(hit *searchHit, terms []string, values []string) {
	for index, candidate := range searchFieldWeights {
		if index >= len(values) || strings.TrimSpace(values[index]) == "" {
			continue
		}
		snippet, ok := buildSnippet(values[index], terms)
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
			match.Title = strings.TrimSpace(values[0])
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
// searchScan runs the two passes that feed a search response and returns the
// hits in scan order.
//
// The broad pass is newest-first and stops at searchScanLimit rows, so it
// spends its whole budget on whatever happened recently. A work item whose
// *title* is the query is then unreachable the moment the same words appear in
// enough recent paragraphs — and the screen still calls the survivors the top
// results. Ordering that pass by score instead fixes it and costs seven times
// the latency on a common word, because every matching row has to be scored to
// discover that nearly all of them score the same.
//
// So the first pass asks only for the fields that outrank a paragraph. It has
// to look through all of history, which is the ~60 ms the date-ordered scan
// avoids by being wrong.
func (a *App) searchScan(ctx context.Context, base string, args []any, termPositions []int, terms []string) (
	[]int64, map[int64]*searchHit, int, int, error) {
	const ordering = ` ORDER BY r.week_start DESC,r.id DESC,i.sort_order NULLS FIRST LIMIT `

	order := []int64{}
	byReport := map[int64]*searchHit{}
	collect := func(query string) (int, error) {
		rows, queryErr := a.db.Query(ctx, query, args...)
		if queryErr != nil {
			return 0, queryErr
		}
		defer rows.Close()
		scanned := 0
		for rows.Next() {
			var reportID, userID int64
			var displayName, status, sourceType string
			var week time.Time
			var summary, title, category, current, next, issue string
			if scanErr := rows.Scan(&reportID, &userID, &displayName, &week, &status, &sourceType,
				&summary, &title, &category, &current, &next, &issue); scanErr != nil {
				return scanned, scanErr
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
			appendSearchMatches(hit, terms, []string{title, category, summary, current, next, issue})
		}
		return scanned, rows.Err()
	}

	priorityMatches := make([]string, 0, len(termPositions)*2)
	for _, position := range termPositions {
		priorityMatches = append(priorityMatches,
			fmt.Sprintf("i.title ILIKE $%d", position), fmt.Sprintf("i.category ILIKE $%d", position))
	}
	priorityScanned, err := collect(base +
		" AND (" + strings.Join(priorityMatches, " OR ") + ")" +
		ordering + strconv.Itoa(searchPriorityScanLimit))
	if err != nil {
		return nil, nil, 0, 0, err
	}
	scanned, err := collect(base + ordering + strconv.Itoa(searchScanLimit))
	if err != nil {
		return nil, nil, 0, 0, err
	}
	return order, byReport, scanned, priorityScanned, nil
}

// searchFieldWeights ranks where a match was found: a query in a work item's
// title is what somebody meant; the same words deep in a paragraph usually are
// not.
//
// The scan orders by these in SQL and the response scores by them in Go. They
// live in one place because a disagreement between the two is invisible and
// total: the scan would choose one set of rows and the screen would rank a
// different one, presenting whatever survived as the best matches.
var searchFieldWeights = []struct {
	column, field, label string
	weight               int
}{
	{"i.title", "title", "업무", 50},
	{"i.category", "category", "구분", 30},
	{"r.summary", "summary", "주간 요약", 25},
	{"i.current_result", "currentResult", "실적", 20},
	{"i.next_plan", "nextPlan", "계획", 15},
	{"i.issue", "issue", "이슈", 15},
}

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

// searchApproximate finds reports whose text is similar to the query without
// containing it exactly, which covers typos and inflected endings. It only runs
// where pg_trgm is installed and never returns a report the exact pass already
// found.
func (a *App) searchApproximate(r *http.Request, p *principal, terms []string, seen map[int64]*searchHit) ([]searchHit, error) {
	threshold := float64(a.settingInt(r.Context(), "search.similarity_threshold", 35)) / 100
	if threshold <= 0 || threshold >= 1 {
		threshold = 0.35
	}
	needle := strings.Join(terms, " ")
	statement := `SELECT r.id,r.user_id,u.display_name,r.week_start,r.status,r.source_type,
			coalesce(i.title,''), coalesce(i.category,''), coalesce(r.summary,''),
			greatest(
			  word_similarity($1, coalesce(i.title,'')),
			  word_similarity($1, coalesce(i.category,'')),
			  word_similarity($1, coalesce(r.summary,'')),
			  word_similarity($1, coalesce(i.current_result,'')),
			  word_similarity($1, coalesce(i.next_plan,'')),
			  word_similarity($1, coalesce(i.issue,''))
			) AS score
		FROM weekly_reports r JOIN users u ON u.id=r.user_id
		LEFT JOIN report_items i ON i.report_id=r.id
		WHERE 1=1`
	args := []any{needle}
	switch {
	case p.Role == "ADMIN":
	case p.Role == "TEAM_LEADER" || p.Role == "ORG_MANAGER":
		if p.OrganizationID == nil {
			args = append(args, p.ID)
			statement += fmt.Sprintf(" AND r.user_id=$%d", len(args))
		} else {
			args = append(args, p.ID, *p.OrganizationID)
			statement += fmt.Sprintf(` AND (r.user_id=$%d OR u.organization_id IN `, len(args)-1) + orgSubtree(len(args)) + `)`
		}
	default:
		args = append(args, p.ID)
		statement += fmt.Sprintf(" AND r.user_id=$%d", len(args))
	}
	args = append(args, threshold)
	statement += fmt.Sprintf(` AND greatest(
			word_similarity($1, coalesce(i.title,'')),
			word_similarity($1, coalesce(i.category,'')),
			word_similarity($1, coalesce(r.summary,'')),
			word_similarity($1, coalesce(i.current_result,'')),
			word_similarity($1, coalesce(i.next_plan,'')),
			word_similarity($1, coalesce(i.issue,''))) >= $%d
		ORDER BY score DESC, r.week_start DESC LIMIT %d`, len(args), searchReportLimit)

	rows, err := a.db.Query(r.Context(), statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []searchHit{}
	added := map[int64]bool{}
	for rows.Next() {
		var reportID, userID int64
		var displayName, status, sourceType, title, category, summary string
		var week time.Time
		var score float64
		if err := rows.Scan(&reportID, &userID, &displayName, &week, &status, &sourceType,
			&title, &category, &summary, &score); err != nil {
			return nil, err
		}
		if seen[reportID] != nil || added[reportID] {
			continue
		}
		added[reportID] = true
		snippet := strings.TrimSpace(title)
		label := "업무"
		if snippet == "" {
			snippet, label = strings.TrimSpace(summary), "주간 요약"
		}
		if snippet == "" {
			continue
		}
		result = append(result, searchHit{
			Approximate: true, ReportID: reportID, UserID: userID, DisplayName: displayName,
			WeekStart: week.Format("2006-01-02"), Status: status, SourceType: sourceType,
			Score:   int(score * 100),
			Matches: []searchMatch{{Field: "similar", Label: label + " · 유사", Snippet: trimRunes(snippet, 80)}},
		})
	}
	return result, rows.Err()
}
