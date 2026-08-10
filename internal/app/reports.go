package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type reportItem struct {
	ID            int64  `json:"id,omitempty"`
	Category      string `json:"category"`
	Title         string `json:"title"`
	CurrentResult string `json:"currentResult"`
	NextPlan      string `json:"nextPlan"`
	Issue         string `json:"issue"`
	Progress      int    `json:"progress"`
	SortOrder     int    `json:"sortOrder"`
}

type reportComment struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"userId"`
	DisplayName string    `json:"displayName"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"createdAt"`
}

type reportView struct {
	ID          int64           `json:"id"`
	UserID      int64           `json:"userId"`
	Username    string          `json:"username"`
	DisplayName string          `json:"displayName"`
	WeekStart   string          `json:"weekStart"`
	Status      string          `json:"status"`
	SourceType  string          `json:"sourceType"`
	Summary     string          `json:"summary"`
	Version     int             `json:"version"`
	SubmittedAt *time.Time      `json:"submittedAt"`
	ReviewedAt  *time.Time      `json:"reviewedAt"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	Items       []reportItem    `json:"items"`
	Comments    []reportComment `json:"comments"`
}

type reportListItem struct {
	ID          int64      `json:"id"`
	UserID      int64      `json:"userId"`
	Username    string     `json:"username"`
	DisplayName string     `json:"displayName"`
	WeekStart   string     `json:"weekStart"`
	Status      string     `json:"status"`
	SourceType  string     `json:"sourceType"`
	Summary     string     `json:"summary"`
	Version     int        `json:"version"`
	SubmittedAt *time.Time `json:"submittedAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func (a *App) currentReport(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	week := currentWeekStart(time.Now().In(a.serviceLocation(r.Context())), a.setting(r.Context(), "workflow.week_start", "MONDAY"))
	var id int64
	err := a.db.QueryRow(r.Context(), `SELECT id FROM weekly_reports WHERE user_id=$1 AND week_start=$2`, p.ID, week).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeData(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "현재 보고서를 조회할 수 없습니다.")
		return
	}
	a.writeReport(w, r, id)
}

func (a *App) getReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !a.canViewReport(r.Context(), currentPrincipal(r.Context()), id) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "이 보고서를 조회할 권한이 없습니다.")
		return
	}
	a.writeReport(w, r, id)
}

func (a *App) writeReport(w http.ResponseWriter, r *http.Request, id int64) {
	report, err := a.loadReport(r.Context(), id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "REPORT_NOT_FOUND", "보고서를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		a.logger.Error("load report", "error", err)
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "보고서를 조회할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, report)
}

func (a *App) loadReport(ctx context.Context, id int64) (*reportView, error) {
	result := &reportView{Items: []reportItem{}, Comments: []reportComment{}}
	var week time.Time
	err := a.db.QueryRow(ctx, `SELECT r.id,r.user_id,u.username,u.display_name,r.week_start,r.status,r.source_type,r.summary,r.version,r.submitted_at,r.reviewed_at,r.created_at,r.updated_at
		FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE r.id=$1`, id).
		Scan(&result.ID, &result.UserID, &result.Username, &result.DisplayName, &week, &result.Status, &result.SourceType, &result.Summary, &result.Version, &result.SubmittedAt, &result.ReviewedAt, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	result.WeekStart = week.Format("2006-01-02")
	rows, err := a.db.Query(ctx, `SELECT id,category,title,current_result,next_plan,issue,progress,sort_order FROM report_items WHERE report_id=$1 ORDER BY sort_order,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item reportItem
		if err := rows.Scan(&item.ID, &item.Category, &item.Title, &item.CurrentResult, &item.NextPlan, &item.Issue, &item.Progress, &item.SortOrder); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
	}
	commentRows, err := a.db.Query(ctx, `SELECT c.id,c.user_id,u.display_name,c.content,c.created_at FROM report_comments c JOIN users u ON u.id=c.user_id WHERE c.report_id=$1 ORDER BY c.created_at`, id)
	if err != nil {
		return nil, err
	}
	defer commentRows.Close()
	for commentRows.Next() {
		var comment reportComment
		if err := commentRows.Scan(&comment.ID, &comment.UserID, &comment.DisplayName, &comment.Content, &comment.CreatedAt); err != nil {
			return nil, err
		}
		result.Comments = append(result.Comments, comment)
	}
	return result, nil
}

