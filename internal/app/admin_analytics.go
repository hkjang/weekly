package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Administrator analytics: what the organisation is working on (keywords),
// how the organisations compare, and whether reporting is actually happening.

const (
	analyticsMaxWeeks     = 104
	analyticsDefaultWeeks = 12
	analyticsTermLimit    = 120
)

type keywordResponse struct {
	Start          string         `json:"start"`
	End            string         `json:"end"`
	Weeks          int            `json:"weeks"`
	Field          string         `json:"field"`
	Documents      int            `json:"documents"`
	Reports        int            `json:"reports"`
	Terms          []analysisTerm `json:"terms"`
	ComparedStart  string         `json:"comparedStart"`
	ComparedEnd    string         `json:"comparedEnd"`
	ComparedReport int            `json:"comparedReports"`
}

// analyticsWindow resolves the requested number of weeks into two adjacent
// windows: the period under review and the one before it for comparison.
func (a *App) analyticsWindow(r *http.Request) (weeks int, start, end, priorStart time.Time) {
	weeks = analyticsDefaultWeeks
	if value := a.settingIntFromQuery(r, "weeks"); value > 0 {
		weeks = value
	}
	if weeks > analyticsMaxWeeks {
		weeks = analyticsMaxWeeks
	}
	location := a.serviceLocation(r.Context())
	weekday := a.setting(r.Context(), "workflow.week_start", "MONDAY")
	current := currentWeekStart(time.Now().In(location), weekday)
	end = current.AddDate(0, 0, 6)
	start = current.AddDate(0, 0, -7*(weeks-1))
	priorStart = start.AddDate(0, 0, -7*weeks)
	return weeks, start, end, priorStart
}

func (a *App) settingIntFromQuery(r *http.Request, name string) int {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return 0
	}
	parsed := 0
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed < 1 {
		return 0
	}
	return parsed
}

// analyticsKeywords powers the word cloud and the keyword trend table.
func (a *App) analyticsKeywords(w http.ResponseWriter, r *http.Request) {
	weeks, start, end, priorStart := a.analyticsWindow(r)
	field := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("field")))
	columns, ok := keywordColumns(field)
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_FIELD", "분석 대상은 ALL, CURRENT, NEXT, ISSUE, TITLE 중 하나여야 합니다.")
		return
	}

	current, currentReports, err := a.collectTerms(r, columns, start, end)
	if err != nil {
		a.logger.Error("keyword analytics", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "키워드를 분석할 수 없습니다.")
		return
	}
	prior, priorReports, err := a.collectTerms(r, columns, priorStart, start.AddDate(0, 0, -1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "비교 기간을 분석할 수 없습니다.")
		return
	}

	writeData(w, http.StatusOK, keywordResponse{
		Start: start.Format("2006-01-02"), End: end.Format("2006-01-02"), Weeks: weeks,
		Field: fieldLabel(field), Documents: current.total, Reports: currentReports,
		Terms:         current.rank(analyticsTermLimit, prior.counted()),
		ComparedStart: priorStart.Format("2006-01-02"), ComparedEnd: start.AddDate(0, 0, -1).Format("2006-01-02"),
		ComparedReport: priorReports,
	})
}

func keywordColumns(field string) ([]string, bool) {
	switch field {
	case "", "ALL":
		return []string{"i.title", "i.category", "i.current_result", "i.next_plan", "i.issue", "r.summary"}, true
	case "TITLE":
		return []string{"i.title", "i.category"}, true
	case "CURRENT":
		return []string{"i.current_result"}, true
	case "NEXT":
		return []string{"i.next_plan"}, true
	case "ISSUE":
		return []string{"i.issue"}, true
	}
	return nil, false
}

func fieldLabel(field string) string {
	switch strings.ToUpper(field) {
	case "TITLE":
		return "업무명"
	case "CURRENT":
		return "금주 실적"
	case "NEXT":
		return "차주 계획"
	case "ISSUE":
		return "이슈"
	default:
		return "전체"
	}
}

