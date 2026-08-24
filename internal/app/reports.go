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
	ID         int64  `json:"id,omitempty"`
	WorkItemID *int64 `json:"workItemId,omitempty"`
	// CandidateID is the Confluence draft this line was written from, sent by
	// the editor and previously ignored here. It is not stored on the item —
	// it is the handle used to copy that draft's pages into the lineage model,
	// which is where the evidence has to live once the candidate is accepted.
	CandidateID   *int64 `json:"candidateId,omitempty"`
	Category      string `json:"category"`
	Title         string `json:"title"`
	CurrentResult string `json:"currentResult"`
	NextPlan      string `json:"nextPlan"`
	Issue         string `json:"issue"`
	// ManagementAsk is what the reporting line must decide or supply. It is
	// deliberately separate from Issue: an issue states what is wrong, an ask
	// states what is needed from above.
	ManagementAsk string `json:"managementAsk"`
	// IssueOutcome answers what happened to last week's issue when this week's
	// field is empty. Only a person knows whether a blank means the obstacle
	// cleared or that they stopped writing it down, so the editor asks once and
	// sends the answer with the save. Empty for every other line.
	IssueOutcome string `json:"issueOutcome,omitempty"`
	Progress     int    `json:"progress"`
	SortOrder    int    `json:"sortOrder"`
	// Sources is where this line came from, when the system knows. Empty means
	// nobody recorded an origin — typed by hand, or written before the lineage
	// model existed — and never that the line is unsupported.
	Sources []itemSource `json:"sources,omitempty"`
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
	sources, err := a.sourcesForReport(r.Context(), id)
	if err != nil {
		a.logger.Error("load report sources", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "보고서를 조회할 수 없습니다.")
		return
	}
	for index := range report.Items {
		report.Items[index].Sources = sources[report.Items[index].ID]
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
	rows, err := a.db.Query(ctx, `SELECT id,work_item_id,category,title,current_result,next_plan,issue,management_ask,progress,sort_order FROM report_items WHERE report_id=$1 ORDER BY sort_order,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item reportItem
		if err := rows.Scan(&item.ID, &item.WorkItemID, &item.Category, &item.Title, &item.CurrentResult, &item.NextPlan, &item.Issue, &item.ManagementAsk, &item.Progress, &item.SortOrder); err != nil {
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

// weekIsFree reports whether the author has no report already covering these
// days, writing the response itself when they do.
//
// The unique key is (user, week_start), which only catches an exact repeat.
// After the week start weekday is changed the same days are addressed by a
// different date, so an author whose current report has "disappeared" could
// otherwise file a second one covering the same work. Both the blank report and
// the clone go through here, because the clone takes a target week too and a
// rule enforced on one path is not a rule.
func (a *App) weekIsFree(w http.ResponseWriter, r *http.Request, userID int64, week time.Time) bool {
	var existingID int64
	var existingWeek time.Time
	err := a.db.QueryRow(r.Context(), `SELECT id, week_start FROM weekly_reports
		WHERE user_id=$1 AND week_start <= $2 AND week_start + 6 >= $3
		ORDER BY week_start LIMIT 1`,
		userID, week.AddDate(0, 0, 6).Format("2006-01-02"), week.Format("2006-01-02")).
		Scan(&existingID, &existingWeek)
	if err == nil {
		writeError(w, http.StatusConflict, "REPORT_PERIOD_OVERLAPS",
			existingWeek.Format("2006-01-02")+" 주차 보고서가 같은 기간을 이미 담고 있습니다. 그 보고서를 여십시오.")
		return false
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		a.logger.Error("check overlapping report", "error", err, "userId", userID)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "보고서를 만들 수 없습니다.")
		return false
	}
	return true
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
	if !a.weekIsFree(w, r, p.ID, week) {
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
	err = tx.QueryRow(r.Context(), `INSERT INTO weekly_reports(user_id,week_start,summary,source_type) VALUES($1,$2,$3,$4) RETURNING id`, p.ID, week, trimRunes(input.Summary, 10000), sourceType).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "weekly_reports_user_id_week_start_key") {
			writeError(w, http.StatusConflict, "REPORT_EXISTS", "해당 주차 보고서가 이미 있습니다.")
		} else {
			a.logger.Error("create report", "error", err, "userId", p.ID, "trace", traceIDFromContext(r.Context()))
			writeError(w, 500, "DATABASE_ERROR", "보고서를 만들 수 없습니다.")
		}
		return
	}
	for index, item := range input.Items {
		workItemID, resolveErr := resolveWorkItem(r.Context(), tx, p.ID, item.Title, item.Category)
		if resolveErr != nil {
			a.logger.Error("resolve work item", "error", resolveErr, "reportId", id, "trace", traceIDFromContext(r.Context()))
			writeError(w, 500, "DATABASE_ERROR", "업무 식별자를 만들 수 없습니다.")
			return
		}
		var itemID int64
		if err = tx.QueryRow(r.Context(), `INSERT INTO report_items(report_id,work_item_id,category,title,current_result,next_plan,issue,management_ask,progress,sort_order)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
			id, workItemID, item.Category, item.Title, item.CurrentResult, item.NextPlan, item.Issue, item.ManagementAsk, item.Progress, index).Scan(&itemID); err != nil {
			a.logger.Error("insert report item", "error", err, "reportId", id, "index", index, "trace", traceIDFromContext(r.Context()))
			writeError(w, 500, "DATABASE_ERROR", "보고서 항목을 저장할 수 없습니다.")
			return
		}
		sources, sourceErr := a.sourcesForSavedItem(r.Context(), tx, item, p.ID)
		if sourceErr == nil {
			sourceErr = recordItemSources(r.Context(), tx, itemID, sources)
		}
		if sourceErr != nil {
			a.logger.Error("record item sources", "error", sourceErr, "reportId", id, "trace", traceIDFromContext(r.Context()))
			writeError(w, 500, "DATABASE_ERROR", "보고서 항목의 근거를 저장할 수 없습니다.")
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
	// The branches are decided here rather than in SQL. Passing the same status
	// parameter to both an assignment and a comparison made PostgreSQL deduce
	// two different types for it and reject the whole statement.
	relabelAsAI := strings.EqualFold(strings.TrimSpace(input.SourceType), "AI_TEXT")
	clearReview := newStatus == "DRAFT"
	_, err = tx.Exec(r.Context(), `UPDATE weekly_reports SET summary=$1,
		source_type=CASE WHEN $2 THEN 'AI_TEXT' ELSE source_type END,
		status=$3,
		submitted_at=CASE WHEN $4 THEN NULL ELSE submitted_at END,
		reviewed_at=CASE WHEN $4 THEN NULL ELSE reviewed_at END,
		reviewed_by=CASE WHEN $4 THEN NULL ELSE reviewed_by END,
		version=version+1,updated_at=now() WHERE id=$5`,
		trimRunes(input.Summary, 10000), relabelAsAI, newStatus, clearReview, id)
	if err != nil {
		a.logger.Error("update report", "error", err, "reportId", id, "trace", traceIDFromContext(r.Context()))
		writeError(w, 500, "DATABASE_ERROR", "보고서를 저장할 수 없습니다.")
		return
	}
	// Reconcile rather than delete and re-insert. Re-inserting would issue new
	// row ids on every save, which discards the work item link and any future
	// reference to a specific item.
	if err = a.persistReportItems(r.Context(), tx, id, ownerID, input.Items); err != nil {
		a.logger.Error("persist report items", "error", err, "reportId", id, "trace", traceIDFromContext(r.Context()))
		writeError(w, 500, "DATABASE_ERROR", "보고서 항목을 저장할 수 없습니다.")
		return
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
	// The source is not excluded: a clone whose target week overlaps the report
	// it is copied from is the duplicate this exists to prevent.
	if !a.weekIsFree(w, r, p.ID, targetWeek) {
		return
	}
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
	// The editor has always refused to save a report with no work items, but the
	// rule lived only in the browser: POST /api/v1/reports with an empty body
	// created one, and it could then be submitted, reviewed and approved like
	// any other. An empty submission is the one that tells a leader the week is
	// done. A draft may be empty — that is what a draft is — so the guard sits
	// at submission, where the claim is made.
	var itemCount int
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM report_items WHERE report_id=$1`, id).Scan(&itemCount); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "보고서를 제출할 수 없습니다.")
		return
	}
	if itemCount == 0 {
		writeError(w, 409, "EMPTY_REPORT", "업무 항목이 없는 보고서는 제출할 수 없습니다. 항목을 하나 이상 추가한 뒤 제출하세요.")
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
		_, err = tx.Exec(r.Context(), `INSERT INTO report_comments(report_id,user_id,content) VALUES($1,$2,$3)`, id, p.ID, trimRunes(strings.TrimSpace(input.Comment), 5000))
		if err != nil {
			writeError(w, 500, "DATABASE_ERROR", "검토 의견을 저장할 수 없습니다.")
			return
		}
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO report_status_history(report_id,actor_id,from_status,to_status,comment) VALUES($1,$2,'SUBMITTED',$3,$4)`, id, p.ID, target, trimRunes(input.Comment, 5000))
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

// clampQueryInt reads a whole-number query parameter, falling back when it is
// absent or unreadable and holding the result inside [low, high]. An unreadable
// value is not an error: a caller sending limit=abc gets the default page,
// which is more useful than a 400 telling them what they already typed.
func clampQueryInt(r *http.Request, name string, fallback, low, high int) int {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return min(max(parsed, low), high)
}

// How many reports one request returns, and the most it will return however
// large the limit asked for. The list used to end at a bare LIMIT 500 with no
// total beside it: measured on 120 people over 26 weeks, 팀 주간보고 held 3,120
// reports, returned 500 of them, and the screen showed five weeks as though
// that were the whole record.
const (
	reportPageDefault = 200
	reportPageMaximum = 500
)

// reportListView carries what was returned and what exists, so a screen can
// never present a page as the whole set.
type reportListView struct {
	Items  []reportListItem `json:"items"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

func (a *App) queryReports(w http.ResponseWriter, r *http.Request, teamOnly bool) {
	p := currentPrincipal(r.Context())
	week := strings.TrimSpace(r.URL.Query().Get("weekStart"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := clampQueryInt(r, "limit", reportPageDefault, 1, reportPageMaximum)
	offset := clampQueryInt(r, "offset", 0, 0, 1_000_000)

	where := ""
	args := []any{}
	if !teamOnly {
		args = append(args, p.ID)
		where += fmt.Sprintf(" AND r.user_id=$%d", len(args))
	} else if p.Role != "ADMIN" {
		if p.OrganizationID == nil {
			writeData(w, 200, reportListView{Items: []reportListItem{}, Limit: limit, Offset: offset})
			return
		}
		args = append(args, *p.OrganizationID)
		where += fmt.Sprintf(" AND u.organization_id IN (WITH RECURSIVE orgs AS (SELECT id FROM organizations WHERE id=$%d UNION ALL SELECT o.id FROM organizations o JOIN orgs x ON o.parent_id=x.id) SELECT id FROM orgs)", len(args))
	}
	if week != "" {
		args = append(args, week)
		where += fmt.Sprintf(" AND r.week_start=$%d", len(args))
	}
	if status != "" {
		args = append(args, status)
		where += fmt.Sprintf(" AND r.status=$%d", len(args))
	}

	total := 0
	countQuery := `SELECT count(*) FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE 1=1` + where
	if err := a.db.QueryRow(r.Context(), countQuery, args...).Scan(&total); err != nil {
		a.logger.Error("count reports", "error", err)
		writeError(w, 500, "QUERY_FAILED", "보고서 목록을 조회할 수 없습니다.")
		return
	}

	query := `SELECT r.id,r.user_id,u.username,u.display_name,r.week_start,r.status,r.source_type,r.summary,r.version,r.submitted_at,r.updated_at
		FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE 1=1` + where
	args = append(args, limit, offset)
	query += fmt.Sprintf(" ORDER BY r.week_start DESC,u.display_name,r.id LIMIT $%d OFFSET $%d", len(args)-1, len(args))
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
	if total > len(result)+offset {
		// A standing condition, not an event: this deployment has more reports
		// than one page holds and will say so on every request. The response
		// carries total and limit for the caller either way.
		a.conditions.once("report-list-truncated", "report list truncated",
			"total", total, "returned", len(result), "limit", limit, "offset", offset)
	}
	writeData(w, 200, reportListView{Items: result, Total: total, Limit: limit, Offset: offset})
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

// Every limit here is counted in characters so that it matches the varchar
// column widths, which PostgreSQL also counts in characters. Counting bytes
// rejected valid Korean input and, for fields with no check at all, let
// oversized values reach the database and fail the request with a 500.
func validateItems(items []reportItem) error {
	if len(items) > 100 {
		return errors.New("업무 항목은 최대 100개까지 입력할 수 있습니다.")
	}
	for _, item := range items {
		if strings.TrimSpace(item.Title) == "" || runeLength(item.Title) > 240 {
			return errors.New("각 업무 항목의 제목은 1~240자여야 합니다.")
		}
		if runeLength(item.Category) > 80 {
			return errors.New("업무 구분은 80자 이하로 입력하세요.")
		}
		if item.Progress < 0 || item.Progress > 100 {
			return errors.New("진척도는 0~100 사이여야 합니다.")
		}
		if runeLength(item.CurrentResult) > 20000 || runeLength(item.NextPlan) > 20000 || runeLength(item.Issue) > 20000 {
			return errors.New("금주 실적, 차주 계획, 이슈는 각각 20000자 이하로 입력하세요.")
		}
		if runeLength(item.ManagementAsk) > 5000 {
			return errors.New("상위 조직 요청은 5000자 이하로 입력하세요.")
		}
	}
	return nil
}
