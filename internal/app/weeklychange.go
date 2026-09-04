package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// One classification of "what happened to this task last week", shared by
// everything that needs the answer.
//
// It lives on its own because the roadmap's first rule for work item identity
// applies just as much to what is derived from it: if each screen re-implements
// the comparison, the screens disagree, and a reader who sees "완료" in one place
// and "진척" in another stops trusting both.

type changeKind string

const (
	// changeAbsent means the task was not reported in either week.
	changeAbsent changeKind = "ABSENT"
	// changeNew is a task reported for the very first time.
	changeNew changeKind = "NEW"
	// changeResumed is a task that was reported before, went quiet for at least
	// one week, and came back. It used to fall through every branch and vanish
	// from the meeting agenda, which is the opposite of what a room wants to
	// hear: work that went quiet and returned is exactly the news.
	changeResumed changeKind = "RESUMED"
	// changeCompleted is a task that reached 100% this week.
	changeCompleted changeKind = "COMPLETED"
	// changeProgressed is a task whose progress figure rose.
	changeProgressed changeKind = "PROGRESSED"
	// changeRegressed is a task reported at a lower progress than the week
	// before. Progress does not run backwards on its own, so this is either a
	// correction or a mistake, and either way somebody has to look.
	changeRegressed changeKind = "REGRESSED"
	// changeStalled is a task sitting at the same progress long enough to count.
	changeStalled changeKind = "STALLED"
	// changeSteady is a task that was reported and simply carried on. It is the
	// most common outcome and the least worth saying out loud.
	changeSteady changeKind = "STEADY"
	// changeSilent is a task that was open last week and is missing this week.
	changeSilent changeKind = "SILENT"
)

type weeklyChange struct {
	Kind changeKind
	// Note explains the classification in the reader's language. Every screen
	// that shows a classification shows this with it.
	Note string
	// Detail is the sentence from the report that supports the note.
	Detail        string
	ProgressDelta int
}

// classifyWeeklyChange compares one task's two most recent snapshots.
//
// The order of the branches is the order of newsworthiness, and the first match
// wins. A task that both resumed and completed is reported as completed, because
// that is the fact that changes what anyone does next.
func classifyWeeklyChange(item workItemView, week string, cfg rollupConfig) weeklyChange {
	current := snapshotFor(item, week)
	prior := snapshotPrior(item, week)

	switch {
	case current == nil && prior == nil:
		return weeklyChange{Kind: changeAbsent}

	case current == nil && prior != nil:
		if item.Completed {
			// Finished work stops being reported. That is not a gap.
			return weeklyChange{Kind: changeAbsent}
		}
		return weeklyChange{
			Kind:   changeSilent,
			Note:   "지난주에는 보고됐으나 이번 주 보고에 없습니다.",
			Detail: strings.TrimSpace(prior.NextPlan),
		}

	case current.Progress >= 100 && (prior == nil || prior.Progress < 100):
		delta := 100
		if prior != nil {
			delta = 100 - prior.Progress
		}
		return weeklyChange{
			Kind:          changeCompleted,
			Note:          fmt.Sprintf("%d주 만에 완료됐습니다.", item.ReportedWeeks),
			Detail:        strings.TrimSpace(current.CurrentResult),
			ProgressDelta: delta,
		}

	// The item's first week is the date its earliest snapshot was written under,
	// so it is compared against the snapshot found rather than against the date
	// asked for: on a moved grid those two differ, and a brand new task would be
	// announced as 재개 — resumed from a silence that never happened.
	case prior == nil && item.FirstWeek == current.WeekStart:
		return weeklyChange{
			Kind:   changeNew,
			Note:   "이번 주에 시작된 업무입니다.",
			Detail: strings.TrimSpace(current.CurrentResult),
		}

	case prior == nil:
		note := "지난주 보고에 없다가 다시 보고됐습니다."
		if item.SilentWeeks > 0 {
			note = fmt.Sprintf("%d주 쉬었다가 다시 보고됐습니다.", item.SilentWeeks)
		}
		return weeklyChange{
			Kind:   changeResumed,
			Note:   note,
			Detail: strings.TrimSpace(current.CurrentResult),
		}

	case current.Progress < prior.Progress:
		return weeklyChange{
			Kind:          changeRegressed,
			Note:          "진척도가 지난주보다 낮게 보고됐습니다. 확인이 필요합니다.",
			Detail:        strings.TrimSpace(current.CurrentResult),
			ProgressDelta: current.Progress - prior.Progress,
		}

	case current.Progress > prior.Progress:
		return weeklyChange{
			Kind:          changeProgressed,
			Note:          fmt.Sprintf("진척 %d%% → %d%%", prior.Progress, current.Progress),
			Detail:        strings.TrimSpace(current.CurrentResult),
			ProgressDelta: current.Progress - prior.Progress,
		}

	case item.StalledWeeks >= cfg.StallWeeks && !item.Completed:
		return weeklyChange{
			Kind:   changeStalled,
			Note:   fmt.Sprintf("%d주째 진척이 없습니다.", item.StalledWeeks),
			Detail: strings.TrimSpace(current.NextPlan),
		}
	}

	return weeklyChange{Kind: changeSteady, Detail: strings.TrimSpace(current.CurrentResult)}
}

