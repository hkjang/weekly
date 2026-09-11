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
)

// 월간 업무 상황판: a department's month on one screen, with a checkbox on
// every line.
//
// The product could already say what a person reported last week and how a task
// has aged. It could not say what this department is doing on the 14th. A team
// that runs on a schedule — an audit, an inspection, a release window, a
// training day — plans the month first and reports on it afterwards, and until
// now that plan lived in somebody's spreadsheet next to the product that was
// supposed to hold it.
//
// Three things follow from being a board rather than a list:
//
//   - The checkbox is the whole interaction. Ticking one is a single request
//     that changes one row, because it is done standing in front of a screen at
//     a morning meeting, not sitting in an editor.
//   - Everyone in the department sees it. A shared plan that each person sees
//     only their own slice of is not a status board; see scheduleVisibility for
//     what that does and does not open up.
//   - What can be edited is decided here, not on the screen. Every row carries
//     canEdit so the board never draws a checkbox that the server will refuse.

const (
	scheduleTitleLimit = 200
	scheduleNoteLimit  = 2000
	// scheduleSpanDays bounds one read. A month is the normal ask and a year is
	// a legitimate one (연간 계획), but nothing beyond that is a board — it is a
	// data export wearing a calendar's clothes.
	scheduleSpanDays = 366
)

// The four levels a department actually uses, and the colours they are drawn
// in: 긴급 빨강, 중요 초록, 필요 파랑, 일반 검정.
//
// Four rather than a number, because a board is read at a glance and a 1-to-5
// scale is read by nobody. The colour lives on the screen and the word lives
// here: a value that arrives as anything else becomes NORMAL rather than an
// error, so an older client or a copied API call cannot put a row on the board
// in a colour nothing draws.
const (
	priorityUrgent    = "URGENT"
	priorityImportant = "IMPORTANT"
	priorityNeeded    = "NEEDED"
	priorityNormal    = "NORMAL"
)

func normalizePriority(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case priorityUrgent:
		return priorityUrgent
	case priorityImportant:
		return priorityImportant
	case priorityNeeded:
		return priorityNeeded
	}
	return priorityNormal
}

// priorityRank orders a day's lines: 긴급 first, 일반 last. Written as SQL
// because the ordering has to survive paging and grouping in the query rather
// than being re-sorted per view.
const priorityRank = `CASE s.priority WHEN 'URGENT' THEN 0 WHEN 'IMPORTANT' THEN 1 WHEN 'NEEDED' THEN 2 ELSE 3 END`

