package app

import (
	"strings"
	"testing"
)

func series(startWeek string, progress ...int) []rollupItemWeek {
	weeks := make([]rollupItemWeek, len(progress))
	for index, value := range progress {
		weeks[index] = rollupItemWeek{WeekStart: shiftISOWeek(startWeek, index), Progress: value}
	}
	return weeks
}

func TestForecastCompletion(t *testing.T) {
	cases := []struct {
		name          string
		weeks         []rollupItemWeek
		progress      int
		wantKind      string
		wantEarliest  int
		wantLatest    int
		wantNoteHas   string
		wantEarlyWeek string
	}{
		{
			// 10 a week, all the way. Both paces agree, so the range collapses
			// and there is nothing to hedge about.
			name: "steady pace", weeks: series("2026-06-01", 10, 20, 30, 40), progress: 40,
			wantKind: forecastProjected, wantEarliest: 6, wantLatest: 6,
			wantEarlyWeek: "2026-08-03", wantNoteHas: "두 속도가 같습니다",
		},
		{
			// Slow start, recent sprint. The reader is shown both, and the gap
			// between 4 and 12 weeks is the point.
			name: "recently accelerated", weeks: series("2026-06-01", 0, 5, 10, 40, 60), progress: 60,
			wantKind: forecastProjected, wantEarliest: 2, wantLatest: 3,
			wantNoteHas: "이 사이 속도가 유지되면",
		},
		{
			// Nothing has moved. A date here would be invented, so there is none.
			name: "flat", weeks: series("2026-06-01", 40, 40, 40, 40), progress: 40,
			wantKind: forecastStalled, wantNoteHas: "끝나는 시점이 없습니다",
		},
		{
			// Reported progress went backwards. The average slope is negative,
			// and arithmetic on it would produce a date in the past.
			name: "regressed", weeks: series("2026-06-01", 60, 50, 40), progress: 40,
			wantKind: forecastStalled,
		},
		{
			// Moved once early and stopped. Overall says it lands, recent says
			// never; the disagreement is reported instead of averaged away.
			name: "moved then stopped", weeks: series("2026-06-01", 0, 30, 30, 30), progress: 30,
			wantKind: forecastProjected, wantEarliest: 7, wantLatest: 0,
			wantNoteHas: "두 속도가 엇갈립니다",
		},
		{
			// One point per week for four weeks: 96 points left is over a year.
			name: "crawling", weeks: series("2026-06-01", 1, 2, 3, 4), progress: 4,
			wantKind: forecastDistant, wantNoteHas: "1년이 넘게",
		},
		{
			// Two weeks fit a line through any pair of numbers.
			name: "two weeks", weeks: series("2026-06-01", 10, 60), progress: 60,
			wantKind: forecastInsufficient, wantNoteHas: "3주가 쌓이면",
		},
		{
			name: "finished", weeks: series("2026-06-01", 80, 100), progress: 100,
			wantKind: forecastDone,
		},
	}
	for _, item := range cases {
		got := forecastCompletion(item.weeks, item.progress)
		if got.Kind != item.wantKind {
			t.Errorf("%s: kind=%s want=%s (%s)", item.name, got.Kind, item.wantKind, got.Note)
			continue
		}
		if item.wantEarliest != 0 && got.EarliestWeeks != item.wantEarliest {
			t.Errorf("%s: earliest=%d want=%d (%s)", item.name, got.EarliestWeeks, item.wantEarliest, got.Note)
		}
		if item.wantKind == forecastProjected && got.LatestWeeks != item.wantLatest {
			t.Errorf("%s: latest=%d want=%d (%s)", item.name, got.LatestWeeks, item.wantLatest, got.Note)
		}
		if item.wantEarlyWeek != "" && got.EarliestWeek != item.wantEarlyWeek {
			t.Errorf("%s: earliest week=%s want=%s", item.name, got.EarliestWeek, item.wantEarlyWeek)
		}
		if item.wantNoteHas != "" && !strings.Contains(got.Note, item.wantNoteHas) {
			t.Errorf("%s: note %q does not say %q", item.name, got.Note, item.wantNoteHas)
		}
	}
}

// The estimate must not depend on the order the caller happened to assemble the
// weeks in. Built from a map, the series arrives shuffled, and a pace computed
// from first-and-last of a shuffled slice is a different number every run.
func TestForecastDoesNotTrustTheCallersOrdering(t *testing.T) {
	ordered := series("2026-06-01", 10, 20, 30, 40)
	shuffled := []rollupItemWeek{ordered[2], ordered[0], ordered[3], ordered[1]}
	if forecastCompletion(shuffled, 40) != forecastCompletion(ordered, 40) {
		t.Errorf("shuffled=%+v\nordered=%+v", forecastCompletion(shuffled, 40), forecastCompletion(ordered, 40))
	}
}

