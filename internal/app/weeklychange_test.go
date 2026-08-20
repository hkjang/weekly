package app

import "testing"

func changeItem(title string, first string, silent int, stalled int, completed bool, weeks ...workItemWeek) workItemView {
	item := workItemView{ID: 1, Title: title, FirstWeek: first, Weeks: weeks,
		ReportedWeeks: len(weeks), SilentWeeks: silent, StalledWeeks: stalled, Completed: completed}
	if len(weeks) > 0 {
		item.Progress = weeks[len(weeks)-1].Progress
	}
	return item
}

func TestClassifyWeeklyChange(t *testing.T) {
	cfg := defaultRollupConfig()
	const last, now = "2026-08-10", "2026-08-17"
	cases := []struct {
		name string
		item workItemView
		want changeKind
	}{
		{"reported in neither week", changeItem("A", "2026-07-06", 0, 0, false,
			workItemWeek{WeekStart: "2026-07-06", Progress: 10}), changeAbsent},
		{"first ever report", changeItem("A", now, 0, 0, false,
			workItemWeek{WeekStart: now, Progress: 10}), changeNew},
		// The case that used to fall through every branch and disappear.
		{"quiet for a week, then back", changeItem("A", "2026-07-27", 1, 0, false,
			workItemWeek{WeekStart: "2026-07-27", Progress: 30},
			workItemWeek{WeekStart: now, Progress: 40}), changeResumed},
		{"reached 100", changeItem("A", last, 0, 0, true,
			workItemWeek{WeekStart: last, Progress: 80},
			workItemWeek{WeekStart: now, Progress: 100}), changeCompleted},
		// Completing outranks resuming: what changes anyone's next move is that
		// the work is done, not that it was quiet.
		{"quiet, then back and finished", changeItem("A", "2026-07-27", 1, 0, true,
			workItemWeek{WeekStart: "2026-07-27", Progress: 60},
			workItemWeek{WeekStart: now, Progress: 100}), changeCompleted},
		{"moved forward", changeItem("A", last, 0, 0, false,
			workItemWeek{WeekStart: last, Progress: 40},
			workItemWeek{WeekStart: now, Progress: 55}), changeProgressed},
		{"reported lower than last week", changeItem("A", last, 0, 0, false,
			workItemWeek{WeekStart: last, Progress: 70},
			workItemWeek{WeekStart: now, Progress: 50}), changeRegressed},
		{"same figure long enough to count", changeItem("A", "2026-08-03", 0, 3, false,
			workItemWeek{WeekStart: "2026-08-03", Progress: 40},
			workItemWeek{WeekStart: last, Progress: 40},
			workItemWeek{WeekStart: now, Progress: 40}), changeStalled},
		// Unchanged but not yet long enough to be worth saying.
		{"same figure, one week only", changeItem("A", last, 0, 1, false,
			workItemWeek{WeekStart: last, Progress: 40},
			workItemWeek{WeekStart: now, Progress: 40}), changeSteady},
		{"open last week, gone this week", changeItem("A", last, 0, 0, false,
			workItemWeek{WeekStart: last, Progress: 40}), changeSilent},
		// Finished work stops being reported. That is not a gap in reporting.
		{"finished last week, gone this week", changeItem("A", last, 0, 0, true,
			workItemWeek{WeekStart: last, Progress: 100}), changeAbsent},
	}
	for _, item := range cases {
		if got := classifyWeeklyChange(item.item, now, last, cfg); got.Kind != item.want {
			t.Errorf("%s: got=%s want=%s note=%q", item.name, got.Kind, item.want, got.Note)
		}
	}
}

func TestBuildChangeSummaryCountsSteadyButNeverListsIt(t *testing.T) {
	cfg := defaultRollupConfig()
	const last, now = "2026-08-10", "2026-08-17"
	steady := changeItem("계속 진행", last, 0, 1, false,
		workItemWeek{WeekStart: last, Progress: 40}, workItemWeek{WeekStart: now, Progress: 40})
	moved := changeItem("진척", last, 0, 0, false,
		workItemWeek{WeekStart: last, Progress: 40}, workItemWeek{WeekStart: now, Progress: 60})
	moved.ID = 2
	elsewhere := changeItem("다른 기간", "2026-06-01", 0, 0, false,
		workItemWeek{WeekStart: "2026-06-01", Progress: 10})
	elsewhere.ID = 3

	view := buildChangeSummary([]workItemView{steady, moved, elsewhere}, now, cfg)
	if view.Reported != 2 || view.Changed != 1 {
		t.Fatalf("reported=%d changed=%d want 2 and 1", view.Reported, view.Changed)
	}
	if len(view.Groups) != len(changeGroupOrder) {
		t.Fatalf("groups=%d want=%d", len(view.Groups), len(changeGroupOrder))
	}
	// Every group is present even when empty, so a screen can show a stable set
	// of counts instead of a list that changes shape every week.
	listed := 0
	for _, group := range view.Groups {
		listed += len(group.Entries)
		if group.Count != len(group.Entries) {
			t.Errorf("%s: count=%d entries=%d", group.Kind, group.Count, len(group.Entries))
		}
		// Never nil: a nil slice encodes as null and makes every caller check
		// each group before iterating it.
		if group.Entries == nil {
			t.Errorf("%s: entries is nil", group.Kind)
		}
	}
	if listed != 1 {
		t.Fatalf("listed=%d want=1", listed)
	}
	if view.PreviousWeek != last {
		t.Fatalf("previousWeek=%q want=%q", view.PreviousWeek, last)
	}
}
