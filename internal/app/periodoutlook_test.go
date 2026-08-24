package app

import (
	"strings"
	"testing"
)

// guards: outlookForPeriodEnd=100
func TestOutlookForPeriodEnd(t *testing.T) {
	// 10 a week for four weeks, ending at 40 on 2026-06-22.
	steady := series("2026-06-01", 10, 20, 30, 40)
	steadyForecast := forecastCompletion(steady, 40)
	// Slow overall, fast lately: 9 a week across the run, 16 a week now.
	uneven := series("2026-06-01", 0, 2, 4, 6, 36)
	unevenForecast := forecastCompletion(uneven, 36)

	cases := []struct {
		name     string
		end      string
		forecast completionForecast
		weeks    []rollupItemWeek
		progress int
		wantKind string
		wantLow  int
		wantHigh int
		noteHas  string
	}{
		{
			// No period at all. There is nothing to be short of.
			name: "no period end", end: "",
			forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: periodOutlookNone,
		},
		{
			// The projection runs from the last reported week, so a week whose
			// own date is unreadable leaves nothing to run from.
			name: "unreadable last week", end: "2026-09-30",
			forecast: steadyForecast, weeks: []rollupItemWeek{{WeekStart: "이번 주", Progress: 40}}, progress: 40,
			wantKind: periodOutlookNone,
		},
		{
			// Three weeks left and neither pace arrives, but they disagree about
			// how far short — which is the number worth printing.
			name: "short by different amounts", end: "2026-07-20",
			forecast: unevenForecast, weeks: uneven, progress: 36,
			wantKind: periodOutlookShort, noteHas: "기간 말(2026-07-20)에",
		},
		{
			// A period end that will not parse. Whatever wrote it was wrong; the
			// screen must not turn that into a claim about the work.
			name: "unparseable period end", end: "2026-13-45",
			forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: periodOutlookNone,
		},
		{
			name: "lands well inside the quarter", end: "2026-09-30",
			forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: periodOutlookLands, wantLow: 100, wantHigh: 100,
		},
		{
			// Three weeks of runway at 10 a week is 30 points, and 70 is not 100.
			name: "still running when the period closes", end: "2026-07-13",
			forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: periodOutlookShort, wantLow: 70, wantHigh: 70, noteHas: "70%",
		},
		{
			// Five weeks left. The recent sprint clears it, the run average does
			// not, and which holds is exactly what nobody knows.
			name: "depends which pace holds", end: "2026-08-03",
			forecast: unevenForecast, weeks: uneven, progress: 36,
			wantKind: periodOutlookSplit, wantLow: 81, wantHigh: 100, noteHas: "빠른 쪽",
		},
		{
			// The quarter is over. What happened is a fact, and a projection of
			// a settled period is noise dressed as a forecast.
			name: "period already closed", end: "2026-06-15",
			forecast: steadyForecast, weeks: steady, progress: 40,
			wantKind: periodOutlookNone,
		},
		{
			name: "already finished", end: "2026-09-30",
			forecast: steadyForecast, weeks: steady, progress: 100,
			wantKind: periodOutlookFinished, wantLow: 100, wantHigh: 100,
		},
		{
			// Two weeks cannot support a pace, so they cannot support a verdict
			// about the period either.
			name: "not enough history", end: "2026-09-30",
			forecast: forecastCompletion(series("2026-06-01", 10, 20), 20),
			weeks:    series("2026-06-01", 10, 20), progress: 20,
			wantKind: periodOutlookNone,
		},
	}
	for _, item := range cases {
		got := outlookForPeriodEnd(item.end, item.forecast, item.weeks, item.progress)
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

// The whole point: this answers the deadline question for work that has no
// deadline. Zero of eight work items had one, so an outlook that needed one
// would have nothing to say about any of them.
func TestPeriodOutlookNeedsNoDeadline(t *testing.T) {
	weeks := series("2026-06-01", 10, 20, 30, 40)
	item := rollupItem{Progress: 40, Weeks: weeks}
	item.Forecast = forecastCompletion(weeks, item.Progress)
	item.PeriodOutlook = outlookForPeriodEnd("2026-07-13", item.Forecast, weeks, item.Progress)

	if item.PeriodOutlook.Kind == periodOutlookNone {
		t.Fatal("nothing was said about an item with four weeks of history")
	}
	if item.PeriodOutlook.PeriodEnd != "2026-07-13" {
		t.Errorf("the boundary it measured against is not reported back: %q", item.PeriodOutlook.PeriodEnd)
	}
	if !missesThePeriod(item) {
		t.Errorf("70%% by the close should be reported: %+v", item.PeriodOutlook)
	}
}

// A risk list that names the same task under two headings is one that stops
// being read. Work whose numbers name no finishing week at all is already
// reported as 완료 시점 불명; this heading is for work that does finish, just
// after the report closes.
// guards: missesThePeriod
func TestPeriodMissDoesNotRepeatWhatNoLandingAlreadySays(t *testing.T) {
	crawling := rollupItem{Progress: 4, Weeks: series("2026-06-01", 1, 2, 3, 4)}
	crawling.Forecast = forecastCompletion(crawling.Weeks, crawling.Progress)
	crawling.PeriodOutlook = outlookForPeriodEnd("2026-09-30", crawling.Forecast, crawling.Weeks, crawling.Progress)
	if !noLandingDate(crawling) {
		t.Fatal("a task needing over a year should be reported by the no-landing rule")
	}
	if missesThePeriod(crawling) {
		t.Error("it is already reported under another heading and must not be listed twice")
	}

	done := rollupItem{Progress: 100, Completed: true, Weeks: series("2026-06-01", 60, 80, 100)}
	done.Forecast = forecastCompletion(done.Weeks, done.Progress)
	done.PeriodOutlook = outlookForPeriodEnd("2026-09-30", done.Forecast, done.Weeks, done.Progress)
	if missesThePeriod(done) {
		t.Error("finished work has nothing left to miss")
	}
}
