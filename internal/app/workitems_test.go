package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func week(start string, progress int, current, next, issue, ask string) workItemWeek {
	return workItemWeek{WeekStart: start, Progress: progress, CurrentResult: current,
		NextPlan: next, Issue: issue, ManagementAsk: ask}
}

func TestSummarizeWorkItemDerivesAgeing(t *testing.T) {
	item := &workItemView{Weeks: []workItemWeek{
		week("2026-06-01", 10, "착수", "설계", "", ""),
		week("2026-06-08", 30, "설계", "구현", "인력 부족", ""),
		// 06-15 is missing: the task went unreported for a week.
		week("2026-06-22", 30, "구현 지연", "구현", "인력 부족", "인원 1명 지원 필요"),
		week("2026-06-29", 30, "구현 지연", "구현", "인력 부족", "인원 1명 지원 필요"),
	}}
	summarizeWorkItem(item, defaultRollupConfig())

	if item.FirstWeek != "2026-06-01" || item.LastWeek != "2026-06-29" {
		t.Errorf("span = %s..%s", item.FirstWeek, item.LastWeek)
	}
	if item.ReportedWeeks != 4 {
		t.Errorf("reportedWeeks = %d, want 4", item.ReportedWeeks)
	}
	// Five calendar weeks span, four of them reported.
	if item.AgeWeeks != 5 {
		t.Errorf("ageWeeks = %d, want 5", item.AgeWeeks)
	}
	if item.SilentWeeks != 1 {
		t.Errorf("silentWeeks = %d, want 1", item.SilentWeeks)
	}
	if item.Progress != 30 || item.StartProgress != 10 || item.ProgressGain != 20 {
		t.Errorf("progress %d start %d gain %d", item.Progress, item.StartProgress, item.ProgressGain)
	}
	// The figure has read 30% since 06-08, and 06-08 to 06-29 is four weeks on
	// the calendar. This used to expect three, the number of reports — which
	// skips the missing 06-15 and so understates exactly the tasks nobody is
	// touching. Standing still elapses whether or not anybody writes it down.
	if item.StalledWeeks != 4 || !item.Stalled {
		t.Errorf("stalledWeeks = %d stalled = %v, want 4/true", item.StalledWeeks, item.Stalled)
	}
	// An issue is different: it is an observation, and an unreported week
	// observed nothing. Three reports named an issue, so three it is.
	if item.IssueWeeks != 3 || !item.AtRisk {
		t.Errorf("issueWeeks = %d atRisk = %v, want 3/true", item.IssueWeeks, item.AtRisk)
	}
	// The same plan was restated three weeks running.
	if item.RepeatedPlan != 3 {
		t.Errorf("repeatedPlan = %d, want 3", item.RepeatedPlan)
	}
	if item.LatestManagementAsk != "인원 1명 지원 필요" {
		t.Errorf("latest ask = %q", item.LatestManagementAsk)
	}
	if !item.Carryover {
		t.Error("an unfinished task with a plan is a carryover")
	}
}

func TestSummarizeWorkItemCompletedIsNeitherStalledNorAtRisk(t *testing.T) {
	item := &workItemView{Weeks: []workItemWeek{
		week("2026-06-01", 40, "a", "b", "지연", ""),
		week("2026-06-08", 40, "b", "c", "지연", ""),
		week("2026-06-15", 100, "완료", "", "", ""),
	}}
	summarizeWorkItem(item, defaultRollupConfig())
	if !item.Completed {
		t.Fatal("expected completed")
	}
	if item.Stalled || item.AtRisk {
		t.Errorf("completed work must not be stalled(%v) or at risk(%v)", item.Stalled, item.AtRisk)
	}
	if item.Carryover {
		t.Error("completed work is not a carryover")
	}
	// The issue history is still visible even though it is no longer a risk.
	if item.IssueWeeks != 2 {
		t.Errorf("issueWeeks = %d, want the history kept", item.IssueWeeks)
	}
}