type scheduleTaskView struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	// StartDate and EndDate are the same day for most rows. They are always
	// both sent so the grid never has to guess a span.
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`

	Done       bool       `json:"done"`
	DoneAt     *time.Time `json:"doneAt,omitempty"`
	DoneByName string     `json:"doneByName,omitempty"`

	UserID           int64  `json:"userId"`
	DisplayName      string `json:"displayName"`
	OrganizationName string `json:"organizationName,omitempty"`
	CreatedByID      int64  `json:"createdById"`
	CreatedByName    string `json:"createdByName,omitempty"`

	WorkItemID *int64 `json:"workItemId,omitempty"`
	Note       string `json:"note,omitempty"`

	// Priority is URGENT / IMPORTANT / NEEDED / NORMAL.
	Priority string `json:"priority"`
	// SRID is the ITSM service request this line came from, and SRURL is where
	// clicking it should go. The URL is resolved on every read from the current
	// link template rather than stored, so moving the ITSM portal moves every
	// existing link with it.
	SRID  string `json:"srId,omitempty"`
	SRURL string `json:"srUrl,omitempty"`

	// CanEdit says whether this reader may tick, edit or remove this row. A
	// board full of checkboxes that answer 403 is worse than one with none.
	CanEdit bool `json:"canEdit"`
}

// scheduleSummary is the strip above the grid: what a person standing in front
// of the board needs to read from three metres away.
type scheduleSummary struct {
	Total int `json:"total"`
	Done  int `json:"done"`
	// Overdue is open work whose last day has passed, Today is open work that
	// covers today. Both are measured against today in the service timezone,
	// not the reader's browser: a board hanging in an office in one country
	// must not change colour because somebody opened it from another.
	Overdue int `json:"overdue"`
	Today   int `json:"today"`
	People  int `json:"people"`
	// Urgent is open 긴급 work. The red count is the one a department head
	// looks for first, and counting it here means the board and the summary
	// cannot disagree about what is still red.
	Urgent int `json:"urgent"`
}

type scheduleResponse struct {
	From    string             `json:"from"`
	To      string             `json:"to"`
	Scope   string             `json:"scope"`
	Today   string             `json:"today"`
	Tasks   []scheduleTaskView `json:"tasks"`
	Summary scheduleSummary    `json:"summary"`
}

// scheduleVisibility is who sees which rows.
//
// Deliberately wider than workScope, which the tracking screens use. That one
// answers "whose reporting history may I read", and a member may only read
// their own; this one answers "what is my department doing this month", and a
// plan only works as a plan when the people in it can see it. So a member reads
// their own organisation's board — and nothing above or beside it, because the
// subtree starts at their own organisation.
//
// Writing stays where it was: seeing the board does not mean ticking somebody
// else's line. canManageScheduleTask decides that separately.
func scheduleVisibility(p *principal, team bool, start int) (string, []any) {
	own := fmt.Sprintf(" AND s.user_id=$%d", start)
	if !team {
		return own, []any{p.ID}
	}
	if p.Role == "ADMIN" {
		return "", nil
	}
	if p.OrganizationID == nil {
		// Nobody has put this account in an organisation, so there is no
		// department to show. Their own rows are the honest answer.
		return own, []any{p.ID}
	}
	return fmt.Sprintf(" AND (s.user_id=$%d OR u.organization_id IN ", start) +
		orgSubtree(start+1) + ")", []any{p.ID, *p.OrganizationID}
}

// manageableOwners is the set of assignees this reader may write for.
//
// One query rather than one per row: a month of a 300 person department is a
// few hundred rows, and asking the database whether the reader leads each one
// of them would be a few hundred round trips to draw one screen.
func (a *App) manageableOwners(ctx context.Context, p *principal) (map[int64]bool, bool, error) {
	if p.Role == "ADMIN" {
		return nil, true, nil
	}
	owners := map[int64]bool{p.ID: true}
	if (p.Role != "TEAM_LEADER" && p.Role != "ORG_MANAGER") || p.OrganizationID == nil {
		return owners, false, nil
	}
	rows, err := a.db.Query(ctx,
		`SELECT id FROM users WHERE organization_id IN `+orgSubtree(1), *p.OrganizationID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		owners[id] = true
	}
	return owners, false, rows.Err()
}

// scheduleTasksBetween is the board's rows, without the screen around them.
//
// Extracted because the board is no longer the only reader: the MCP surface
// answers "무슨 일정이 있나" from the same table, and a second SELECT written
// beside this one is a second set of visibility rules to keep in step. The
// screen's own additions — who may tick a line, and the counts along the top —
// stay with the screen, because a read-only key has no use for the first.
func (a *App) scheduleTasksBetween(ctx context.Context, p *principal, from, to time.Time, team bool) ([]scheduleTaskView, error) {
	args := []any{from, to}
	predicate, scopeArgs := scheduleVisibility(p, team, len(args)+1)
	args = append(args, scopeArgs...)
	// Overlap, not containment: a task that started in August and ends on the
	// 3rd of September belongs on September's board. Asking for rows whose
	// start_date falls inside the month would drop exactly the long-running
	// work a board exists to show.
	query := `
		SELECT s.id, s.title, s.category, s.start_date, s.end_date, s.done_at,
		       coalesce(doner.display_name, ''), s.user_id, coalesce(u.display_name, ''),
		       coalesce(org.name, ''), s.created_by, coalesce(author.display_name, ''),
		       s.work_item_id, s.note, s.priority, s.sr_id
		FROM schedule_tasks s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN organizations org ON org.id = u.organization_id
		LEFT JOIN users doner ON doner.id = s.done_by
		LEFT JOIN users author ON author.id = s.created_by
		WHERE s.start_date <= $2 AND s.end_date >= $1` + predicate + `
		ORDER BY s.start_date, ` + priorityRank + `, s.end_date, u.display_name, s.id`
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// One read of the link template for the whole board rather than one per
	// row: every SR line resolves against the same setting.
	itsm := a.loadITSMSettings(ctx)

	tasks := []scheduleTaskView{}
	for rows.Next() {
		var task scheduleTaskView
		var start, end time.Time
		if err := rows.Scan(&task.ID, &task.Title, &task.Category, &start, &end, &task.DoneAt,
			&task.DoneByName, &task.UserID, &task.DisplayName, &task.OrganizationName,
			&task.CreatedByID, &task.CreatedByName, &task.WorkItemID, &task.Note,
			&task.Priority, &task.SRID); err != nil {
			return nil, err
		}
		task.StartDate = start.Format(dateLayout)
		task.EndDate = end.Format(dateLayout)
		task.Done = task.DoneAt != nil
		task.Priority = normalizePriority(task.Priority)
		if task.SRID != "" {
			task.SRURL = itsm.linkFor(task.SRID)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// summariseSchedule counts a set of rows the way the board's header does, so
// the numbers cannot disagree with the rows they sit above — on the screen or
// in a tool result.
func summariseSchedule(tasks []scheduleTaskView, today string) scheduleSummary {
	summary := scheduleSummary{}
	people := map[int64]bool{}
	for _, task := range tasks {
		people[task.UserID] = true
		summary.Total++
		switch {
		case task.Done:
			summary.Done++
		case task.EndDate < today:
			summary.Overdue++
		case task.StartDate <= today && today <= task.EndDate:
			summary.Today++
		}
		if !task.Done && task.Priority == priorityUrgent {
			summary.Urgent++
		}
	}
	summary.People = len(people)
	return summary
}

func (a *App) listScheduleTasks(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	location := a.serviceLocation(r.Context())
	today := time.Now().In(location)

	from, to, err := scheduleRange(r, today)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_RANGE", err.Error())
		return
	}
	scope := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = scopeTeam
	}
	if scope != scopeSelf && scope != scopeTeam {
		writeError(w, http.StatusBadRequest, "INVALID_SCOPE", "조회 범위는 SELF 또는 TEAM이어야 합니다.")
		return
	}

	tasks, err := a.scheduleTasksBetween(r.Context(), p, from, to, scope == scopeTeam)
	if err != nil {
		a.logger.Error("list schedule", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무 일정을 조회할 수 없습니다.")
		return
	}

	owners, manageAll, err := a.manageableOwners(r.Context(), p)
	if err != nil {
		a.logger.Error("schedule owners", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무 일정을 조회할 수 없습니다.")
		return
	}

	todayDate := today.Format(dateLayout)
	for index := range tasks {
		tasks[index].CanEdit = manageAll || owners[tasks[index].UserID] || tasks[index].CreatedByID == p.ID
	}
	view := scheduleResponse{
		From: from.Format(dateLayout), To: to.Format(dateLayout), Scope: scope,
		Today: todayDate, Tasks: tasks, Summary: summariseSchedule(tasks, todayDate),
	}
	writeData(w, http.StatusOK, view)
}

const dateLayout = "2006-01-02"

// scheduleRange reads the window to draw, defaulting to the month the reader is
// in — the board's own idea of "now", so a bookmarked link with no dates opens
// on this month rather than on whatever month it was saved in.
func scheduleRange(r *http.Request, today time.Time) (time.Time, time.Time, error) {
	fromText := strings.TrimSpace(r.URL.Query().Get("from"))
	toText := strings.TrimSpace(r.URL.Query().Get("to"))
	if fromText == "" && toText == "" {
		first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		return first, first.AddDate(0, 1, -1), nil
	}
	from, err := time.Parse(dateLayout, fromText)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("시작일을 YYYY-MM-DD 형식으로 입력하세요.")
	}
	to, err := time.Parse(dateLayout, toText)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("종료일을 YYYY-MM-DD 형식으로 입력하세요.")
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, errors.New("종료일은 시작일보다 빠를 수 없습니다.")
	}
	if to.Sub(from) > scheduleSpanDays*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("한 번에 조회할 수 있는 기간은 최대 %d일입니다.", scheduleSpanDays)
	}
	return from, to, nil
}

type scheduleInput struct {
	Title      string `json:"title"`
	Category   string `json:"category"`
	StartDate  string `json:"startDate"`
	EndDate    string `json:"endDate"`
	AssigneeID int64  `json:"assigneeId"`
	WorkItemID *int64 `json:"workItemId"`
	Note       string `json:"note"`
	Priority   string `json:"priority"`
	SRID       string `json:"srId"`
}

// scheduleRow is one validated line, in the shape the table takes.
type scheduleRow struct {
	Title    string
	Category string
	Note     string
	Priority string
	SRID     string
	Start    time.Time
	End      time.Time
}

// parse validates one submitted row.
//
// The ITSM settings are passed in because an SR number is checked against the
// same pattern here as it is on lookup. A number that could be typed into the
// form but never fetched would be a link that renders and never works.
func (input scheduleInput) parse(itsm itsmSettings) (scheduleRow, error) {
	row := scheduleRow{Priority: normalizePriority(input.Priority)}
	row.Title = strings.TrimSpace(input.Title)
	if row.Title == "" {
		return scheduleRow{}, errors.New("업무명을 입력하세요.")
	}
	row.Title = trimRunes(row.Title, scheduleTitleLimit)
	row.Category = trimRunes(strings.TrimSpace(input.Category), 80)
	row.Note = trimRunes(strings.TrimSpace(input.Note), scheduleNoteLimit)

	if row.SRID = strings.TrimSpace(input.SRID); row.SRID != "" {
		if err := validITSMID(itsm, row.SRID); err != nil {
			return scheduleRow{}, err
		}
	}

	start, err := time.Parse(dateLayout, strings.TrimSpace(input.StartDate))
	if err != nil {
		return scheduleRow{}, errors.New("시작일을 YYYY-MM-DD 형식으로 입력하세요.")
	}
	row.Start = start
	// A single day is the common case, so an omitted end date means "the same
	// day" rather than an error somebody has to read.
	endText := strings.TrimSpace(input.EndDate)
	if endText == "" {
		row.End = start
	} else {
		end, err := time.Parse(dateLayout, endText)
		if err != nil {
			return scheduleRow{}, errors.New("종료일을 YYYY-MM-DD 형식으로 입력하세요.")
		}
		row.End = end
	}
	if row.End.Before(row.Start) {
		return scheduleRow{}, errors.New("종료일은 시작일보다 빠를 수 없습니다.")
	}
	if row.End.Sub(row.Start) > scheduleSpanDays*24*time.Hour {
		return scheduleRow{}, fmt.Errorf("하나의 일정은 최대 %d일까지 지정할 수 있습니다.", scheduleSpanDays)
	}
	return row, nil
}

// canManageScheduleTask decides every write.
//
// The assignee, whoever put the row on the board, an administrator, and the
// leader the assignee reports to. The leader is included here and excluded from
// editing work items on purpose: rewriting somebody's reporting history is not
// the same act as moving a date on a plan the leader drew up.
func (a *App) canManageScheduleTask(ctx context.Context, p *principal, owner, createdBy int64) (bool, error) {
	if p.ID == owner || p.ID == createdBy || p.Role == "ADMIN" {
		return true, nil
	}
	return a.canViewPerson(ctx, p, owner)
}

func (a *App) createScheduleTask(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	var input scheduleInput
	if !decodeJSON(w, r, &input) {
		return
	}
	row, err := input.parse(a.loadITSMSettings(r.Context()))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SCHEDULE", err.Error())
		return
	}
	assignee := input.AssigneeID
	if assignee == 0 {
		assignee = p.ID
	}
	if assignee != p.ID {
		// Assigning to somebody else is the team leader's half of this feature.
		// It is also the half that could put work on a stranger's board, so it
		// is checked against the same scope that decides everything else.
		allowed, err := a.canViewPerson(r.Context(), p, assignee)
		if err != nil {
			a.logger.Error("schedule assignee scope", "error", err, "trace", traceIDFromContext(r.Context()))
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "담당자를 확인할 수 없습니다.")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "담당자로 지정할 수 있는 조직원이 아닙니다.")
			return
		}
	}
	if !a.scheduleWorkItemAllowed(w, r, input.WorkItemID, assignee) {
		return
	}

	var id int64
	if err := a.db.QueryRow(r.Context(), `
		INSERT INTO schedule_tasks(user_id, created_by, title, category, start_date, end_date,
			work_item_id, note, priority, sr_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		assignee, p.ID, row.Title, row.Category, row.Start, row.End,
		input.WorkItemID, row.Note, row.Priority, row.SRID).Scan(&id); err != nil {
		a.logger.Error("create schedule", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무 일정을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "schedule.create", "schedule_task", strconv.FormatInt(id, 10), map[string]any{
		"assigneeId": assignee, "startDate": row.Start.Format(dateLayout),
		"endDate": row.End.Format(dateLayout), "priority": row.Priority, "srId": row.SRID,
	})
	writeData(w, http.StatusCreated, map[string]any{"id": id})
}