func (a *App) createReport(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	var input struct {
		WeekStart  string       `json:"weekStart"`
		Summary    string       `json:"summary"`
		SourceType string       `json:"sourceType"`
		Items      []reportItem `json:"items"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	week := currentWeekStart(time.Now().In(a.serviceLocation(r.Context())), a.setting(r.Context(), "workflow.week_start", "MONDAY"))
	if input.WeekStart != "" {
		parsed, err := time.Parse("2006-01-02", input.WeekStart)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_WEEK", "주차 시작일이 올바르지 않습니다.")
			return
		}
		week = parsed
	}
	if err := validateItems(input.Items); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ITEMS", err.Error())
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 만들 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	sourceType := editableSourceType(input.SourceType)
	err = tx.QueryRow(r.Context(), `INSERT INTO weekly_reports(user_id,week_start,summary,source_type) VALUES($1,$2,$3,$4) RETURNING id`, p.ID, week, trimLength(input.Summary, 10000), sourceType).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "weekly_reports_user_id_week_start_key") {
			writeError(w, http.StatusConflict, "REPORT_EXISTS", "해당 주차 보고서가 이미 있습니다.")
		} else {
			writeError(w, 500, "DATABASE_ERROR", "보고서를 만들 수 없습니다.")
		}
		return
	}
	for index, item := range input.Items {
		if _, err = tx.Exec(r.Context(), `INSERT INTO report_items(report_id,category,title,current_result,next_plan,issue,progress,sort_order) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, item.Category, item.Title, item.CurrentResult, item.NextPlan, item.Issue, item.Progress, index); err != nil {
			writeError(w, 500, "DATABASE_ERROR", "보고서 항목을 저장할 수 없습니다.")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO report_status_history(report_id,actor_id,to_status) VALUES($1,$2,'DRAFT')`, id, p.ID); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "이력을 저장할 수 없습니다.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 만들 수 없습니다.")
		return
	}
	a.audit(r, p, "report.create", "report", strconv.FormatInt(id, 10), map[string]any{"weekStart": week.Format("2006-01-02")})
	writeData(w, http.StatusCreated, map[string]int64{"id": id})
}

func (a *App) updateReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	var input struct {
		Summary    string       `json:"summary"`
		Version    int          `json:"version"`
		SourceType string       `json:"sourceType"`
		Items      []reportItem `json:"items"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Version < 1 {
		writeError(w, 400, "VERSION_REQUIRED", "보고서 버전이 필요합니다.")
		return
	}
	if err := validateItems(input.Items); err != nil {
		writeError(w, 400, "INVALID_ITEMS", err.Error())
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 저장할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var ownerID int64
	var storedVersion int
	var previousStatus string
	err = tx.QueryRow(r.Context(), `SELECT user_id,version,status FROM weekly_reports WHERE id=$1 FOR UPDATE`, id).Scan(&ownerID, &storedVersion, &previousStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인의 보고서만 수정할 수 있습니다.")
		return
	}
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 저장할 수 없습니다.")
		return
	}
	if ownerID != p.ID {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인의 보고서만 수정할 수 있습니다.")
		return
	}
	if storedVersion != input.Version {
		writeError(w, http.StatusConflict, "VERSION_CONFLICT", "다른 변경사항이 먼저 저장되었습니다. 새로고침 후 다시 시도하세요.")
		return
	}
	newStatus := previousStatus
	if previousStatus == "SUBMITTED" || previousStatus == "APPROVED" {
		newStatus = "DRAFT"
	}
	_, err = tx.Exec(r.Context(), `UPDATE weekly_reports SET summary=$1,
		source_type=CASE WHEN upper(trim($2))='AI_TEXT' THEN 'AI_TEXT' ELSE source_type END,
		status=$3,submitted_at=CASE WHEN $3='DRAFT' THEN NULL ELSE submitted_at END,
		reviewed_at=CASE WHEN $3='DRAFT' THEN NULL ELSE reviewed_at END,
		reviewed_by=CASE WHEN $3='DRAFT' THEN NULL ELSE reviewed_by END,
		version=version+1,updated_at=now() WHERE id=$4`, trimLength(input.Summary, 10000), input.SourceType, newStatus, id)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 저장할 수 없습니다.")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM report_items WHERE report_id=$1`, id); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서 항목을 저장할 수 없습니다.")
		return
	}
	for index, item := range input.Items {
		_, err = tx.Exec(r.Context(), `INSERT INTO report_items(report_id,category,title,current_result,next_plan,issue,progress,sort_order) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, item.Category, item.Title, item.CurrentResult, item.NextPlan, item.Issue, item.Progress, index)
		if err != nil {
			writeError(w, 500, "DATABASE_ERROR", "보고서 항목을 저장할 수 없습니다.")
			return
		}
	}
	if newStatus != previousStatus {
		_, err = tx.Exec(r.Context(), `INSERT INTO report_status_history(report_id,actor_id,from_status,to_status,comment)
			VALUES($1,$2,$3,$4,'작성자가 제출·승인 후 내용을 수정하여 재검토 상태로 전환')`, id, p.ID, previousStatus, newStatus)
		if err != nil {
			writeError(w, 500, "DATABASE_ERROR", "보고서 상태 이력을 저장할 수 없습니다.")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "report.update", "report", strconv.FormatInt(id, 10), map[string]any{"version": input.Version + 1, "status": newStatus})
	writeData(w, http.StatusOK, map[string]any{"id": id, "version": input.Version + 1, "status": newStatus})
}

func (a *App) deleteReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	version, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("version")))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "VERSION_REQUIRED", "삭제할 보고서 버전이 필요합니다.")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 삭제할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var ownerID int64
	var storedVersion int
	var week time.Time
	var status string
	err = tx.QueryRow(r.Context(), `SELECT user_id,version,week_start,status FROM weekly_reports WHERE id=$1 FOR UPDATE`, id).
		Scan(&ownerID, &storedVersion, &week, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인의 보고서만 삭제할 수 있습니다.")
		return
	}
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 삭제할 수 없습니다.")
		return
	}
	if ownerID != p.ID {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인의 보고서만 삭제할 수 있습니다.")
		return
	}
	if storedVersion != version {
		writeError(w, http.StatusConflict, "VERSION_CONFLICT", "보고서가 변경되었습니다. 새로고침 후 다시 시도하세요.")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE report_candidates SET status='DETECTED',accepted_report_id=NULL,updated_at=now()
		WHERE accepted_report_id=$1 AND user_id=$2 AND status='ACCEPTED'`, id, p.ID); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서 연결 정보를 정리할 수 없습니다.")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM weekly_reports WHERE id=$1 AND user_id=$2`, id, p.ID); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 삭제할 수 없습니다.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 삭제할 수 없습니다.")
		return
	}
	a.audit(r, p, "report.delete", "report", strconv.FormatInt(id, 10), map[string]any{"weekStart": week.Format("2006-01-02"), "status": status, "version": version})
	w.WriteHeader(http.StatusNoContent)
}