func TestSummarizeWorkItemMergesDuplicateWeeks(t *testing.T) {
	// The author split one task into two rows in the same week.
	item := &workItemView{Weeks: []workItemWeek{
		week("2026-06-01", 20, "앞단 작업", "다음", "", ""),
		week("2026-06-01", 55, "뒷단 작업", "다음", "자원 부족", ""),
		week("2026-06-08", 60, "이어서", "다음", "", ""),
	}}
	summarizeWorkItem(item, defaultRollupConfig())
	if item.ReportedWeeks != 2 {
		t.Errorf("reportedWeeks = %d, want 2 after merging the duplicate week", item.ReportedWeeks)
	}
	if item.StartProgress != 55 {
		t.Errorf("startProgress = %d, want the furthest progress of that week", item.StartProgress)
	}
	if item.IssueWeeks != 1 {
		t.Errorf("issueWeeks = %d, want 1", item.IssueWeeks)
	}
	// Merging two entries renders the house bullet list, as elsewhere in the app.
	if got := item.Weeks[0].CurrentResult; got != "• 앞단 작업\n• 뒷단 작업" {
		t.Errorf("merged result = %q", got)
	}
}

func TestSummarizeWorkItemSingleWeek(t *testing.T) {
	item := &workItemView{Weeks: []workItemWeek{week("2026-06-01", 100, "완료", "", "", "")}}
	summarizeWorkItem(item, defaultRollupConfig())
	if item.AgeWeeks != 1 || item.ReportedWeeks != 1 || item.SilentWeeks != 0 {
		t.Errorf("age %d reported %d silent %d", item.AgeWeeks, item.ReportedWeeks, item.SilentWeeks)
	}
	if item.Stalled {
		t.Error("a single week cannot be stalled")
	}
}

func TestSummarizeWorkItemEmptyIsSafe(t *testing.T) {
	item := &workItemView{}
	summarizeWorkItem(item, defaultRollupConfig())
	if item.ReportedWeeks != 0 || item.Stalled || item.AtRisk {
		t.Error("an empty work item must summarize to nothing")
	}
}

// Everything summarizeWorkItem writes is derived from the snapshots, so running
// it twice must produce the same view. It did not: the issue, stall and
// repeated-plan counters were `++` over the weeks with nothing clearing them
// first, so a second call doubled them and turned work that moved every single
// week into a stalled task.
//
// Only one caller runs it once per freshly built view, which is why this never
// showed up in production. That is exactly the kind of property that needs a
// test rather than a convention.
func TestSummarizeWorkItemIsIdempotent(t *testing.T) {
	build := func() workItemView {
		weeks := []workItemWeek{}
		for index, progress := range []int{10, 10, 25, 25, 25, 40} {
			weeks = append(weeks, workItemWeek{
				WeekStart: shiftISOWeek("2026-06-01", index), Progress: progress,
				NextPlan: "규칙 확장", Issue: "방화벽 정책 대기",
			})
		}
		return workItemView{ID: 1, Title: "업무", DueDate: "2026-09-21", Weeks: weeks}
	}
	once, twice := build(), build()
	cfg := defaultRollupConfig()
	summarizeWorkItem(&once, cfg)
	summarizeWorkItem(&twice, cfg)
	summarizeWorkItem(&twice, cfg)

	if once.IssueWeeks != twice.IssueWeeks || once.StalledWeeks != twice.StalledWeeks || once.RepeatedPlan != twice.RepeatedPlan {
		t.Errorf("counters drift on a second call: issue %d→%d, stalled %d→%d, plan %d→%d",
			once.IssueWeeks, twice.IssueWeeks, once.StalledWeeks, twice.StalledWeeks, once.RepeatedPlan, twice.RepeatedPlan)
	}
	if once.Stalled != twice.Stalled || once.AtRisk != twice.AtRisk {
		t.Errorf("verdicts drift: stalled %v→%v, atRisk %v→%v", once.Stalled, twice.Stalled, once.AtRisk, twice.AtRisk)
	}
	if once.Forecast != twice.Forecast || once.DueOutlook != twice.DueOutlook {
		t.Errorf("forecast drifts:\n once=%+v %+v\ntwice=%+v %+v", once.Forecast, once.DueOutlook, twice.Forecast, twice.DueOutlook)
	}
	if len(once.Weeks) != len(twice.Weeks) {
		t.Errorf("weeks accumulate: %d→%d", len(once.Weeks), len(twice.Weeks))
	}
}

