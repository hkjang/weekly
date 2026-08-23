package app

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The decision log.
//
// A weekly report says what happened. It does not say who decided what, on
// what grounds, or what was meant to follow — so the question that actually
// gets asked six months later, 왜 이렇게 하기로 했더라, is answered by finding
// somebody who still remembers. The handover screen exists for exactly the
// moment when nobody does, and until now it had nothing to show.
//
// Recording is deliberately separate from editing the work itself. Merging two
// tasks rewrites somebody's reporting history and is restricted to the owner;
// writing down a decision adds to the record and is open to anyone who can see
// the work, because the common case is a team lead writing down what a director
// said about somebody else's task.

const (
	decisionOpen       = "OPEN"
	decisionDone       = "DONE"
	decisionSuperseded = "SUPERSEDED"
	decisionTextLimit  = 5000
)

type decisionView struct {
	ID           int64  `json:"id"`
	WorkItemID   int64  `json:"workItemId"`
	Title        string `json:"title"`
	DecidedBy    string `json:"decidedBy"`
	DecidedOn    string `json:"decidedOn"`
	Rationale    string `json:"rationale"`
	FollowUp     string `json:"followUp"`
	DueDate      string `json:"dueDate,omitempty"`
	Status       string `json:"status"`
	SupersedesID *int64 `json:"supersedesId,omitempty"`
	// RecordedByName is who wrote it down, which is not always who decided.
	// Both are shown, because a log that cannot distinguish them is wrong in
	// precisely the cases that make it worth keeping.
	RecordedByName string    `json:"recordedByName"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// workItemViewer resolves a work item's owner and asks whether this principal
// may see it. Visibility follows the person, not the task: the same rule that
// decides whether their handover can be opened.
func (a *App) workItemViewer(ctx context.Context, p *principal, workItemID int64) (int64, bool, error) {
	owner, _, err := a.workItemOwner(ctx, workItemID)
	if err != nil {
		return 0, false, err
	}
	visible, err := a.canViewPerson(ctx, p, owner)
	return owner, visible, err
}

func (a *App) listWorkItemDecisions(w http.ResponseWriter, r *http.Request) {
	workItemID, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	_, visible, err := a.workItemViewer(r.Context(), p, workItemID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		a.logger.Error("list decisions", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "결정 기록을 조회할 수 없습니다.")
		return
	}
	if !visible {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조회 권한 범위 밖의 업무입니다.")
		return
	}
	decisions, err := a.decisionsFor(r.Context(), workItemID)
	if err != nil {
		a.logger.Error("list decisions", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "결정 기록을 조회할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, decisions)
}

// decisionsFor reads one task's decisions, newest decision date first. Not
// paged: a task accumulates decisions at the rate a person makes them, and a
// list that needs paging is a list nobody is keeping.
func (a *App) decisionsFor(ctx context.Context, workItemID int64) ([]decisionView, error) {
	rows, err := a.db.Query(ctx, `SELECT d.id,d.work_item_id,d.title,d.decided_by,d.decided_on,d.rationale,
			d.follow_up,d.due_date,d.status,d.supersedes_id,coalesce(u.display_name,''),d.created_at,d.updated_at
		FROM decisions d LEFT JOIN users u ON u.id=d.recorded_by
		WHERE d.work_item_id=$1 ORDER BY d.decided_on DESC, d.id DESC`, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []decisionView{}
	for rows.Next() {
		var item decisionView
		var decidedOn time.Time
		var dueDate *time.Time
		if err := rows.Scan(&item.ID, &item.WorkItemID, &item.Title, &item.DecidedBy, &decidedOn, &item.Rationale,
			&item.FollowUp, &dueDate, &item.Status, &item.SupersedesID, &item.RecordedByName,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.DecidedOn = decidedOn.Format("2006-01-02")
		if dueDate != nil {
			item.DueDate = dueDate.Format("2006-01-02")
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type decisionInput struct {
	Title        string `json:"title"`
	DecidedBy    string `json:"decidedBy"`
	DecidedOn    string `json:"decidedOn"`
	Rationale    string `json:"rationale"`
	FollowUp     string `json:"followUp"`
	DueDate      string `json:"dueDate"`
	Status       string `json:"status"`
	SupersedesID *int64 `json:"supersedesId"`
}

// validateDecision checks what a reader will need in order to trust the entry.
// The title and the decider are required because an entry without them records
// that something was decided and nothing else.
func validateDecision(input *decisionInput) (time.Time, *time.Time, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.DecidedBy = strings.TrimSpace(input.DecidedBy)
	input.Rationale = strings.TrimSpace(input.Rationale)
	input.FollowUp = strings.TrimSpace(input.FollowUp)
	if input.Title == "" || runeLength(input.Title) > 240 {
		return time.Time{}, nil, errors.New("결정 제목은 1~240자로 입력하세요.")
	}
	if input.DecidedBy == "" || runeLength(input.DecidedBy) > 120 {
		return time.Time{}, nil, errors.New("결정한 사람은 1~120자로 입력하세요.")
	}
	if runeLength(input.Rationale) > decisionTextLimit || runeLength(input.FollowUp) > decisionTextLimit {
		return time.Time{}, nil, errors.New("결정 근거와 후속 조치는 각각 5000자 이하로 입력하세요.")
	}
	decidedOn, err := time.Parse("2006-01-02", strings.TrimSpace(input.DecidedOn))
	if err != nil {
		return time.Time{}, nil, errors.New("결정 일자를 YYYY-MM-DD 형식으로 입력하세요.")
	}
	var due *time.Time
	if trimmed := strings.TrimSpace(input.DueDate); trimmed != "" {
		parsed, err := time.Parse("2006-01-02", trimmed)
		if err != nil {
			return time.Time{}, nil, errors.New("후속 조치 기한을 YYYY-MM-DD 형식으로 입력하세요.")
		}
		if parsed.Before(decidedOn) {
			return time.Time{}, nil, errors.New("후속 조치 기한은 결정 일자보다 빠를 수 없습니다.")
		}
		due = &parsed
	}
	if input.Status == "" {
		input.Status = decisionOpen
	}
	switch input.Status {
	case decisionOpen, decisionDone, decisionSuperseded:
	default:
		return time.Time{}, nil, errors.New("결정 상태는 OPEN, DONE, SUPERSEDED 중 하나여야 합니다.")
	}
	return decidedOn, due, nil
}

func (a *App) createWorkItemDecision(w http.ResponseWriter, r *http.Request) {
	workItemID, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	_, visible, err := a.workItemViewer(r.Context(), p, workItemID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		a.logger.Error("create decision", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 기록할 수 없습니다.")
		return
	}
	if !visible {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조회 권한 범위 밖의 업무입니다.")
		return
	}
	var input decisionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	decidedOn, due, err := validateDecision(&input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_DECISION", err.Error())
		return
	}
	// A superseded decision has to be one of this task's own. Pointing across
	// tasks would make the chain unreadable from either end.
	if input.SupersedesID != nil {
		var belongs bool
		if err := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM decisions WHERE id=$1 AND work_item_id=$2)`,
			*input.SupersedesID, workItemID).Scan(&belongs); err != nil || !belongs {
			writeError(w, http.StatusBadRequest, "INVALID_SUPERSEDES", "대체할 결정은 같은 업무의 결정이어야 합니다.")
			return
		}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 기록할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	if err := tx.QueryRow(r.Context(), `INSERT INTO decisions(work_item_id,recorded_by,decided_by,decided_on,title,rationale,follow_up,due_date,status,supersedes_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		workItemID, p.ID, input.DecidedBy, decidedOn, input.Title, input.Rationale, input.FollowUp, due, input.Status, input.SupersedesID).Scan(&id); err != nil {
		a.logger.Error("create decision", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 기록할 수 없습니다.")
		return
	}
	// Superseding is stated once, by the new entry, and applied to the old one
	// here. Leaving the old row OPEN would keep it in the outstanding list
	// forever; deleting it would lose the fact that it was reversed.
	if input.SupersedesID != nil {
		if _, err := tx.Exec(r.Context(), `UPDATE decisions SET status=$1,updated_at=now() WHERE id=$2 AND status<>$1`,
			decisionSuperseded, *input.SupersedesID); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 기록할 수 없습니다.")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 기록할 수 없습니다.")
		return
	}
	a.audit(r, p, "decision.create", "decision", strconv.FormatInt(id, 10),
		map[string]any{"workItemId": workItemID, "decidedBy": input.DecidedBy, "status": input.Status})
	writeData(w, http.StatusCreated, map[string]int64{"id": id})
}

// decisionWorkItem resolves which task a decision belongs to, and who recorded
// it, so both the visibility rule and the ownership rule can be applied.
func (a *App) decisionWorkItem(ctx context.Context, id int64) (int64, *int64, error) {
	var workItemID int64
	var recordedBy *int64
	err := a.db.QueryRow(ctx, `SELECT work_item_id, recorded_by FROM decisions WHERE id=$1`, id).Scan(&workItemID, &recordedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, errNotFound
	}
	return workItemID, recordedBy, err
}

func (a *App) updateDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	workItemID, _, err := a.decisionWorkItem(r.Context(), id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "DECISION_NOT_FOUND", "결정 기록을 찾을 수 없습니다.")
		return
	}
	if err == nil {
		var visible bool
		if _, visible, err = a.workItemViewer(r.Context(), p, workItemID); err == nil && !visible {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "조회 권한 범위 밖의 업무입니다.")
			return
		}
	}
	if err != nil {
		a.logger.Error("update decision", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 수정할 수 없습니다.")
		return
	}
	var input decisionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	decidedOn, due, err := validateDecision(&input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_DECISION", err.Error())
		return
	}
	if _, err := a.db.Exec(r.Context(), `UPDATE decisions SET title=$1,decided_by=$2,decided_on=$3,rationale=$4,
			follow_up=$5,due_date=$6,status=$7,updated_at=now() WHERE id=$8`,
		input.Title, input.DecidedBy, decidedOn, input.Rationale, input.FollowUp, due, input.Status, id); err != nil {
		a.logger.Error("update decision", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 수정할 수 없습니다.")
		return
	}
	a.audit(r, p, "decision.update", "decision", strconv.FormatInt(id, 10), map[string]any{"status": input.Status})
	writeData(w, http.StatusOK, map[string]bool{"updated": true})
}

// deleteDecision is for entries written by mistake, not for entries somebody
// would rather not have. Recording is open to anyone who can see the work;
// removing is not, because a log others can quietly edit is not a record.
func (a *App) deleteDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	_, recordedBy, err := a.decisionWorkItem(r.Context(), id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "DECISION_NOT_FOUND", "결정 기록을 찾을 수 없습니다.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 삭제할 수 없습니다.")
		return
	}
	if p.Role != "ADMIN" && (recordedBy == nil || *recordedBy != p.ID) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "기록한 사람과 관리자만 결정 기록을 삭제할 수 있습니다.")
		return
	}
	if _, err := a.db.Exec(r.Context(), `DELETE FROM decisions WHERE id=$1`, id); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "결정을 삭제할 수 없습니다.")
		return
	}
	a.audit(r, p, "decision.delete", "decision", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// decisionsForWorkItems reads several tasks' decisions in one query.
//
// The handover needs them per task, and asking per task would be a query for
// every item on the screen — the shape v0.25 spent a release removing from the
// insight endpoints. Grouped here instead, and returned keyed by task so the
// caller does not have to re-sort.
func (a *App) decisionsForWorkItems(ctx context.Context, ids []int64) (map[int64][]decisionView, error) {
	grouped := map[int64][]decisionView{}
	if len(ids) == 0 {
		return grouped, nil
	}
	rows, err := a.db.Query(ctx, `SELECT d.id,d.work_item_id,d.title,d.decided_by,d.decided_on,d.rationale,
			d.follow_up,d.due_date,d.status,d.supersedes_id,coalesce(u.display_name,''),d.created_at,d.updated_at
		FROM decisions d LEFT JOIN users u ON u.id=d.recorded_by
		WHERE d.work_item_id = ANY($1) ORDER BY d.work_item_id, d.decided_on DESC, d.id DESC`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item decisionView
		var decidedOn time.Time
		var dueDate *time.Time
		if err := rows.Scan(&item.ID, &item.WorkItemID, &item.Title, &item.DecidedBy, &decidedOn, &item.Rationale,
			&item.FollowUp, &dueDate, &item.Status, &item.SupersedesID, &item.RecordedByName,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.DecidedOn = decidedOn.Format("2006-01-02")
		if dueDate != nil {
			item.DueDate = dueDate.Format("2006-01-02")
		}
		grouped[item.WorkItemID] = append(grouped[item.WorkItemID], item)
	}
	return grouped, rows.Err()
}

// overdueDecision returns the first outstanding decision whose follow-up date
// has passed, which is the thing a new owner most needs told: somebody agreed
// to do something by a date, and the date is gone.
func overdueDecision(decisions []decisionView, today string) *decisionView {
	for index := range decisions {
		entry := &decisions[index]
		if entry.Status == decisionOpen && entry.DueDate != "" && entry.DueDate < today {
			return entry
		}
	}
	return nil
}