type cloneReportInput struct {
	TargetWeekStart string `json:"targetWeekStart"`
	Mode            string `json:"mode"`
}

func (a *App) cloneReport(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	var input cloneReportInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Mode = strings.ToUpper(strings.TrimSpace(input.Mode))
	if input.Mode == "" {
		input.Mode = "STRUCTURE"
	}
	if input.Mode != "STRUCTURE" && input.Mode != "FULL" {
		writeError(w, http.StatusBadRequest, "INVALID_CLONE_MODE", "복제 방식이 올바르지 않습니다.")
		return
	}
	location := a.serviceLocation(r.Context())
	configuredWeekday := a.setting(r.Context(), "workflow.week_start", "MONDAY")
	targetWeek := currentWeekStart(time.Now().In(location), configuredWeekday)
	if strings.TrimSpace(input.TargetWeekStart) != "" {
		parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(input.TargetWeekStart), location)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_WEEK", "대상 주차 시작일이 올바르지 않습니다.")
			return
		}
		targetWeek = parsed
	}
	if !currentWeekStart(targetWeek, configuredWeekday).Equal(targetWeek) {
		writeError(w, http.StatusBadRequest, "INVALID_WEEKDAY", "대상 날짜는 관리자가 설정한 주차 시작 요일이어야 합니다.")
		return
	}

	p := currentPrincipal(r.Context())
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "보고서를 복제할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var summary string
	if err = tx.QueryRow(r.Context(), `SELECT summary FROM weekly_reports WHERE id=$1 AND user_id=$2 FOR SHARE`, sourceID, p.ID).Scan(&summary); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "본인의 보고서만 복제할 수 있습니다.")
		} else {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "원본 보고서를 조회할 수 없습니다.")
		}
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT category,title,current_result,next_plan,issue,progress,sort_order
		FROM report_items WHERE report_id=$1 ORDER BY sort_order,id`, sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "원본 보고서 항목을 조회할 수 없습니다.")
		return
	}
	sourceItems := []reportItem{}
	for rows.Next() {
		var item reportItem
		if err = rows.Scan(&item.Category, &item.Title, &item.CurrentResult, &item.NextPlan, &item.Issue, &item.Progress, &item.SortOrder); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "원본 보고서 항목을 조회할 수 없습니다.")
			return
		}
		sourceItems = append(sourceItems, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "원본 보고서 항목을 조회할 수 없습니다.")
		return
	}
	rows.Close()
	clonedSummary, clonedItems := prepareClonedReport(summary, sourceItems, input.Mode)
	if len(clonedItems) == 0 {
		writeError(w, http.StatusConflict, "EMPTY_SOURCE_REPORT", "복제할 업무 항목이 없습니다.")
		return
	}
	var reportID int64
	err = tx.QueryRow(r.Context(), `INSERT INTO weekly_reports(user_id,week_start,status,summary,source_type,source_ref)
		VALUES($1,$2,'DRAFT',$3,'CLONED',$4) RETURNING id`, p.ID, targetWeek, clonedSummary, "report:"+strconv.FormatInt(sourceID, 10)).Scan(&reportID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, "REPORT_EXISTS", targetWeek.Format("2006-01-02")+" 주차 보고서가 이미 있습니다.")
		} else {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "복제 보고서를 만들 수 없습니다.")
		}
		return
	}
	for index, item := range clonedItems {
		_, err = tx.Exec(r.Context(), `INSERT INTO report_items(report_id,category,title,current_result,next_plan,issue,progress,sort_order)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, reportID, item.Category, item.Title, item.CurrentResult, item.NextPlan, item.Issue, item.Progress, index)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "복제 보고서 항목을 저장할 수 없습니다.")
			return
		}
	}
	comment := "보고서 #" + strconv.FormatInt(sourceID, 10) + "에서 업무 구조 복제"
	if input.Mode == "FULL" {
		comment = "보고서 #" + strconv.FormatInt(sourceID, 10) + "에서 전체 내용 복제"
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO report_status_history(report_id,actor_id,to_status,comment) VALUES($1,$2,'DRAFT',$3)`, reportID, p.ID, comment); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "복제 보고서 이력을 저장할 수 없습니다.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "보고서를 복제할 수 없습니다.")
		return
	}
	a.audit(r, p, "report.clone", "report", strconv.FormatInt(reportID, 10), map[string]any{"sourceReportId": sourceID, "targetWeekStart": targetWeek.Format("2006-01-02"), "mode": input.Mode})
	writeData(w, http.StatusCreated, map[string]any{"id": reportID, "sourceReportId": sourceID, "weekStart": targetWeek.Format("2006-01-02"), "status": "DRAFT", "mode": input.Mode})
}

func prepareClonedReport(summary string, items []reportItem, mode string) (string, []reportItem) {
	result := make([]reportItem, 0, len(items))
	for index, item := range items {
		cloned := reportItem{Category: item.Category, Title: item.Title, SortOrder: index}
		if mode == "FULL" {
			cloned.CurrentResult = item.CurrentResult
			cloned.NextPlan = item.NextPlan
			cloned.Issue = item.Issue
			cloned.Progress = item.Progress
		}
		result = append(result, cloned)
	}
	if mode == "FULL" {
		return summary, result
	}
	return "", result
}

func (a *App) submitReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	target := "CLOSED"
	if a.settingBool(r.Context(), "workflow.enabled", false) {
		target = "SUBMITTED"
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 제출할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var previous string
	if err := tx.QueryRow(r.Context(), `SELECT status FROM weekly_reports WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, p.ID).Scan(&previous); err != nil || (previous != "DRAFT" && previous != "REVISION_REQUESTED") {
		writeError(w, 409, "INVALID_STATUS", "현재 상태에서는 제출할 수 없습니다.")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE weekly_reports SET status=$1,submitted_at=now(),reviewed_at=NULL,reviewed_by=NULL,updated_at=now(),version=version+1 WHERE id=$2`, target, id); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO report_status_history(report_id,actor_id,from_status,to_status) VALUES($1,$2,$3,$4)`, id, p.ID, previous, target)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 제출할 수 없습니다.")
		return
	}
	a.audit(r, p, "report.submit", "report", strconv.FormatInt(id, 10), map[string]any{"status": target})
	writeData(w, http.StatusOK, map[string]string{"status": target})
}

