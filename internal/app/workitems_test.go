package app

import (
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
	if item.StalledWeeks != 3 || !item.Stalled {
		t.Errorf("stalledWeeks = %d stalled = %v, want 3/true", item.StalledWeeks, item.Stalled)
	}
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