// collectTerms builds the term table for one window. One report is one document,
// so document frequency answers "how many reports mention this".
func (a *App) collectTerms(r *http.Request, columns []string, start, end time.Time) (*termAccumulator, int, error) {
	// Newline separated so a phrase never spans two different fields.
	expression := "concat_ws(E'\\n', " + strings.Join(columns, ", ") + ")"
	statement := `SELECT r.id, ` + expression + `
		FROM weekly_reports r LEFT JOIN report_items i ON i.report_id=r.id
		WHERE r.week_start BETWEEN $1 AND $2`
	args := []any{start, end}
	if organizationID := a.settingIntFromQuery(r, "organizationId"); organizationID > 0 {
		args = append(args, organizationID)
		statement += ` AND r.user_id IN (SELECT id FROM users WHERE organization_id IN
			` + orgSubtree(len(args)) + `)`
	}
	statement += ` ORDER BY r.id`

	rows, err := a.db.Query(r.Context(), statement, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	accumulator := newTermAccumulator()
	byReport := map[int64]*strings.Builder{}
	order := []int64{}
	for rows.Next() {
		var reportID int64
		var text string
		if err := rows.Scan(&reportID, &text); err != nil {
			return nil, 0, err
		}
		builder, exists := byReport[reportID]
		if !exists {
			builder = &strings.Builder{}
			byReport[reportID] = builder
			order = append(order, reportID)
		}
		builder.WriteString(text)
		builder.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for _, reportID := range order {
		accumulator.addDocument(byReport[reportID].String())
	}
	return accumulator, len(order), nil
}

type organizationAnalytics struct {
	OrganizationID  int64   `json:"organizationId"`
	Name            string  `json:"name"`
	Members         int     `json:"members"`
	Reports         int     `json:"reports"`
	ExpectedReports int     `json:"expectedReports"`
	SubmissionRate  float64 `json:"submissionRate"`
	Items           int     `json:"items"`
	CompletedItems  int     `json:"completedItems"`
	CompletionRate  float64 `json:"completionRate"`
	IssueItems      int     `json:"issueItems"`
	IssueRate       float64 `json:"issueRate"`
	AverageProgress float64 `json:"averageProgress"`
}

// analyticsOrganizations compares reporting output across organisations.
func (a *App) analyticsOrganizations(w http.ResponseWriter, r *http.Request) {
	weeks, start, end, _ := a.analyticsWindow(r)
	rows, err := a.db.Query(r.Context(), `
		WITH active AS (
		  SELECT u.id, u.organization_id FROM users u WHERE u.active=true
		), submitted AS (
		  SELECT r.id, r.user_id FROM weekly_reports r
		  WHERE r.week_start BETWEEN $1 AND $2 AND r.status <> 'DRAFT'
		)
		SELECT o.id, o.name,
		  count(DISTINCT a.id),
		  count(DISTINCT s.id),
		  count(i.id),
		  count(i.id) FILTER (WHERE i.progress >= 100),
		  count(i.id) FILTER (WHERE length(trim(coalesce(i.issue,''))) > 0),
		  coalesce(avg(i.progress), 0)
		FROM organizations o
		LEFT JOIN active a ON a.organization_id = o.id
		LEFT JOIN submitted s ON s.user_id = a.id
		LEFT JOIN report_items i ON i.report_id = s.id
		GROUP BY o.id, o.name ORDER BY o.name`, start, end)
	if err != nil {
		a.logger.Error("organization analytics", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "조직 분석을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := []organizationAnalytics{}
	for rows.Next() {
		var item organizationAnalytics
		if err := rows.Scan(&item.OrganizationID, &item.Name, &item.Members, &item.Reports,
			&item.Items, &item.CompletedItems, &item.IssueItems, &item.AverageProgress); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "조직 분석을 읽을 수 없습니다.")
			return
		}
		item.ExpectedReports = item.Members * weeks
		if item.ExpectedReports > 0 {
			item.SubmissionRate = round1(float64(item.Reports) * 100 / float64(item.ExpectedReports))
		}
		if item.Items > 0 {
			item.CompletionRate = round1(float64(item.CompletedItems) * 100 / float64(item.Items))
			item.IssueRate = round1(float64(item.IssueItems) * 100 / float64(item.Items))
		}
		item.AverageProgress = round1(item.AverageProgress)
		result = append(result, item)
	}
	writeData(w, http.StatusOK, map[string]any{
		"start": start.Format("2006-01-02"), "end": end.Format("2006-01-02"),
		"weeks": weeks, "organizations": result,
	})
}

type participationWeek struct {
	WeekStart      string  `json:"weekStart"`
	ActiveUsers    int     `json:"activeUsers"`
	Reports        int     `json:"reports"`
	Submitted      int     `json:"submitted"`
	OnTime         int     `json:"onTime"`
	Late           int     `json:"late"`
	SubmissionRate float64 `json:"submissionRate"`
	OnTimeRate     float64 `json:"onTimeRate"`
	// Open marks a week whose deadline has not passed. Its submission rate is a
	// count so far, not a result, and reading it beside finished weeks turns
	// every Monday morning into a collapse.
	Open bool `json:"open"`
}

// deadlineInstant is when a week's report stops being on time, in the service
// timezone. Hour 24 means midnight at the end of that day.
func (rule deadlineRule) instant(weekStart time.Time, location *time.Location) time.Time {
	day := time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), 0, 0, 0, 0, location)
	return day.AddDate(0, 0, rule.Days).Add(time.Duration(rule.Hour) * time.Hour)
}

