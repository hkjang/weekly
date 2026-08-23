package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Setting a deadline on a task, and saying before the date arrives whether the
// reported pace gets there.
//
// work_items.due_date has existed since the table did and nothing has ever
// written it — no endpoint, no screen, every row NULL. So the roadmap's
// schedule risk had no schedule to compare against, and v0.48 could only say
// how long the work would take, never whether that was late.
//
// The one deadline the product did hold was a decision's follow-up date, and it
// was only ever reported after it passed. Being told on the 16th that the 15th
// was missed is a record, not a warning.

type dueDateInput struct {
	// DueDate is YYYY-MM-DD, or empty to clear. Clearing has to be expressible:
	// a date entered by mistake that can only be changed to another wrong date
	// is worse than no date at all.
	DueDate string `json:"dueDate"`
}

func (a *App) setWorkItemDueDate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input dueDateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	var due *time.Time
	if trimmed := strings.TrimSpace(input.DueDate); trimmed != "" {
		parsed, err := time.Parse("2006-01-02", trimmed)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_DUE_DATE", "마감일을 YYYY-MM-DD 형식으로 입력하세요.")
			return
		}
		due = &parsed
	}
	p := currentPrincipal(r.Context())
	owner, merged, err := a.workItemOwner(r.Context(), id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 조회할 수 없습니다.")
		return
	}
	if !canEditWorkItem(p, owner) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인 업무의 마감일만 설정할 수 있습니다.")
		return
	}
	if merged != nil {
		// The merged row is not what anyone reads any more, so a date set here
		// would be invisible and would look like the save silently failed.
		writeError(w, http.StatusBadRequest, "ALREADY_MERGED", "다른 업무로 합쳐진 업무에는 마감일을 설정할 수 없습니다. 합친 대상 업무에 설정하세요.")
		return
	}
	if _, err := a.db.Exec(r.Context(), `UPDATE work_items SET due_date=$1, updated_at=now() WHERE id=$2`, due, id); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "마감일을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "work_item.due_date", "work_item", strconv.FormatInt(id, 10), map[string]any{"dueDate": input.DueDate})
	writeData(w, http.StatusOK, map[string]string{"dueDate": input.DueDate})
}

// What the reported pace says about a deadline that has been set.
const (
	dueOutlookNone     = "NONE"     // no deadline set
	dueOutlookOverdue  = "OVERDUE"  // the date passed and the work is not done
	dueOutlookUnknown  = "UNKNOWN"  // too little history to project anything
	dueOutlookOnTrack  = "ON_TRACK" // both paces reach 100 in time
	dueOutlookAtRisk   = "AT_RISK"  // neither pace reaches 100 in time
	dueOutlookSplit    = "SPLIT"    // one does and one does not
	dueOutlookFinished = "FINISHED" // already complete
)

type dueOutlook struct {
	Kind      string `json:"kind"`
	DueDate   string `json:"dueDate,omitempty"`
	WeeksLeft int    `json:"weeksLeft"`
	// ProjectedLow and ProjectedHigh are the progress this work reaches by the
	// deadline at the two paces, capped at 100. They are the answer; the kind is
	// only a label on it.
	ProjectedLow  int    `json:"projectedLow"`
	ProjectedHigh int    `json:"projectedHigh"`
	Note          string `json:"note"`
}