// changeSummaryEntry is one task in the week's change list.
type changeSummaryEntry struct {
	WorkItemID       int64      `json:"workItemId"`
	Title            string     `json:"title"`
	Category         string     `json:"category"`
	DisplayName      string     `json:"displayName"`
	OrganizationName string     `json:"organizationName"`
	Kind             changeKind `json:"kind"`
	Note             string     `json:"note"`
	Detail           string     `json:"detail"`
	Progress         int        `json:"progress"`
	ProgressDelta    int        `json:"progressDelta"`
}

type changeSummaryGroup struct {
	Kind    changeKind           `json:"kind"`
	Title   string               `json:"title"`
	Count   int                  `json:"count"`
	Entries []changeSummaryEntry `json:"entries"`
	Limit   int                  `json:"limit"`
}

// changeGroupLimit is how many rows one group carries.
//
// The groups had no cap. Read across a 300 person organisation the summary
// came back with 1,866 rows in one response — 진척 1,166 and 정체 700 — half a
// megabyte on the screen the product opens on. The dashboard draws the bar
// from count alone and never touches entries, so every one of those rows was
// serialised, sent and thrown away.
//
// count still carries the true number, so the bar is unchanged; limit says how
// many of them came with it.
const changeGroupLimit = 40

// orderChangeEntries puts the rows worth reading first, so cutting the tail
// cuts the least important thing rather than the highest work item id.
//
// The same reasoning as the meeting agenda: the size of the movement decides,
// because a task that jumped 40% and one that moved 1% are not equally worth
// somebody's attention. Within a group the direction is already fixed, so what
// is left is how far it moved, then how far it still has to go, then the title
// so the order is total and the same week does not reshuffle between requests.
func orderChangeEntries(entries []changeSummaryEntry) {
	sort.SliceStable(entries, func(x, y int) bool {
		left, right := entries[x], entries[y]
		leftSize, rightSize := left.ProgressDelta, right.ProgressDelta
		if leftSize < 0 {
			leftSize = -leftSize
		}
		if rightSize < 0 {
			rightSize = -rightSize
		}
		if leftSize != rightSize {
			return leftSize > rightSize
		}
		if left.Progress != right.Progress {
			return left.Progress < right.Progress
		}
		return left.Title < right.Title
	})
}

type changeSummaryView struct {
	Week         string               `json:"week"`
	PreviousWeek string               `json:"previousWeek"`
	Scope        string               `json:"scope"`
	Reported     int                  `json:"reported"`
	Changed      int                  `json:"changed"`
	Groups       []changeSummaryGroup `json:"groups"`
}

