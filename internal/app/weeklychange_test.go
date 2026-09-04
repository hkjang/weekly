package app

import (
	"strings"
	"testing"
)

func changeItem(title string, first string, silent int, stalled int, completed bool, weeks ...workItemWeek) workItemView {
	item := workItemView{ID: 1, Title: title, FirstWeek: first, Weeks: weeks,
		ReportedWeeks: len(weeks), SilentWeeks: silent, StalledWeeks: stalled, Completed: completed}
	if len(weeks) > 0 {
		item.Progress = weeks[len(weeks)-1].Progress
	}
	return item
}

// Every branch of the classification, in the order of newsworthiness that
// decides which one a task falls into.
//
// guards: classifyWeeklyChange
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
		// The threshold itself: StallWeeks is how many weeks count as a stall,
		// not how many it has to exceed.
		{"same figure for exactly as long as the rule says", changeItem("A", last, 0, 2, false,
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
		if got := classifyWeeklyChange(item.item, now, cfg); got.Kind != item.want {
			t.Errorf("%s: got=%s want=%s note=%q", item.name, got.Kind, item.want, got.Note)
		}
	}
}

// The classification asks for the seven days, not for the date they are filed
// under, so moving the week start weekday does not rewrite a team's history.
//
// Every case here is a week the old exact-date lookup answered wrongly, and
// they fail in different directions: the transition week itself reported no
// work at all, and the week after it reported months-old tasks as newly begun
// or resumed, because the snapshot they should have been compared against sat
// two days off the new grid. The rest hold the edges: the window for the week
// before is that week and nothing older, so a task genuinely last seen five
// weeks ago is still 재개 and not ordinary progress; and a grid that moves
// backwards leaves two snapshots over the same seven days, where the later one
// is the answer.
//
// guards: classifyWeeklyChange, snapshotFor, snapshotPrior
func TestClassifyWeeklyChangeAfterTheGridMoves(t *testing.T) {
	cfg := defaultRollupConfig()
	// Monday moved to Wednesday. The transition week begins on 08-12 and the
	// report covering those days was filed under 08-10; the week after it
	// begins on 08-19 and is written on the new grid.
	const before, moved, after = "2026-08-03", "2026-08-10", "2026-08-19"
	cases := []struct {
		name string
		week string
		item workItemView
		want changeKind
	}{
		{"the transition week reads the report covering its days", "2026-08-12",
			changeItem("A", before, 0, 0, false,
				workItemWeek{WeekStart: before, Progress: 40},
				workItemWeek{WeekStart: moved, Progress: 60}), changeProgressed},
		{"a task first reported in the transition week is new, not resumed", "2026-08-12",
			changeItem("A", moved, 0, 0, false,
				workItemWeek{WeekStart: moved, Progress: 10}), changeNew},
		{"the week after reads the transition week as the week before", after,
			changeItem("A", before, 0, 0, false,
				workItemWeek{WeekStart: moved, Progress: 60},
				workItemWeek{WeekStart: after, Progress: 75}), changeProgressed},
		{"work missing from the week after is still silent", after,
			changeItem("A", before, 0, 0, false,
				workItemWeek{WeekStart: moved, Progress: 60}), changeSilent},
		{"a task last seen five weeks ago has resumed", moved,
			changeItem("A", "2026-07-06", 4, 0, false,
				workItemWeek{WeekStart: "2026-07-06", Progress: 30},
				workItemWeek{WeekStart: moved, Progress: 40}), changeResumed},
		{"and one last seen five weeks ago that stayed put has too", moved,
			changeItem("A", "2026-07-06", 4, 0, false,
				workItemWeek{WeekStart: "2026-07-06", Progress: 40},
				workItemWeek{WeekStart: moved, Progress: 40}), changeResumed},
		// Wednesday moved back to Monday, so two snapshots overlap the week
		// beginning 08-10: the one filed under 08-05 and the one under 08-12.
		// The later is the report the author is writing now — reading the
		// earlier one instead makes a task that has been running since 08-05
		// look like it began this week.
		{"the later of two overlapping reports is this week's", moved,
			changeItem("A", "2026-08-05", 0, 0, false,
				workItemWeek{WeekStart: "2026-08-05", Progress: 20},
				workItemWeek{WeekStart: "2026-08-12", Progress: 55}), changeResumed},
		// And the same tie a week later, where both overlapping reports fall in
		// the week before: comparing against 08-06 instead of 08-12 turns a rise
		// of ten points into a fall of ten.
		{"the later of two overlapping reports is also last week's", after,
			changeItem("A", "2026-08-06", 0, 0, false,
				workItemWeek{WeekStart: "2026-08-06", Progress: 70},
				workItemWeek{WeekStart: "2026-08-12", Progress: 50},
				workItemWeek{WeekStart: after, Progress: 60}), changeProgressed},
	}
	for _, item := range cases {
		if got := classifyWeeklyChange(item.item, item.week, cfg); got.Kind != item.want {
			t.Errorf("%s: got=%s want=%s note=%q", item.name, got.Kind, item.want, got.Note)
		}
	}
	// The note carries how long the silence was, and a reader who is told a task
	// resumed without being told from what is no better off than before.
	quiet := changeItem("A", "2026-07-06", 4, 0, false,
		workItemWeek{WeekStart: "2026-07-06", Progress: 30},
		workItemWeek{WeekStart: moved, Progress: 40})
	if note := classifyWeeklyChange(quiet, moved, cfg).Note; !strings.Contains(note, "4주") {
		t.Errorf("resumed note = %q, want the number of quiet weeks", note)
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
