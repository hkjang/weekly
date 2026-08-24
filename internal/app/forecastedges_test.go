package app

import (
	"testing"
)

// guards: noLandingDate=100
//
// scripts/mutation-check.py: turning the || in this exclusion into && lets a
// finished task onto the "no landing date" list, and nothing noticed. A risk
// list that names completed work stops being read, which is the same failure
// the stalled exclusion above it exists to prevent.
func TestNothingFinishedOrStalledIsCalledUnlandable(t *testing.T) {
	distant := completionForecast{Kind: forecastDistant}
	openEnded := completionForecast{Kind: forecastProjected, LatestWeeks: 0}
	landing := completionForecast{Kind: forecastProjected, LatestWeeks: 6}

	cases := []struct {
		name string
		item rollupItem
		want bool
	}{
		// The positive controls. Without these a false everywhere would pass.
		{"끝날 주차를 못 잡는 진행 업무", rollupItem{Forecast: distant}, true},
		{"추정은 되지만 상한이 없는 업무", rollupItem{Forecast: openEnded}, true},

		{"완료된 업무", rollupItem{Completed: true, Forecast: distant}, false},
		{"완료됐고 정체도 아닌 업무", rollupItem{Completed: true, Forecast: openEnded}, false},
		// Stalled work is already reported by a rule that says something more
		// specific; naming it twice is how a list stops being read.
		{"정체된 업무", rollupItem{Stalled: true, Forecast: distant}, false},
		{"완료이면서 정체", rollupItem{Completed: true, Stalled: true, Forecast: distant}, false},

		{"끝날 주차가 잡히는 업무", rollupItem{Forecast: landing}, false},
	}
	for _, item := range cases {
		if got := noLandingDate(item.item); got != item.want {
			t.Errorf("%s: noLandingDate=%v, want %v", item.name, got, item.want)
		}
	}
}

// guards: outlookForDueDate
//
// The week the deadline lands in is the boundary between "here is a projection"
// and "this is late". Reversing the comparison moves a deadline that has just
// arrived back into the projection branch, and the screen offers a forecast for
// a date that is already here.
func TestADeadlineThatHasArrivedIsStatedNotProjected(t *testing.T) {
	// Four weeks of reports ending 2026-06-22, sitting at 40.
	weeks := series("2026-06-01", 10, 20, 30, 40)
	forecast := forecastCompletion(weeks, 40)

	// The deadline falls on the last reported week: zero weeks left.
	arrived := outlookForDueDate("2026-06-22", forecast, weeks, 40)
	if arrived.Kind != dueOutlookOverdue {
		t.Errorf("a deadline on the last reported week is %q, want %q — %s",
			arrived.Kind, dueOutlookOverdue, arrived.Note)
	}
	if arrived.ProjectedLow != 40 || arrived.ProjectedHigh != 40 {
		t.Errorf("an arrived deadline projected %d~%d instead of stating the observed 40",
			arrived.ProjectedLow, arrived.ProjectedHigh)
	}

	// One week past it is the same answer, and one week before it is not.
	if past := outlookForDueDate("2026-06-15", forecast, weeks, 40); past.Kind != dueOutlookOverdue {
		t.Errorf("a deadline already behind us is %q, want %q", past.Kind, dueOutlookOverdue)
	}
	ahead := outlookForDueDate("2026-08-10", forecast, weeks, 40)
	if ahead.Kind == dueOutlookOverdue {
		t.Errorf("a deadline seven weeks out was called overdue: %s", ahead.Note)
	}
}

// guards: outlookForPeriodEnd
func TestAClosedPeriodIsNotForecast(t *testing.T) {
	weeks := series("2026-06-01", 10, 20, 30, 40)
	forecast := forecastCompletion(weeks, 40)

	if closed := outlookForPeriodEnd("2026-06-22", forecast, weeks, 40); closed.Kind != periodOutlookNone {
		t.Errorf("a period ending on the last reported week is %q, want %q — %s",
			closed.Kind, periodOutlookNone, closed.Note)
	}
	if behind := outlookForPeriodEnd("2026-06-08", forecast, weeks, 40); behind.Kind != periodOutlookNone {
		t.Errorf("a period that closed weeks ago is %q, want %q", behind.Kind, periodOutlookNone)
	}
	// Positive control: a period still open does get an answer.
	if open := outlookForPeriodEnd("2026-09-30", forecast, weeks, 40); open.Kind == periodOutlookNone {
		t.Error("an open period produced no outlook at all, so the refusals above prove nothing")
	}
}