// scheduleWorkItemAllowed refuses a link to a task that is not the assignee's.
//
// The link exists so the board and 업무 추적 describe the same work. Pointing a
// row at somebody else's task would make it describe two.
func (a *App) scheduleWorkItemAllowed(w http.ResponseWriter, r *http.Request, workItemID *int64, assignee int64) bool {
	if workItemID == nil {
		return true
	}
	owner, _, err := a.workItemOwner(r.Context(), *workItemID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusBadRequest, "WORK_ITEM_NOT_FOUND", "연결할 업무를 찾을 수 없습니다.")
		return false
	}
	if err != nil {
		a.logger.Error("schedule work item", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "연결할 업무를 확인할 수 없습니다.")
		return false
	}
	if owner != assignee {
		writeError(w, http.StatusBadRequest, "WORK_ITEM_NOT_OWNED", "담당자 본인의 업무에만 연결할 수 있습니다.")
		return false
	}
	return true
}

// scheduleTaskOwners returns who the row belongs to and who put it there.
func (a *App) scheduleTaskOwners(ctx context.Context, id int64) (owner, createdBy int64, err error) {
	err = a.db.QueryRow(ctx, `SELECT user_id, created_by FROM schedule_tasks WHERE id=$1`, id).
		Scan(&owner, &createdBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, errNotFound
	}
	return owner, createdBy, err
}

