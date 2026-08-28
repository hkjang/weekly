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
	rows, err := a.db.Query(ctx, `SELECT r.status,count(*) FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE r.week_start=$1`+orgFilter+` GROUP BY r.status`, args...)
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
		if status != "DRAFT" && status != "REVISION_REQUESTED" {
			result.SubmittedUsers += count
		}
	}
	if result.TotalUsers > 0 {
		result.SubmissionRate = float64(result.SubmittedUsers) * 100 / float64(result.TotalUsers)
	}
	itemArgs := []any{week}
	itemFilter := ""
	if len(args) > 1 {
		itemArgs = append(itemArgs, args[1])
		itemFilter = ` AND u.organization_id IN ` + orgSubtree(len(itemArgs))
	}
	err = a.db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE length(trim(i.issue))>0),coalesce(avg(i.progress),0) FROM report_items i JOIN weekly_reports r ON r.id=i.report_id JOIN users u ON u.id=r.user_id WHERE r.week_start=$1`+itemFilter, itemArgs...).Scan(&result.OpenIssues, &result.AverageProgress)
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
