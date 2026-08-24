package app

import (
	"strings"
	"testing"
)

// guards: outlookForDueDate=100
func TestOutlookForDueDate(t *testing.T) {
	// 10 a week, four weeks, sitting at 40.
	steady := series("2026-06-01", 10, 20, 30, 40)
	steadyForecast := forecastCompletion(steady, 40)
	// Slow overall, fast lately: 4 a week over the whole run, 15 a week now.
	uneven := series("2026-06-01", 0, 2, 4, 6, 36)
	unevenForecast := forecastCompletion(uneven, 36)
	// Fast early, slow lately: the run average is well ahead of the recent pace.
	slowing := series("2026-06-01", 0, 30, 60, 62, 64)
	slowingForecast := forecastCompletion(slowing, 64)

	cases := []struct {
		name     string
		due      string
		forecast completionForecast
		weeks    []rollupItemWeek
		progress int
		wantKind string
		wantLow  int
		wantHigh int
		noteHas  string
	}{
		{
			// Six weeks left at 10 a week clears 60 points with room to spare.
			name: "comfortable", due: "2026-08-10", forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: dueOutlookOnTrack, wantLow: 100, wantHigh: 100,
		},
		{
			// The projection runs from the last reported week, so a week whose
			// own date is unreadable leaves nothing to run from.
			name: "unreadable last week", due: "2026-08-10", forecast: steadyForecast,
			weeks: []rollupItemWeek{{WeekStart: "지난주", Progress: 40}}, progress: 40,
			wantKind: dueOutlookNone,
		},
		{
			// Fast early, slow lately — the mirror of the case above it. The
			// sentence has to name the pace that gets there, and here that is
			// the run average rather than the recent one.
			name: "recent pace is the slower one", due: "2026-07-20",
			forecast: slowingForecast, weeks: slowing, progress: 64,
			wantKind: dueOutlookSplit, noteHas: "전체 평균",
		},
		{
			// Neither pace arrives, and they disagree about how far short.
			name: "short by different amounts", due: "2026-07-06",
			forecast: slowingForecast, weeks: slowing, progress: 64,
			wantKind: dueOutlookAtRisk, noteHas: "두 속도 어느 쪽으로도",
		},
		{
			// A deadline with nothing reported behind it. Saying "at risk" here
			// would be a guess dressed as a projection.
			name: "nothing reported", due: "2026-08-10", forecast: forecastCompletion(nil, 0), weeks: nil, progress: 0,
			wantKind: dueOutlookUnknown, noteHas: "보고된 주차가 없어",
		},
		{
			// A stored date that will not parse. Whatever wrote it was wrong;
			// the screen must not turn that into a claim about the work.
			name: "unparseable date", due: "2026-13-45", forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: dueOutlookNone,
		},
		{
			// Three weeks left is 30 points, and 70 is not 100.
			name: "short", due: "2026-07-13", forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: dueOutlookAtRisk, wantLow: 70, wantHigh: 70, noteHas: "70%에 그칩니다",
		},
		{
			// Five weeks left. The recent sprint gets there, the run average
			// does not, and which one holds is exactly what nobody knows.
			name: "depends which pace holds", due: "2026-08-03", forecast: unevenForecast, weeks: uneven, progress: 36,
			wantKind: dueOutlookSplit, wantLow: 81, wantHigh: 100, noteHas: "최근 속도",
		},
		{
			// The date is behind the last reported week. Nothing is projected,
			// because nothing needs to be — it is late, and that is observed.
			name: "already past", due: "2026-06-15", forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: dueOutlookOverdue, wantLow: 40, wantHigh: 40, noteHas: "마감일이 지났고",
		},
		{
			name: "done before the date", due: "2026-08-10", forecast: steadyForecast, weeks: steady, progress: 100,
			wantKind: dueOutlookFinished, wantLow: 100, wantHigh: 100,
		},
		{
			name: "no deadline", due: "", forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: dueOutlookNone,
		},
		{
			// Two weeks of history cannot support a pace, so it cannot support a
			// verdict on a deadline either.
			name: "not enough history", due: "2026-08-10",
			forecast: forecastCompletion(series("2026-06-01", 10, 20), 20),
			weeks:    series("2026-06-01", 10, 20), progress: 20,
			wantKind: dueOutlookUnknown,
		},
	}
	for _, item := range cases {
		got := outlookForDueDate(item.due, item.forecast, item.weeks, item.progress)
		if got.Kind != item.wantKind {
			t.Errorf("%s: kind=%s want=%s (%s)", item.name, got.Kind, item.wantKind, got.Note)
			continue
		}
		if item.wantLow != 0 && got.ProjectedLow != item.wantLow {
			t.Errorf("%s: low=%d want=%d (%s)", item.name, got.ProjectedLow, item.wantLow, got.Note)
		}
		if item.wantHigh != 0 && got.ProjectedHigh != item.wantHigh {
			t.Errorf("%s: high=%d want=%d (%s)", item.name, got.ProjectedHigh, item.wantHigh, got.Note)
		}
		if item.noteHas != "" && !strings.Contains(got.Note, item.noteHas) {
			t.Errorf("%s: note %q does not say %q", item.name, got.Note, item.noteHas)
		}
	}
}

// The projection starts from the last reported week, not from today. A task
// last written up a month ago has not been moving since, and running the pace
// forward from today would credit it with four weeks it never worked.
func TestOutlookProjectsFromTheLastReportedWeekNotToday(t *testing.T) {
	weeks := series("2026-06-01", 10, 20, 30, 40)
	forecast := forecastCompletion(weeks, 40)
	// Last reported week is 2026-06-22; the deadline is five weeks after it.
	got := outlookForDueDate("2026-07-27", forecast, weeks, 40)
	if got.WeeksLeft != 5 {
		t.Errorf("weeksLeft=%d want=5", got.WeeksLeft)
	}
	if got.ProjectedHigh != 90 {
		t.Errorf("projected=%d want=90 — five weeks at 10 a week from 40", got.ProjectedHigh)
	}
}

// A deadline that has been met needs no warning, and one that has passed needs
// no projection. Neither case may borrow the language of the other.
func TestOutlookDoesNotProjectPastADateThatAlreadyArrived(t *testing.T) {
	weeks := series("2026-06-01", 10, 20, 30, 40)
	got := outlookForDueDate("2026-06-08", forecastCompletion(weeks, 40), weeks, 40)
	if got.Kind != dueOutlookOverdue {
		t.Fatalf("kind=%s want=%s", got.Kind, dueOutlookOverdue)
	}
	if got.ProjectedHigh != 40 {
		t.Errorf("an overdue task is reported at its actual progress, got %d", got.ProjectedHigh)
	}
	if strings.Contains(got.Note, "%/주") {
		t.Errorf("a passed deadline is observed, not projected: %q", got.Note)
	}
}

// The offer only goes to work that has no deadline. A task whose owner already
// answered the question does not need a second date beside the first, which
// turns one answer into an argument. Finished work has nothing left to be late
// for.
// guards: workItemsWantingADeadline
func TestAgreedDueIsOnlyOfferedWhereThereIsNoDeadline(t *testing.T) {
	got := workItemsWantingADeadline([]workItemView{
		{ID: 1, DueDate: ""},
		{ID: 2, DueDate: "2026-09-01"},
		{ID: 3, DueDate: "", Completed: true},
		{ID: 4, DueDate: "2026-09-01", Completed: true},
		{ID: 5, DueDate: ""},
	})
	if len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Errorf("asked about %v, want [1 5]", got)
	}
}
