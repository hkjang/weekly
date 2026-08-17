package app

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	scopeSelf = "SELF"
	scopeTeam = "TEAM"
)

func (a *App) rollupConfig(ctx context.Context) rollupConfig {
	cfg := rollupConfig{
		MergeSimilarity:      a.settingInt(ctx, "rollup.merge_similarity", 80),
		StallWeeks:           a.settingInt(ctx, "rollup.stall_weeks", 2),
		PersistentIssueWeeks: a.settingInt(ctx, "rollup.persistent_issue_weeks", 2),
	}
	if cfg.MergeSimilarity < 0 || cfg.MergeSimilarity > 100 {
		cfg.MergeSimilarity = 80
	}
	if cfg.StallWeeks < 2 || cfg.StallWeeks > 12 {
		cfg.StallWeeks = 2
	}
	if cfg.PersistentIssueWeeks < 2 || cfg.PersistentIssueWeeks > 12 {
		cfg.PersistentIssueWeeks = 2
	}
	return cfg
}

// resolveRollupRequest validates the query string shared by every rollup route.
func (a *App) resolveRollupRequest(w http.ResponseWriter, r *http.Request) (periodRange, string, bool) {
	kind := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind == "" {
		kind = periodMonth
	}
	period, err := resolvePeriod(kind, r.URL.Query().Get("period"), time.Now().In(a.serviceLocation(r.Context())))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PERIOD", "조회 기간이 올바르지 않습니다. 예: 2026-08, 2026-Q3, 2026-H2, 2026")
		return periodRange{}, "", false
	}
	scope := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = scopeSelf
	}
	if scope != scopeSelf && scope != scopeTeam {
		writeError(w, http.StatusBadRequest, "INVALID_SCOPE", "조회 범위는 SELF 또는 TEAM이어야 합니다.")
		return periodRange{}, "", false
	}
	if scope == scopeTeam && currentPrincipal(r.Context()).Role == "USER" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조직 단위 집계는 팀장 이상만 조회할 수 있습니다.")
		return periodRange{}, "", false
	}
	return period, scope, true
}