func (a *App) approveReport(w http.ResponseWriter, r *http.Request) {
	a.reviewReport(w, r, "APPROVED")
}

func (a *App) rejectReport(w http.ResponseWriter, r *http.Request) {
	a.reviewReport(w, r, "REVISION_REQUESTED")
}

func (a *App) reviewReport(w http.ResponseWriter, r *http.Request, target string) {
	if !a.settingBool(r.Context(), "workflow.enabled", false) {
		writeError(w, 409, "WORKFLOW_DISABLED", "검토 워크플로가 비활성화되어 있습니다.")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	if !a.canReviewReport(r.Context(), p, id) {
		writeError(w, 403, "FORBIDDEN", "이 보고서를 검토할 권한이 없습니다.")
		return
	}
	var input struct {
		Comment string `json:"comment"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if target == "REVISION_REQUESTED" && strings.TrimSpace(input.Comment) == "" {
		writeError(w, 400, "COMMENT_REQUIRED", "반려 사유를 입력하세요.")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "검토 결과를 저장할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	command, err := tx.Exec(r.Context(), `UPDATE weekly_reports SET status=$1,reviewed_at=now(),reviewed_by=$2,updated_at=now(),version=version+1 WHERE id=$3 AND status='SUBMITTED'`, target, p.ID, id)
	if err != nil || command.RowsAffected() == 0 {
		writeError(w, 409, "INVALID_STATUS", "현재 상태에서는 검토할 수 없습니다.")
		return
	}
	if strings.TrimSpace(input.Comment) != "" {
		_, err = tx.Exec(r.Context(), `INSERT INTO report_comments(report_id,user_id,content) VALUES($1,$2,$3)`, id, p.ID, trimLength(strings.TrimSpace(input.Comment), 5000))
		if err != nil {
			writeError(w, 500, "DATABASE_ERROR", "검토 의견을 저장할 수 없습니다.")
			return
		}
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO report_status_history(report_id,actor_id,from_status,to_status,comment) VALUES($1,$2,'SUBMITTED',$3,$4)`, id, p.ID, target, trimLength(input.Comment, 5000))
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 500, "DATABASE_ERROR", "검토 결과를 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "report.review", "report", strconv.FormatInt(id, 10), map[string]any{"status": target})
	writeData(w, 200, map[string]string{"status": target})
}

func (a *App) addComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	if !a.canViewReport(r.Context(), p, id) {
		writeError(w, 403, "FORBIDDEN", "이 보고서에 의견을 남길 권한이 없습니다.")
		return
	}
	var input struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || len(input.Content) > 5000 {
		writeError(w, 400, "INVALID_COMMENT", "의견은 1~5000자로 입력하세요.")
		return
	}
	var commentID int64
	if err := a.db.QueryRow(r.Context(), `INSERT INTO report_comments(report_id,user_id,content) VALUES($1,$2,$3) RETURNING id`, id, p.ID, input.Content).Scan(&commentID); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "의견을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "report.comment", "report", strconv.FormatInt(id, 10), nil)
	writeData(w, 201, map[string]int64{"id": commentID})
}

