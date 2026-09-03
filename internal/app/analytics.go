package app

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type analyticsOverviewView struct {
	WeekStart       string         `json:"weekStart"`
	TotalUsers      int            `json:"totalUsers"`
	SubmittedUsers  int            `json:"submittedUsers"`
	SubmissionRate  float64        `json:"submissionRate"`
	StatusCounts    map[string]int `json:"statusCounts"`
	OpenIssues      int            `json:"openIssues"`
	AverageProgress float64        `json:"averageProgress"`
}

func (a *App) analyticsOverview(w http.ResponseWriter, r *http.Request) {
	week := strings.TrimSpace(r.URL.Query().Get("weekStart"))
	if week == "" {
		week = currentWeekStart(time.Now().In(a.serviceLocation(r.Context())), a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format("2006-01-02")
	}
	result, err := a.analyticsOverviewData(r.Context(), currentPrincipal(r.Context()), week)
	if err != nil {
		a.logger.Error("analytics overview", "error", err)
		writeError(w, 500, "QUERY_FAILED", "분석 정보를 조회할 수 없습니다.")
		return
	}
	writeData(w, 200, result)
}

func (a *App) analyticsOverviewData(ctx context.Context, p *principal, week string) (analyticsOverviewView, error) {
	return a.analyticsOverviewContext(ctx, p, week)
}

func (a *App) analyticsOverviewContext(ctx context.Context, p *principal, week string) (analyticsOverviewView, error) {
	result := analyticsOverviewView{WeekStart: week, StatusCounts: map[string]int{}}
	orgFilter := ""
	args := []any{week}
	if p != nil && p.Role != "ADMIN" && p.OrganizationID != nil {
		args = append(args, *p.OrganizationID)
		orgFilter = ` AND u.organization_id IN ` + orgSubtree(len(args))
	}
	if p != nil && p.Role != "ADMIN" && p.OrganizationID == nil {
		return result, nil
	}
	totalFilter := ""
	totalArgs := []any{}
	if len(args) > 1 {
		totalArgs = append(totalArgs, args[1])
		totalFilter = ` AND u.organization_id IN ` + orgSubtree(1) + ``
	}
	err := a.db.QueryRow(ctx, `SELECT count(*) FROM users u WHERE u.active=true`+totalFilter, totalArgs...).Scan(&result.TotalUsers)
	if err != nil {
		return result, err
	}
	// The seven days the week covers, not the date the grid now names.
	//
	// After the administrator moves the week start, the reports the team filed
	// on the old grid cover these same days under an earlier date, and
	// weekIsFree will not let anyone file a second one on the new date. Asked by
	// exact date, this screen answered the transition week with 제출률 0% and an
	// empty status breakdown for a week in which everybody had reported — and
	// the same figures leave the product through the MCP week summary, where
	// there is no screen to notice they are impossible.
	//
	// At most one report covers any seven days, so DISTINCT ON only decides
	// between rows written before weekIsFree did; the latest is the one still
	// being written in, the same choice currentReport makes.
	rows, err := a.db.Query(ctx, `SELECT status,count(*) FROM (
		SELECT DISTINCT ON (r.user_id) r.status FROM weekly_reports r JOIN users u ON u.id=r.user_id
		WHERE `+weekCoveringDays("r", 1)+orgFilter+`
		ORDER BY r.user_id,r.week_start DESC) covering GROUP BY status`, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return result, err
		}
		result.StatusCounts[status] = count
		// 제출 means the report was handed in, which a 반려 does not undo: the
		// leader sent it back, and 제출시각 is still on it.
		//
		// This used to exclude REVISION_REQUESTED while the administrator's
		// 참여 분석 counted it, and both screens call the number 제출률.
		// Measured on one deployment, the same week: 관리자 91.1%, 화면 82.0%,
		// and the 9.1 points were exactly the 28 reports sent back. A reader
		// cannot tell which of two numbers with the same name is the answer.
		//
		// The administrator's definition wins because its 미제출 명단 is built
		// on it: counting a returned report as unfiled would put somebody who
		// wrote and was asked to revise into the same list as somebody who
		// wrote nothing. What is still open is on the same screen already —
		// the status breakdown beside this number names 반려 on its own.
		if status != "DRAFT" {
			result.SubmittedUsers += count
		}
	}
	if result.TotalUsers > 0 {
		// round1, as every other analytics figure in this product is rounded.
		// This file was the only one that never called it, and the difference
		// leaves the API: 참여 분석 answers 90.8 and this endpoint answered
		// 90.78947368421052 for the same week and the same people. The screen
		// formats it, so the screen was fine; the API and the MCP tool built on
		// it were not, and an AI client asked to summarise a week was handed
		// sixteen digits of a percentage to quote.
		result.SubmissionRate = round1(float64(result.SubmittedUsers) * 100 / float64(result.TotalUsers))
	}
	itemArgs := []any{week}
	itemFilter := ""
	if len(args) > 1 {
		itemArgs = append(itemArgs, args[1])
		itemFilter = ` AND u.organization_id IN ` + orgSubtree(len(itemArgs))
	}
	// The same reports the breakdown above counted, so 미해결 이슈 and 평균 진행률
	// cannot describe a different set of people than 제출률 does.
	err = a.db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE length(trim(i.issue))>0),coalesce(avg(i.progress),0)
		FROM report_items i WHERE i.report_id IN (
			SELECT DISTINCT ON (r.user_id) r.id FROM weekly_reports r JOIN users u ON u.id=r.user_id
			WHERE `+weekCoveringDays("r", 1)+itemFilter+`
			ORDER BY r.user_id,r.week_start DESC)`, itemArgs...).Scan(&result.OpenIssues, &result.AverageProgress)
	result.AverageProgress = round1(result.AverageProgress)
	return result, err
}

func (a *App) analyticsEndpoints(w http.ResponseWriter, r *http.Request) {
	result, err := a.endpointAnalytics(r.Context())
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "API 분석 정보를 조회할 수 없습니다.")
		return
	}
	writeData(w, 200, result)
}

func (a *App) endpointAnalytics(ctx context.Context) ([]map[string]any, error) {
	// Aggregates over an empty FILTER set return NULL, so every column that can be
	// filtered down to zero rows must fall back to 0 before it reaches Scan.
	rows, err := a.db.Query(ctx, `SELECT method,route,coalesce(sum(request_count),0)::bigint,
		CASE WHEN sum(request_count)>0 THEN round(sum(duration_ms_sum)::numeric/sum(request_count),2) ELSE 0 END,
		coalesce(max(duration_ms_max),0),coalesce(sum(request_count) FILTER(WHERE status>=500),0)::bigint,
		coalesce(round(100.0*coalesce(sum(request_count) FILTER(WHERE status>=400),0)/NULLIF(sum(request_count),0),2),0)
		FROM api_request_metrics WHERE bucket>=now()-interval '24 hours' GROUP BY method,route ORDER BY sum(request_count) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var method, route string
		var requests, maxMS, errors5xx int64
		var avgMS, errorRate float64
		if err := rows.Scan(&method, &route, &requests, &avgMS, &maxMS, &errors5xx, &errorRate); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"method": method, "route": route, "requests": requests, "averageMs": avgMS, "maxMs": maxMS, "serverErrors": errors5xx, "errorRate": errorRate})
	}
	return result, rows.Err()
}