// Every projection has to carry the numbers it came from. A week count with no
// pace beside it is a score, and the roadmap ruled those out for this feature.
func TestEveryProjectionShowsThePaceItCameFrom(t *testing.T) {
	for _, weeks := range [][]rollupItemWeek{
		series("2026-06-01", 10, 20, 30, 40),
		series("2026-06-01", 0, 5, 10, 40, 60),
		series("2026-06-01", 0, 30, 30, 30),
	} {
		got := forecastCompletion(weeks, weeks[len(weeks)-1].Progress)
		if got.Kind != forecastProjected {
			t.Fatalf("expected a projection, got %s", got.Kind)
		}
		if got.OverallPerWeek == 0 && got.RecentPerWeek == 0 {
			t.Errorf("%+v carries no pace", got)
		}
		if got.BasedOnWeeks != len(weeks) {
			t.Errorf("basedOnWeeks=%d want=%d", got.BasedOnWeeks, len(weeks))
		}
	}
}

// A task creeping forward is not a stalled task, and the two findings must not
// both claim it. Naming the same work twice under two headings is how a risk
// list stops being read.
func TestNoLandingDateDoesNotRepeatWhatStalledAlreadySays(t *testing.T) {
	creeping := rollupItem{Progress: 4, Weeks: series("2026-06-01", 1, 2, 3, 4)}
	creeping.Forecast = forecastCompletion(creeping.Weeks, creeping.Progress)
	if !noLandingDate(creeping) {
		t.Errorf("a task needing over a year should be reported: %+v", creeping.Forecast)
	}

	frozen := rollupItem{Progress: 40, Stalled: true, Weeks: series("2026-06-01", 40, 40, 40, 40)}
	frozen.Forecast = forecastCompletion(frozen.Weeks, frozen.Progress)
	if noLandingDate(frozen) {
		t.Error("stalled work is already reported by its own rule and must not be listed twice")
	}

	done := rollupItem{Progress: 100, Completed: true, Weeks: series("2026-06-01", 1, 2, 3, 100)}
	done.Forecast = forecastCompletion(done.Weeks, done.Progress)
	if noLandingDate(done) {
		t.Error("finished work has nothing left to land")
	}
}

// The whole point of this finding: it catches work that every status board
// shows as healthy. Progress moves every single week and the item is neither
// stalled nor flagged by an issue, yet its own numbers need a year.
func TestNoLandingCatchesWorkThatLooksHealthy(t *testing.T) {
	weeks := series("2026-06-01", 1, 2, 3, 4, 5, 6)
	item := rollupItem{Progress: 6, Weeks: weeks}
	item.Stalled = isStalled(weeks, defaultRollupConfig().StallWeeks)
	item.Forecast = forecastCompletion(weeks, item.Progress)
	if item.Stalled {
		t.Fatal("progress moves every week, so the stall rule should not fire")
	}
	if item.AtRisk {
		t.Fatal("no issue was reported, so the issue rule should not fire")
	}
	if !noLandingDate(item) {
		t.Fatalf("nothing else reports this work: %+v", item.Forecast)
	}
}

// A two-week series that has not moved is not "not enough data". First equals
// last is observed, not extrapolated, and the stall rule on the same screen is
// already saying 정체 — telling the reader to wait for an estimate is telling
// them to wait for news they have.
func TestShortFlatSeriesSaysStalledRatherThanAskingForMoreWeeks(t *testing.T) {
	flat := forecastCompletion(series("2026-06-01", 60, 60), 60)
	if flat.Kind != forecastStalled {
		t.Errorf("kind=%s want=%s (%s)", flat.Kind, forecastStalled, flat.Note)
	}
	if strings.Contains(flat.Note, "쌓이면") {
		t.Errorf("asking for weeks that would not change the answer: %q", flat.Note)
	}

	// Moving, but only two points: a rate here is a line through any two
	// numbers, so this one still declines.
	moving := forecastCompletion(series("2026-06-01", 10, 60), 60)
	if moving.Kind != forecastInsufficient {
		t.Errorf("kind=%s want=%s (%s)", moving.Kind, forecastInsufficient, moving.Note)
	}

	// One report is not a series. Nothing has been observed to be flat yet.
	single := forecastCompletion(series("2026-06-01", 40), 40)
	if single.Kind != forecastInsufficient {
		t.Errorf("kind=%s want=%s (%s)", single.Kind, forecastInsufficient, single.Note)
	}
}