// authorizeScheduleTask resolves the row and the reader's right to write it,
// answering on the response when either is missing.
func (a *App) authorizeScheduleTask(w http.ResponseWriter, r *http.Request, id int64) (int64, bool) {
	p := currentPrincipal(r.Context())
	owner, createdBy, err := a.scheduleTaskOwners(r.Context(), id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "SCHEDULE_NOT_FOUND", "업무 일정을 찾을 수 없습니다.")
		return 0, false
	}
	if err != nil {
		a.logger.Error("schedule owner", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무 일정을 조회할 수 없습니다.")
		return 0, false
	}
	allowed, err := a.canManageScheduleTask(r.Context(), p, owner, createdBy)
	if err != nil {
		a.logger.Error("schedule permission", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "권한을 확인할 수 없습니다.")
		return 0, false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "이 업무 일정을 수정할 권한이 없습니다.")
		return 0, false
	}
	return owner, true
}

func (a *App) updateScheduleTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	owner, ok := a.authorizeScheduleTask(w, r, id)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	var input scheduleInput
	if !decodeJSON(w, r, &input) {
		return
	}
	row, err := input.parse(a.loadITSMSettings(r.Context()))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SCHEDULE", err.Error())
		return
	}
	assignee := input.AssigneeID
	if assignee == 0 {
		assignee = owner
	}
	if assignee != owner && assignee != p.ID {
		allowed, err := a.canViewPerson(r.Context(), p, assignee)
		if err != nil {
			a.logger.Error("schedule assignee scope", "error", err, "trace", traceIDFromContext(r.Context()))
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "담당자를 확인할 수 없습니다.")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "담당자로 지정할 수 있는 조직원이 아닙니다.")
			return
		}
	}
	if !a.scheduleWorkItemAllowed(w, r, input.WorkItemID, assignee) {
		return
	}
	if _, err := a.db.Exec(r.Context(), `
		UPDATE schedule_tasks SET user_id=$1, title=$2, category=$3, start_date=$4, end_date=$5,
			work_item_id=$6, note=$7, priority=$8, sr_id=$9, updated_at=now() WHERE id=$10`,
		assignee, row.Title, row.Category, row.Start, row.End, input.WorkItemID, row.Note,
		row.Priority, row.SRID, id); err != nil {
		a.logger.Error("update schedule", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무 일정을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "schedule.update", "schedule_task", strconv.FormatInt(id, 10), map[string]any{
		"assigneeId": assignee, "startDate": row.Start.Format(dateLayout),
		"endDate": row.End.Format(dateLayout), "priority": row.Priority, "srId": row.SRID,
	})
	writeData(w, http.StatusOK, map[string]any{"id": id})
}