func (a *App) listReports(w http.ResponseWriter, r *http.Request) { a.queryReports(w, r, false) }
func (a *App) teamReports(w http.ResponseWriter, r *http.Request) { a.queryReports(w, r, true) }

func (a *App) queryReports(w http.ResponseWriter, r *http.Request, teamOnly bool) {
	p := currentPrincipal(r.Context())
	week := strings.TrimSpace(r.URL.Query().Get("weekStart"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	query := `SELECT r.id,r.user_id,u.username,u.display_name,r.week_start,r.status,r.source_type,r.summary,r.version,r.submitted_at,r.updated_at
		FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE 1=1`
	args := []any{}
	if !teamOnly {
		args = append(args, p.ID)
		query += fmt.Sprintf(" AND r.user_id=$%d", len(args))
	} else if p.Role != "ADMIN" {
		if p.OrganizationID == nil {
			writeData(w, 200, []reportListItem{})
			return
		}
		args = append(args, *p.OrganizationID)
		query += fmt.Sprintf(" AND u.organization_id IN (WITH RECURSIVE orgs AS (SELECT id FROM organizations WHERE id=$%d UNION ALL SELECT o.id FROM organizations o JOIN orgs x ON o.parent_id=x.id) SELECT id FROM orgs)", len(args))
	}
	if week != "" {
		args = append(args, week)
		query += fmt.Sprintf(" AND r.week_start=$%d", len(args))
	}
	if status != "" {
		args = append(args, status)
		query += fmt.Sprintf(" AND r.status=$%d", len(args))
	}
	query += " ORDER BY r.week_start DESC,u.display_name LIMIT 500"
	rows, err := a.db.Query(r.Context(), query, args...)
	if err != nil {
		a.logger.Error("list reports", "error", err)
		writeError(w, 500, "QUERY_FAILED", "보고서 목록을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := []reportListItem{}
	for rows.Next() {
		var item reportListItem
		var weekDate time.Time
		if err := rows.Scan(&item.ID, &item.UserID, &item.Username, &item.DisplayName, &weekDate, &item.Status, &item.SourceType, &item.Summary, &item.Version, &item.SubmittedAt, &item.UpdatedAt); err != nil {
			writeError(w, 500, "QUERY_FAILED", "보고서 목록을 조회할 수 없습니다.")
			return
		}
		item.WeekStart = weekDate.Format("2006-01-02")
		result = append(result, item)
	}
	writeData(w, 200, result)
}

func (a *App) canViewReport(ctx context.Context, p *principal, reportID int64) bool {
	if p == nil {
		return false
	}
	if p.Role == "ADMIN" {
		return true
	}
	var ownerID int64
	var orgID *int64
	if err := a.db.QueryRow(ctx, `SELECT r.user_id,u.organization_id FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE r.id=$1`, reportID).Scan(&ownerID, &orgID); err != nil {
		return false
	}
	if ownerID == p.ID {
		return true
	}
	if (p.Role == "TEAM_LEADER" || p.Role == "ORG_MANAGER") && p.OrganizationID != nil && orgID != nil {
		var allowed bool
		_ = a.db.QueryRow(ctx, `WITH RECURSIVE orgs AS (SELECT id FROM organizations WHERE id=$1 UNION ALL SELECT o.id FROM organizations o JOIN orgs x ON o.parent_id=x.id) SELECT EXISTS(SELECT 1 FROM orgs WHERE id=$2)`, *p.OrganizationID, *orgID).Scan(&allowed)
		return allowed
	}
	return false
}

func (a *App) canReviewReport(ctx context.Context, p *principal, reportID int64) bool {
	if p == nil || p.Role == "USER" {
		return false
	}
	var ownerID int64
	if err := a.db.QueryRow(ctx, `SELECT user_id FROM weekly_reports WHERE id=$1`, reportID).Scan(&ownerID); err != nil || ownerID == p.ID {
		return false
	}
	if p.Role == "ADMIN" {
		return true
	}
	return a.canViewReport(ctx, p, reportID)
}

func currentWeekStart(now time.Time, start string) time.Time {
	weekdays := map[string]time.Weekday{
		"SUNDAY": time.Sunday, "MONDAY": time.Monday, "TUESDAY": time.Tuesday,
		"WEDNESDAY": time.Wednesday, "THURSDAY": time.Thursday, "FRIDAY": time.Friday, "SATURDAY": time.Saturday,
	}
	weekday, ok := weekdays[strings.ToUpper(strings.TrimSpace(start))]
	if !ok {
		weekday = time.Monday
	}
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	offset := (7 + int(date.Weekday()) - int(weekday)) % 7
	return date.AddDate(0, 0, -offset)
}

func (a *App) serviceLocation(ctx context.Context) *time.Location {
	location, err := time.LoadLocation(a.setting(ctx, "service.timezone", "Asia/Seoul"))
	if err != nil {
		return time.UTC
	}
	return location
}

func editableSourceType(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "AI_TEXT") {
		return "AI_TEXT"
	}
	if strings.EqualFold(strings.TrimSpace(value), "CONFLUENCE_AI") {
		return "CONFLUENCE_AI"
	}
	return "MANUAL"
}

func validateItems(items []reportItem) error {
	if len(items) > 100 {
		return errors.New("업무 항목은 최대 100개까지 입력할 수 있습니다.")
	}
	for _, item := range items {
		if strings.TrimSpace(item.Title) == "" || len(item.Title) > 240 {
			return errors.New("각 업무 항목의 제목은 1~240자여야 합니다.")
		}
		if item.Progress < 0 || item.Progress > 100 {
			return errors.New("진척도는 0~100 사이여야 합니다.")
		}
		if len(item.CurrentResult)+len(item.NextPlan)+len(item.Issue) > 60000 {
			return errors.New("업무 항목 내용이 너무 깁니다.")
		}
	}
	return nil
}
