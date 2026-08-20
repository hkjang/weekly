package app

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Last week's plans, served to whoever is writing this week's report.
//
// Writing a weekly report without them in front of you is the single largest
// cost in the whole product: the author reopens the previous report in another
// tab, reads what they promised, and retypes the titles. Everything needed to
// remove that is already stored, so this is a query, not a feature.
//
// It also makes the "planned last week, not reported this week" gap visible at
// the moment it can still be fixed, which is the point of the reporting-quality
// rules planned for v0.15 — the author sees the gap before submitting rather
// than a reviewer seeing it afterwards.

type previousPlanItem struct {
	WorkItemID *int64 `json:"workItemId,omitempty"`
	Category   string `json:"category"`
	Title      string `json:"title"`
	// MatchKey lets the editor pair a plan with an item that has not been saved
	// yet and therefore has no identifier to match on.
	MatchKey string `json:"matchKey"`
	NextPlan string `json:"nextPlan"`
	Issue    string `json:"issue"`
	Progress int    `json:"progress"`
	// CarryOver marks a plan that is still owed. Work reported at 100% is done
	// and work with no plan was never promised, so neither should be offered
	// back to the author as something to continue.
	CarryOver bool `json:"carryOver"`
}

type previousPlanView struct {
	ReportID  int64              `json:"reportId"`
	WeekStart string             `json:"weekStart"`
	Status    string             `json:"status"`
	Items     []previousPlanItem `json:"items"`
}

// planMatchKey pairs a plan with this week's item by title.
//
// Removing spaces and folding case is the whole rule, deliberately: the same
// exact-match-after-normalization used to merge word cloud variants, for the
// same measured reason. Trigram similarity cannot separate titles that differ
// only in spacing from titles that differ in one meaningful character, so a
// looser rule here would silently attach last week's plan to a different task.
func planMatchKey(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), ""))
}

func carriesOver(nextPlan string, progress int) bool {
	return strings.TrimSpace(nextPlan) != "" && progress < 100
}

// previousWeekPlan returns the plans from the author's most recent earlier
// report. It is the report before the target week, not the report exactly one
// week earlier: someone returning from leave should still see what they left
// behind rather than an empty panel.
func (a *App) previousWeekPlan(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	location := a.serviceLocation(r.Context())
	weekday := a.setting(r.Context(), "workflow.week_start", "MONDAY")
	target := currentWeekStart(time.Now().In(location), weekday)
	if requested := strings.TrimSpace(r.URL.Query().Get("weekStart")); requested != "" {
		parsed, err := time.ParseInLocation("2006-01-02", requested, location)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_WEEK", "주차 시작일이 올바르지 않습니다.")
			return
		}
		target = parsed
	}

	view := &previousPlanView{Items: []previousPlanItem{}}
	var week time.Time
	err := a.db.QueryRow(r.Context(), `SELECT id, week_start, status FROM weekly_reports
		WHERE user_id=$1 AND week_start < $2 ORDER BY week_start DESC LIMIT 1`, p.ID, target).
		Scan(&view.ReportID, &week, &view.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeData(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		a.logger.Error("load previous week plan", "error", err, "userId", p.ID)
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "지난 보고서를 조회할 수 없습니다.")
		return
	}
	view.WeekStart = week.Format("2006-01-02")

	rows, err := a.db.Query(r.Context(), `SELECT work_item_id, category, title, next_plan, issue, progress
		FROM report_items WHERE report_id=$1 ORDER BY sort_order, id`, view.ReportID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "지난 보고서 항목을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item previousPlanItem
		if err := rows.Scan(&item.WorkItemID, &item.Category, &item.Title, &item.NextPlan, &item.Issue, &item.Progress); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "지난 보고서 항목을 조회할 수 없습니다.")
			return
		}
		item.MatchKey = planMatchKey(item.Title)
		item.CarryOver = carriesOver(item.NextPlan, item.Progress)
		view.Items = append(view.Items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "지난 보고서 항목을 조회할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}