func (a *App) periodRollup(w http.ResponseWriter, r *http.Request) {
	period, scope, ok := a.resolveRollupRequest(w, r)
	if !ok {
		return
	}
	view, err := a.loadRollup(r.Context(), currentPrincipal(r.Context()), period, scope)
	if err != nil {
		a.logger.Error("period rollup", "error", err, "kind", period.Kind, "period", period.Period)
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "기간 보고를 집계할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}

// loadRollup reads every weekly report overlapping the period inside the
// caller's permission scope and folds it into a single de-duplicated view.
func (a *App) loadRollup(ctx context.Context, p *principal, period periodRange, scope string) (rollupView, error) {
	weekday := a.setting(ctx, "workflow.week_start", "MONDAY")
	expected := expectedWeekStarts(period, weekday)
	maxWeeks := a.settingInt(ctx, "rollup.max_weeks", 80)
	if maxWeeks < 1 || maxWeeks > 400 {
		maxWeeks = 80
	}
	if len(expected) > maxWeeks {
		return rollupView{}, fmt.Errorf("period spans %d weeks, above the configured limit", len(expected))
	}

	// A week belongs to the period when its 7 day span overlaps the period, so
	// a week straddling a month boundary is counted in both adjacent reports.
	query := `SELECT r.id,r.user_id,u.display_name,r.week_start,r.status,r.summary,r.source_type,r.version,r.submitted_at,r.updated_at,u.username
		FROM weekly_reports r JOIN users u ON u.id=r.user_id
		WHERE r.week_start <= $1::date AND r.week_start + 6 >= $2::date`
	args := []any{period.End, period.Start}
	scopeLabel := p.DisplayName
	if scope == scopeTeam {
		switch {
		case p.Role == "ADMIN":
			scopeLabel = "전사"
		case p.OrganizationID != nil:
			args = append(args, *p.OrganizationID)
			query += fmt.Sprintf(` AND u.organization_id IN (WITH RECURSIVE orgs AS (SELECT id FROM organizations WHERE id=$%d
				UNION ALL SELECT o.id FROM organizations o JOIN orgs x ON o.parent_id=x.id) SELECT id FROM orgs)`, len(args))
			scopeLabel = a.organizationName(ctx, *p.OrganizationID)
		default:
			return buildRollup(period, scope, "소속 조직 없음", nil, nil, expected, a.rollupConfig(ctx)), nil
		}
	} else {
		args = append(args, p.ID)
		query += fmt.Sprintf(" AND r.user_id=$%d", len(args))
	}
	query += " ORDER BY r.week_start,u.display_name"

	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return rollupView{}, err
	}
	defer rows.Close()
	reports := []reportListItem{}
	reportIDs := []int64{}
	for rows.Next() {
		var item reportListItem
		var week time.Time
		if err := rows.Scan(&item.ID, &item.UserID, &item.DisplayName, &week, &item.Status, &item.Summary,
			&item.SourceType, &item.Version, &item.SubmittedAt, &item.UpdatedAt, &item.Username); err != nil {
			return rollupView{}, err
		}
		item.WeekStart = week.Format("2006-01-02")
		reports = append(reports, item)
		reportIDs = append(reportIDs, item.ID)
	}
	if err := rows.Err(); err != nil {
		return rollupView{}, err
	}

	entries := []sourceEntry{}
	if len(reportIDs) > 0 {
		itemRows, err := a.db.Query(ctx, `SELECT i.report_id,i.category,i.title,i.current_result,i.next_plan,i.issue,i.management_ask,i.progress
			FROM report_items i WHERE i.report_id = ANY($1) ORDER BY i.report_id,i.sort_order,i.id`, reportIDs)
		if err != nil {
			return rollupView{}, err
		}
		defer itemRows.Close()
		byReport := make(map[int64]reportListItem, len(reports))
		for _, report := range reports {
			byReport[report.ID] = report
		}
		for itemRows.Next() {
			var entry sourceEntry
			var id int64
			if err := itemRows.Scan(&id, &entry.Category, &entry.Title, &entry.CurrentResult, &entry.NextPlan, &entry.Issue, &entry.ManagementAsk, &entry.Progress); err != nil {
				return rollupView{}, err
			}
			report := byReport[id]
			entry.ReportID = id
			entry.UserID = report.UserID
			entry.DisplayName = report.DisplayName
			entry.WeekStart = report.WeekStart
			entry.Status = report.Status
			if strings.TrimSpace(entry.Title) == "" {
				continue
			}
			entries = append(entries, entry)
		}
		if err := itemRows.Err(); err != nil {
			return rollupView{}, err
		}
	}

	view := buildRollup(period, scope, scopeLabel, entries, reports, expected, a.rollupConfig(ctx))
	view.GeneratedAt = time.Now().In(a.serviceLocation(ctx))
	return view, nil
}

func (a *App) organizationName(ctx context.Context, id int64) string {
	var name string
	if err := a.db.QueryRow(ctx, `SELECT name FROM organizations WHERE id=$1`, id).Scan(&name); err != nil {
		return "소속 조직"
	}
	return name
}

// exportRollupCSV gives the reporting line a spreadsheet-ready copy of the
// de-duplicated item table.
func (a *App) exportRollupCSV(w http.ResponseWriter, r *http.Request) {
	period, scope, ok := a.resolveRollupRequest(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	view, err := a.loadRollup(r.Context(), p, period, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "기간 보고를 집계할 수 없습니다.")
		return
	}
	filename := fmt.Sprintf("%s_%s_기간업무보고.csv", view.Period, view.ScopeLabel)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=rollup.csv; filename*=UTF-8''"+url.PathEscape(filename))
	w.Header().Set("Cache-Control", "no-store")
	// Excel needs the BOM to detect UTF-8 in Korean locales.
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"구분", "업무", "담당", "실적", "다음 계획", "이슈", "상위 조직 요청", "진척도", "수행 주차", "시작 주차", "최종 주차", "상태"})
	for _, item := range view.Items {
		state := "진행"
		switch {
		case item.Completed:
			state = "완료"
		case item.AtRisk:
			state = "이슈 지속"
		case item.Stalled:
			state = "정체"
		case item.Progress <= 0:
			state = "미착수"
		}
		_ = writer.Write([]string{
			item.Category, item.Title, strings.Join(item.Owners, ", "),
			item.CurrentResult, item.NextPlan, item.Issue, item.ManagementAsk,
			fmt.Sprintf("%d%%", item.Progress), fmt.Sprint(item.WeekCount),
			item.FirstWeek, item.LastWeek, state,
		})
	}
	writer.Flush()
	a.audit(r, p, "rollup.export_csv", "rollup", view.Period, map[string]any{"kind": view.Kind, "scope": view.Scope, "items": len(view.Items)})
}

// exportRollupPPTX reuses the administrator supplied weekly deck so that a
// period report keeps the same corporate layout as the weekly report.
func (a *App) exportRollupPPTX(w http.ResponseWriter, r *http.Request) {
	period, scope, ok := a.resolveRollupRequest(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	view, err := a.loadRollup(r.Context(), p, period, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "기간 보고를 집계할 수 없습니다.")
		return
	}
	if len(view.Items) == 0 {
		writeError(w, http.StatusConflict, "EMPTY_ROLLUP", "내보낼 업무가 없습니다.")
		return
	}
	// A period rollup can carry a year of merged work, which the fixed four
	// slide weekly frame cannot hold. The rollup builds its own paginated deck
	// so every slide stays readable no matter how long the period is.
	result, err := buildRollupDeck(view)
	if err != nil {
		a.logger.Error("render rollup PPTX", "error", err, "kind", view.Kind, "period", view.Period)
		writeError(w, http.StatusInternalServerError, "PPTX_RENDER_ERROR", "PPTX를 생성할 수 없습니다.")
		return
	}
	filename := fmt.Sprintf("%s_%s_기간업무보고.pptx", view.Period, view.ScopeLabel)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	w.Header().Set("Content-Disposition", "attachment; filename=rollup-report.pptx; filename*=UTF-8''"+url.PathEscape(filename))
	w.Header().Set("Content-Length", fmt.Sprint(len(result)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(result)
	a.audit(r, p, "rollup.export_pptx", "rollup", view.Period, map[string]any{"kind": view.Kind, "scope": view.Scope, "items": len(view.Items)})
}

func rollupItemsAsReportItems(items []rollupItem) []reportItem {
	result := make([]reportItem, 0, len(items))
	for index, item := range items {
		result = append(result, reportItem{
			Category: item.Category, Title: item.Title, CurrentResult: item.CurrentResult,
			NextPlan: item.NextPlan, Issue: item.Issue, Progress: item.Progress, SortOrder: index,
		})
	}
	return result
}