// setScheduleTaskDone is the checkbox.
//
// Its own endpoint, taking one boolean, because it is the action this feature
// exists for: pressed on a wall display during a stand-up, dozens of times a
// morning. Routing it through the editor would make ticking a box a read of the
// whole row, a form and a save.
func (a *App) setScheduleTaskDone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, ok := a.authorizeScheduleTask(w, r, id); !ok {
		return
	}
	p := currentPrincipal(r.Context())
	var input struct {
		Done bool `json:"done"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var doneAt *time.Time
	var doneBy *int64
	if input.Done {
		now := time.Now()
		doneAt, doneBy = &now, &p.ID
	}
	// Unchecking clears who checked it as well as when. A board that remembers
	// "완료: 홍길동" under an unticked box is telling two stories at once.
	if _, err := a.db.Exec(r.Context(),
		`UPDATE schedule_tasks SET done_at=$1, done_by=$2, updated_at=now() WHERE id=$3`,
		doneAt, doneBy, id); err != nil {
		a.logger.Error("schedule done", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "완료 여부를 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "schedule.done", "schedule_task", strconv.FormatInt(id, 10), map[string]any{"done": input.Done})
	writeData(w, http.StatusOK, map[string]any{"id": id, "done": input.Done})
}

func (a *App) deleteScheduleTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, ok := a.authorizeScheduleTask(w, r, id); !ok {
		return
	}
	if _, err := a.db.Exec(r.Context(), `DELETE FROM schedule_tasks WHERE id=$1`, id); err != nil {
		a.logger.Error("delete schedule", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무 일정을 삭제할 수 없습니다.")
		return
	}
	a.audit(r, currentPrincipal(r.Context()), "schedule.delete", "schedule_task", strconv.FormatInt(id, 10), nil)
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}