// outlookForDueDate crosses a deadline with the pace the work has reported.
//
// asOf is the week the projection starts from — the last week reported, not
// today, because a task last reported a month ago has not been moving since and
// pretending otherwise would flatter it.
func outlookForDueDate(dueDate string, forecast completionForecast, weeks []rollupItemWeek, progress int) dueOutlook {
	if dueDate == "" {
		return dueOutlook{Kind: dueOutlookNone}
	}
	due, err := time.Parse("2006-01-02", dueDate)
	if err != nil {
		return dueOutlook{Kind: dueOutlookNone}
	}
	result := dueOutlook{DueDate: dueDate}
	if progress >= 100 {
		result.Kind = dueOutlookFinished
		result.ProjectedLow, result.ProjectedHigh = 100, 100
		result.Note = "완료됐습니다."
		return result
	}
	if len(weeks) == 0 {
		result.Kind = dueOutlookUnknown
		result.Note = "보고된 주차가 없어 마감일까지의 진척을 추정할 수 없습니다."
		return result
	}
	last := weeks[len(weeks)-1].WeekStart
	lastWeek, err := time.Parse("2006-01-02", last)
	if err != nil {
		return dueOutlook{Kind: dueOutlookNone}
	}
	// Whole weeks, rounded down. Half a week of the observed pace is not a unit
	// this data supports.
	weeksLeft := int(due.Sub(lastWeek).Hours() / (24 * 7))
	result.WeeksLeft = weeksLeft
	if weeksLeft <= 0 {
		// The deadline has arrived without the work finishing. That is observed,
		// so it is stated without any projection attached.
		result.Kind = dueOutlookOverdue
		result.ProjectedLow, result.ProjectedHigh = progress, progress
		result.Note = "마감일이 지났고 아직 완료되지 않았습니다."
		return result
	}
	if forecast.Kind == forecastInsufficient {
		result.Kind = dueOutlookUnknown
		result.Note = forecast.Note
		return result
	}

	project := func(pace float64) int {
		return int(math.Min(100, math.Max(0, float64(progress)+pace*float64(weeksLeft))))
	}
	low := project(math.Min(forecast.OverallPerWeek, forecast.RecentPerWeek))
	high := project(math.Max(forecast.OverallPerWeek, forecast.RecentPerWeek))
	result.ProjectedLow, result.ProjectedHigh = low, high

	switch {
	case low >= 100:
		result.Kind = dueOutlookOnTrack
		result.Note = fmt.Sprintf("마감일까지 %d주 남았고, 지금까지의 두 속도 모두 그 안에 100%%에 닿습니다.", weeksLeft)
	case high >= 100:
		// The answer depends on which pace holds, and that is the finding. A
		// single verdict here would pick one of the two and hide the other.
		result.Kind = dueOutlookSplit
		result.Note = fmt.Sprintf("마감일까지 %d주. 최근 속도(%.1f%%/주)로는 닿지만 전체 평균(%.1f%%/주)으로는 %d%%에 그칩니다.",
			weeksLeft, forecast.RecentPerWeek, forecast.OverallPerWeek, low)
		if forecast.RecentPerWeek < forecast.OverallPerWeek {
			result.Note = fmt.Sprintf("마감일까지 %d주. 전체 평균(%.1f%%/주)으로는 닿지만 최근 속도(%.1f%%/주)로는 %d%%에 그칩니다.",
				weeksLeft, forecast.OverallPerWeek, forecast.RecentPerWeek, low)
		}
	default:
		result.Kind = dueOutlookAtRisk
		if low == high {
			result.Note = fmt.Sprintf("마감일까지 %d주. 지금 속도(%.1f%%/주)로는 %d%%에 그칩니다.", weeksLeft, forecast.OverallPerWeek, low)
		} else {
			result.Note = fmt.Sprintf("마감일까지 %d주. 두 속도 어느 쪽으로도 %d~%d%%에 그칩니다.", weeksLeft, low, high)
		}
	}
	return result
}

// A deadline that was already agreed, sitting in the wrong column.
//
// Deadlines are not set by the person doing the work — they are agreed in a
// meeting, and the product already records that: a decision carries a follow-up
// date. So a team can meet, agree "9월 15일까지", write it down as a decision,
// and the work item's own deadline stays empty and its outlook says there is no
// deadline. Two dates for one piece of work, entered in different places, never
// speaking to each other.
//
// This does not copy the date across. A follow-up is not always the whole task,
// and silently promoting one to the work's deadline would claim something the
// meeting did not say. It offers it, named and dated, for one click.
type agreedDue struct {
	DueDate    string `json:"dueDate"`
	Title      string `json:"title"`
	DecidedBy  string `json:"decidedBy"`
	DecidedOn  string `json:"decidedOn"`
	FollowUp   string `json:"followUp"`
	DecisionID int64  `json:"decisionId"`
}

// agreedDueDates returns the earliest open follow-up deadline for each of the
// given work items. Earliest rather than latest: the nearest commitment is the
// one that constrains the work, and offering the far one would understate it.
func (a *App) agreedDueDates(ctx context.Context, ids []int64) (map[int64]agreedDue, error) {
	found := map[int64]agreedDue{}
	if len(ids) == 0 {
		return found, nil
	}
	rows, err := a.db.Query(ctx, `SELECT DISTINCT ON (d.work_item_id)
			d.work_item_id, d.id, d.due_date, d.title, d.decided_by, d.decided_on, d.follow_up
		FROM decisions d
		WHERE d.work_item_id = ANY($1) AND d.status = $2 AND d.due_date IS NOT NULL
		ORDER BY d.work_item_id, d.due_date ASC, d.id DESC`, ids, decisionOpen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var workItemID int64
		var entry agreedDue
		var due, decided time.Time
		if err := rows.Scan(&workItemID, &entry.DecisionID, &due, &entry.Title, &entry.DecidedBy, &decided, &entry.FollowUp); err != nil {
			return nil, err
		}
		entry.DueDate = due.Format("2006-01-02")
		entry.DecidedOn = decided.Format("2006-01-02")
		found[workItemID] = entry
	}
	return found, rows.Err()
}

// attachAgreedDueDates offers a meeting's deadline to work that has none.
//
// Only to work that has none: a task with its own deadline has had the question
// answered, and showing a second date beside it would turn one answer into an
// argument.
// workItemsWantingADeadline is the work the offer applies to: still running,
// and with no deadline of its own.
func workItemsWantingADeadline(items []workItemView) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		if item.DueDate == "" && !item.Completed {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func (a *App) attachAgreedDueDates(ctx context.Context, items []workItemView) {
	ids := workItemsWantingADeadline(items)
	if len(ids) == 0 {
		return
	}
	agreed, err := a.agreedDueDates(ctx, ids)
	if err != nil {
		// A missing suggestion is a smaller failure than a missing list. The
		// deadlines the caller asked for are already loaded.
		a.logger.Warn("load agreed due dates", "error", err)
		return
	}
	for index := range items {
		if entry, ok := agreed[items[index].ID]; ok {
			copied := entry
			items[index].AgreedDue = &copied
		}
	}
}