// A capped list has to say what it capped. Returning a bare array let a screen
// present 200 rows as the whole set, and a team leader counting rows would
// undercount their own team without any sign that they had.
func TestWorkItemListCarriesWhatItLeftOut(t *testing.T) {
	items := make([]workItemView, 640)
	for index := range items {
		items[index] = workItemView{ID: int64(index + 1), Weeks: []workItemWeek{{WeekStart: "2026-06-01", Progress: 10}}}
	}
	page := workItemListView{Items: items[:workItemPageDefault], Total: len(items), Limit: workItemPageDefault, Offset: 0}
	if page.Total <= len(page.Items) {
		t.Fatalf("total %d does not exceed the page %d, so this proves nothing", page.Total, len(page.Items))
	}
	body, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"total":640`, `"limit":200`, `"offset":0`} {
		if !strings.Contains(string(body), key) {
			t.Errorf("the response does not carry %s: %s", key, string(body)[:120])
		}
	}
}

// The weekly history is the bulk of the payload and the table never draws it.
// Sending it for every row made the org-wide list 23.6 MB, of which 19 MB was
// text nothing on screen read.
func TestWorkItemListLeavesTheWeeklyHistoryOut(t *testing.T) {
	withWeeks := workItemView{ID: 1, Title: "업무", Weeks: []workItemWeek{
		{WeekStart: "2026-06-01", Progress: 10, CurrentResult: "진행", NextPlan: "계속"},
		{WeekStart: "2026-06-08", Progress: 20, CurrentResult: "진행", NextPlan: "계속"},
	}}
	full, err := json.Marshal(withWeeks)
	if err != nil {
		t.Fatal(err)
	}
	listed := withWeeks
	listed.Weeks = nil
	trimmed, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trimmed), `"weeks"`) {
		t.Errorf("the list row still carries its history: %s", string(trimmed))
	}
	if !strings.Contains(string(full), `"weeks"`) {
		t.Errorf("the detail row must still carry it: %s", string(full))
	}
	if len(trimmed) >= len(full) {
		t.Errorf("trimming saved nothing: %d vs %d", len(trimmed), len(full))
	}
}

// guards: summarizeWorkItem
//
// The meeting agenda orders by this number and the sentence beside it says
// "진척이 N주째 멈춰 있습니다". Counting reports instead of weeks put the task
// nobody had touched in a month and a half below one that was stalled three
// weeks with somebody watching it every week — the ranking ran backwards for
// exactly the tasks the meeting exists to surface.
func TestStallIsMeasuredInWeeksNotInReports(t *testing.T) {
	cases := []struct {
		name    string
		weeks   []workItemWeek
		stalled int
	}{
		{"매주 보고하며 여섯 주 동안 50%", []workItemWeek{
			week("2026-07-06", 50, "", "계속", "", ""), week("2026-07-13", 50, "", "계속", "", ""),
			week("2026-07-20", 50, "", "계속", "", ""), week("2026-07-27", 50, "", "계속", "", ""),
			week("2026-08-03", 50, "", "계속", "", ""), week("2026-08-10", 50, "", "계속", "", ""),
		}, 6},
		{"여섯 주 사이에 두 번만 보고, 둘 다 50%", []workItemWeek{
			week("2026-07-06", 50, "", "계속", "", ""), week("2026-08-10", 50, "", "계속", "", ""),
		}, 6},
		{"세 주 연속 50%", []workItemWeek{
			week("2026-07-27", 50, "", "계속", "", ""), week("2026-08-03", 50, "", "계속", "", ""),
			week("2026-08-10", 50, "", "계속", "", ""),
		}, 3},
		{"지난주에 움직였다", []workItemWeek{
			week("2026-07-27", 30, "", "계속", "", ""), week("2026-08-03", 30, "", "계속", "", ""),
			week("2026-08-10", 60, "", "계속", "", ""),
		}, 1},
	}
	for _, item := range cases {
		view := &workItemView{Weeks: item.weeks}
		summarizeWorkItem(view, defaultRollupConfig())
		if view.StalledWeeks != item.stalled {
			t.Errorf("%s: stalledWeeks = %d, want %d", item.name, view.StalledWeeks, item.stalled)
		}
	}

	// A finished task is not stalled, however long the figure has read 100.
	done := &workItemView{Weeks: []workItemWeek{
		week("2026-07-06", 100, "", "", "", ""), week("2026-08-10", 100, "", "", "", ""),
	}}
	summarizeWorkItem(done, defaultRollupConfig())
	if done.StalledWeeks != 0 || done.Stalled {
		t.Errorf("a completed task reads as stalled for %d week(s)", done.StalledWeeks)
	}
}