// changeGroupOrder is the reading order: what ended, what began, what moved,
// what went wrong, what stopped, what disappeared.
var changeGroupOrder = []struct {
	Kind  changeKind
	Title string
}{
	{changeCompleted, "완료"},
	{changeNew, "신규"},
	{changeResumed, "재개"},
	{changeProgressed, "진척"},
	{changeRegressed, "진척도 역행"},
	{changeStalled, "정체"},
	{changeSilent, "보고 누락"},
}

// buildChangeSummary groups one week's classifications.
//
// changeSteady is counted but never listed. "계속 진행 중" is what the written
// report already says, and a summary that repeats it stops being a summary.
func buildChangeSummary(items []workItemView, week string, cfg rollupConfig) changeSummaryView {
	previous := weekBefore(week)
	view := changeSummaryView{Week: week, PreviousWeek: previous, Groups: []changeSummaryGroup{}}
	byKind := map[changeKind][]changeSummaryEntry{}

	for _, item := range items {
		change := classifyWeeklyChange(item, week, cfg)
		if change.Kind == changeAbsent {
			continue
		}
		view.Reported++
		if change.Kind == changeSteady {
			continue
		}
		view.Changed++
		byKind[change.Kind] = append(byKind[change.Kind], changeSummaryEntry{
			WorkItemID: item.ID, Title: item.Title, Category: item.Category,
			DisplayName: item.DisplayName, OrganizationName: item.OrganizationName,
			Kind: change.Kind, Note: change.Note, Detail: change.Detail,
			Progress: item.Progress, ProgressDelta: change.ProgressDelta,
		})
	}

	for _, group := range changeGroupOrder {
		// An absent kind still gets an empty list rather than a null, so a
		// caller can iterate every group without checking each one first.
		entries := byKind[group.Kind]
		if entries == nil {
			entries = []changeSummaryEntry{}
		}
		orderChangeEntries(entries)
		total := len(entries)
		if len(entries) > changeGroupLimit {
			entries = entries[:changeGroupLimit]
		}
		view.Groups = append(view.Groups, changeSummaryGroup{
			Kind: group.Kind, Title: group.Title, Count: total, Entries: entries,
			Limit: changeGroupLimit,
		})
	}
	return view
}

// weeklyChanges serves the change summary for one week.
func (a *App) weeklyChanges(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	scope := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = scopeSelf
	}
	if scope != scopeSelf && scope != scopeTeam {
		writeError(w, http.StatusBadRequest, "INVALID_SCOPE", "조회 범위는 SELF 또는 TEAM이어야 합니다.")
		return
	}
	if scope == scopeTeam && p.Role == "USER" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조직 단위 변화 요약은 팀장 이상만 조회할 수 있습니다.")
		return
	}
	week := strings.TrimSpace(r.URL.Query().Get("week"))
	if week == "" {
		week = currentWeekStart(time.Now().In(a.serviceLocation(r.Context())),
			a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format("2006-01-02")
	}
	parsed, err := time.Parse("2006-01-02", week)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WEEK", "주차는 YYYY-MM-DD 형식이어야 합니다.")
		return
	}
	// The ageing figures several classifications rest on — stalled weeks, silent
	// weeks — cannot be read from two weeks of data. That was answered once by
	// widening this to 26 weeks, which is the right instinct and the wrong
	// shape: a stall of 32 weeks still reads 26, and "%d주째 진척이 없습니다"
	// then states the window. Whole history, then the window, like the agenda
	// and the digest.
	since := parsed.AddDate(0, 0, -7*26).Format("2006-01-02")
	loaded, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, scope == scopeSelf), "")
	if err != nil {
		a.logger.Error("weekly changes", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "주간 변화를 만들 수 없습니다.")
		return
	}
	items := make([]workItemView, 0, len(loaded))
	for _, item := range loaded {
		if item.LastWeek >= since {
			items = append(items, item)
		}
	}
	view := buildChangeSummary(items, week, a.rollupConfig(r.Context()))
	view.Scope = scope
	writeData(w, http.StatusOK, view)
}