// missingReporterLimit is how many of the worst offenders the screen lists.
// A ranking is allowed to be a ranking; what it is not allowed to do is hide
// how many people are behind. Twenty-five rows out of sixty people in arrears
// makes the problem look like a quarter of its size.
const missingReporterLimit = 25

type missingReporter struct {
	UserID       int64  `json:"userId"`
	DisplayName  string `json:"displayName"`
	Username     string `json:"username"`
	Organization string `json:"organization"`
	MissedWeeks  int    `json:"missedWeeks"`
	LastWeek     string `json:"lastWeek"`
}

// expectedFromWeek is the earliest week a person can be held to: the earlier of
// when their account appeared and the first week they actually filed.
//
// created_at alone does not survive a migration. A deployment that imported its
// past reports gives every account the go-live date, so every historical week
// would precede it and the participation figure would silently read zero
// missing, forever.
//
// $3 is the service timezone. It is a fragment rather than a copy in two places
// because the list and the total have to agree; a person named in one and
// absent from the other is worse than either number alone.
const expectedFromWeek = `least(
		(u.created_at AT TIME ZONE $3)::date,
		coalesce((SELECT min(r2.week_start) FROM weekly_reports r2
		          WHERE r2.user_id=u.id AND r2.status <> 'DRAFT'), 'infinity'::date))`

// deadlinePassed is true for a week that is over. $3 timezone, $4 days, $5 hour.
const deadlinePassed = `(week.day::date + make_interval(days => $4, hours => $5)) AT TIME ZONE $3 <= now()`

// weekIsOwed combines the two: this week counted against this person, and it is
// no longer open.
const weekIsOwed = `week.day::date >= ` + expectedFromWeek + ` AND ` + deadlinePassed + `
		AND NOT EXISTS (SELECT 1 FROM weekly_reports r
			WHERE r.user_id=u.id AND r.week_start=week.day::date AND r.status <> 'DRAFT')`

// analyticsParticipation reports whether the reporting habit is holding, which
// is the first thing to check before trusting any other number.
func (a *App) analyticsParticipation(w http.ResponseWriter, r *http.Request) {
	weeks, start, end, _ := a.analyticsWindow(r)
	var activeUsers int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE active=true`).Scan(&activeUsers); err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "사용자 수를 조회할 수 없습니다.")
		return
	}
	// The deadline is an instant in the service timezone, not a date in whatever
	// timezone the database session happens to be in. The previous expression
	// cast submitted_at to a date in UTC, so a report handed in at 08:00 KST the
	// day after the deadline was dated to the previous day and counted on time.
	deadline := a.deadlineRule(r.Context())
	rows, err := a.db.Query(r.Context(), `
		SELECT r.week_start,
		  count(*),
		  count(*) FILTER (WHERE r.status <> 'DRAFT'),
		  count(*) FILTER (WHERE r.submitted_at IS NOT NULL AND r.submitted_at < deadline.at),
		  count(*) FILTER (WHERE r.submitted_at IS NOT NULL AND r.submitted_at >= deadline.at)
		FROM weekly_reports r
		CROSS JOIN LATERAL (SELECT (r.week_start + make_interval(days => $3, hours => $4)) AT TIME ZONE $5 AS at) deadline
		WHERE r.week_start BETWEEN $1 AND $2
		GROUP BY r.week_start ORDER BY r.week_start`,
		start, end, deadline.Days, deadline.Hour, deadline.Timezone)
	if err != nil {
		a.logger.Error("participation analytics", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "제출 현황을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	byWeek := map[string]participationWeek{}
	for rows.Next() {
		var week time.Time
		var item participationWeek
		if err := rows.Scan(&week, &item.Reports, &item.Submitted, &item.OnTime, &item.Late); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "제출 현황을 읽을 수 없습니다.")
			return
		}
		item.WeekStart = week.Format("2006-01-02")
		byWeek[item.WeekStart] = item
	}

	location := a.serviceLocation(r.Context())
	now := time.Now()
	trend := []participationWeek{}
	for cursor := start; !cursor.After(end); cursor = cursor.AddDate(0, 0, 7) {
		key := cursor.Format("2006-01-02")
		item, exists := byWeek[key]
		if !exists {
			item = participationWeek{WeekStart: key}
		}
		item.ActiveUsers = activeUsers
		item.Open = deadline.instant(cursor, location).After(now)
		if activeUsers > 0 {
			item.SubmissionRate = round1(float64(item.Submitted) * 100 / float64(activeUsers))
		}
		if item.Submitted > 0 {
			item.OnTimeRate = round1(float64(item.OnTime) * 100 / float64(item.Submitted))
		}
		trend = append(trend, item)
	}

	// Who is behind, ranked by how many of the recent weeks they missed.
	//
	// A week only counts against somebody who could have been expected to file
	// it: after they were here, and after the deadline passed. Without those two
	// conditions this list is headed by whoever joined most recently, and every
	// Monday it names everybody — which is the same as naming nobody.
	//
	// "After they were here" cannot be created_at alone. A deployment that
	// imported its past reports gives every account the go-live date, so every
	// historical week would precede it and the whole metric would silently read
	// zero. The earlier of created_at and their first filed week is the evidence
	// that actually survives a migration.
	missingRows, err := a.db.Query(r.Context(), `
		SELECT u.id, u.display_name, u.username, coalesce(o.name,''),
		  (SELECT count(*) FROM generate_series($1::date, $2::date, interval '7 day') AS week(day)
		     WHERE `+weekIsOwed+`),
		  coalesce((SELECT max(r.week_start)::text FROM weekly_reports r WHERE r.user_id=u.id AND r.status <> 'DRAFT'), '')
		FROM users u LEFT JOIN organizations o ON o.id=u.organization_id
		WHERE u.active=true
		ORDER BY 5 DESC, u.display_name LIMIT $6`,
		start, end, deadline.Timezone, deadline.Days, deadline.Hour, missingReporterLimit)
	if err != nil {
		a.logger.Error("missing reporters", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "미제출자를 조회할 수 없습니다.")
		return
	}
	defer missingRows.Close()
	missing := []missingReporter{}
	for missingRows.Next() {
		var item missingReporter
		if err := missingRows.Scan(&item.UserID, &item.DisplayName, &item.Username, &item.Organization, &item.MissedWeeks, &item.LastWeek); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "미제출자를 읽을 수 없습니다.")
			return
		}
		if item.MissedWeeks > 0 {
			missing = append(missing, item)
		}
	}
	missingTotal := 0
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM users u WHERE u.active=true
		AND (SELECT count(*) FROM generate_series($1::date, $2::date, interval '7 day') AS week(day)
			WHERE `+weekIsOwed+`) > 0`,
		start, end, deadline.Timezone, deadline.Days, deadline.Hour).Scan(&missingTotal); err != nil {
		a.logger.Error("missing reporter total", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "미제출자를 조회할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"start": start.Format("2006-01-02"), "end": end.Format("2006-01-02"),
		"weeks": weeks, "activeUsers": activeUsers, "trend": trend, "missing": missing,
		// How many are behind, beside the few the list names. The list is the
		// worst 25; the count is everyone.
		"missingTotal": missingTotal, "missingLimit": missingReporterLimit,
		// Carried in the response so the screen can state the rule it is
		// reporting against. A punctuality figure whose definition is invisible
		// invites everyone to assume a different one.
		"deadline": deadline,
	})
}

// deadlineRule is the submission deadline, expressed the way a policy is
// stated: so many days after the week starts, by such an hour of that day.
type deadlineRule struct {
	Days     int    `json:"days"`
	Hour     int    `json:"hour"`
	Timezone string `json:"timezone"`
	// Label is the rule in words, so a screen does not have to reassemble it and
	// risk describing it differently from the number beside it.
	Label string `json:"label"`
}

func (a *App) deadlineRule(ctx context.Context) deadlineRule {
	rule := deadlineRule{
		Days:     a.settingInt(ctx, "workflow.deadline_days", 7),
		Hour:     a.settingInt(ctx, "workflow.deadline_hour", 24),
		Timezone: a.setting(ctx, "service.timezone", "Asia/Seoul"),
	}
	// "N일째 되는 날 자정" reads two ways — the midnight that starts that day or
	// the one that ends it — and the two are a full day apart. The rule itself is
	// unchanged; only the sentence describing it now says which one.
	if rule.Hour == 24 {
		rule.Label = fmt.Sprintf("주차 시작일로부터 %d일 뒤, 그날이 끝나는 자정까지 (%s)", rule.Days, rule.Timezone)
	} else {
		rule.Label = fmt.Sprintf("주차 시작일로부터 %d일 뒤 %d시까지 (%s)", rule.Days, rule.Hour, rule.Timezone)
	}
	return rule
}
