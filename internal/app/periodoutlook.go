package app

import (
	"fmt"
	"math"
	"time"
)

// Asking about the deadline that already exists.
//
// v0.48 through v0.51 built completion estimates, a deadline field, a way to
// adopt a date a meeting had agreed, and a place for the result in the
// executive digest. All of it answers one question — will this land in time —
// and all of it is gated on work_items.due_date, which measured at zero of
// eight filled. Four releases of arithmetic waiting on a column nobody has a
// reason to type into.
//
// But a period report is already asking the question. Somebody opening the
// quarterly rollup wants to know what finishes this quarter; the boundary is
// right there in the request, agreed by the calendar rather than by a person.
// So the same arithmetic runs against it, and nobody has to enter anything.
//
// This is not a commitment and does not pretend to be one. A deadline says
// somebody promised; a period end says the report stops here. The wording keeps
// them apart, and an item with a real deadline still carries its own outlook
// beside this one.

const (
	periodOutlookNone     = "NONE"     // nothing to say: the period is over, or there is no history
	periodOutlookLands    = "LANDS"    // both paces reach 100 before the period ends
	periodOutlookSplit    = "SPLIT"    // one pace makes it, the other does not
	periodOutlookShort    = "SHORT"    // neither pace reaches 100 in the period
	periodOutlookFinished = "FINISHED" // already complete
)

type periodOutlook struct {
	Kind string `json:"kind"`
	// PeriodEnd is the boundary this was measured against, so a reader can see
	// the projection is about the report's own window and not a promise.
	PeriodEnd string `json:"periodEnd,omitempty"`
	WeeksLeft int    `json:"weeksLeft"`
	// ProjectedLow and ProjectedHigh are where the two paces put this work when
	// the period closes, capped at 100.
	ProjectedLow  int    `json:"projectedLow"`
	ProjectedHigh int    `json:"projectedHigh"`
	Note          string `json:"note"`
}

// outlookForPeriodEnd projects each item to the close of the reporting period.
//
// Nothing is projected for a period that has already ended. The result is known
// by then, and an estimate of a settled quarter is not a forecast, it is noise
// dressed as one.
func outlookForPeriodEnd(periodEnd string, forecast completionForecast, weeks []rollupItemWeek, progress int) periodOutlook {
	if periodEnd == "" {
		return periodOutlook{Kind: periodOutlookNone}
	}
	end, err := time.Parse("2006-01-02", periodEnd)
	if err != nil {
		return periodOutlook{Kind: periodOutlookNone}
	}
	result := periodOutlook{PeriodEnd: periodEnd}
	if progress >= 100 {
		result.Kind = periodOutlookFinished
		result.ProjectedLow, result.ProjectedHigh = 100, 100
		return result
	}
	// Nothing to project from. A stalled task has no forward pace to extend —
	// running one anyway produced "기간 말에 15%" for work sitting at 25%,
	// which reads as a forecast and is really a report that it went backwards.
	// The stall rule says that already, and says it better.
	if len(weeks) == 0 || forecast.Kind == forecastInsufficient || forecast.Kind == forecastStalled {
		result.Kind = periodOutlookNone
		result.Note = forecast.Note
		return result
	}

	// Measured from the last reported week, like every other projection here: a
	// task last written up a month ago has not been moving since, and running
	// its pace forward from today would credit it with weeks it never worked.
	last, err := time.Parse("2006-01-02", weeks[len(weeks)-1].WeekStart)
	if err != nil {
		return periodOutlook{Kind: periodOutlookNone}
	}
	weeksLeft := int(end.Sub(last).Hours() / (24 * 7))
	if weeksLeft <= 0 {
		// The period has closed. What happened is a fact now, and this has
		// nothing to add to it.
		return periodOutlook{Kind: periodOutlookNone}
	}
	result.WeeksLeft = weeksLeft

	project := func(pace float64) int {
		return int(math.Min(100, math.Max(0, float64(progress)+pace*float64(weeksLeft))))
	}
	low := project(math.Min(forecast.OverallPerWeek, forecast.RecentPerWeek))
	high := project(math.Max(forecast.OverallPerWeek, forecast.RecentPerWeek))
	result.ProjectedLow, result.ProjectedHigh = low, high

	switch {
	case low >= 100:
		result.Kind = periodOutlookLands
		result.Note = fmt.Sprintf("기간 말까지 %d주 남았고, 두 속도 모두 그 안에 100%%에 닿습니다.", weeksLeft)
	case high >= 100:
		// Which pace holds decides the answer, and that disagreement is the
		// finding. One verdict here would pick a side and hide the other.
		fast, slow := math.Max(forecast.OverallPerWeek, forecast.RecentPerWeek), math.Min(forecast.OverallPerWeek, forecast.RecentPerWeek)
		result.Kind = periodOutlookSplit
		result.Note = fmt.Sprintf("기간 말까지 %d주. 빠른 쪽 속도(%.1f%%/주)로는 닿지만 느린 쪽(%.1f%%/주)으로는 %d%%에 그칩니다.",
			weeksLeft, round1(fast), round1(slow), low)
	default:
		result.Kind = periodOutlookShort
		if low == high {
			result.Note = fmt.Sprintf("이 속도(%.1f%%/주)로는 기간 말(%s)에 %d%%입니다.", round1(forecast.OverallPerWeek), periodEnd, low)
		} else {
			result.Note = fmt.Sprintf("기간 말(%s)에 %d~%d%%입니다. 전체 %.1f%%/주, 최근 %.1f%%/주.",
				periodEnd, low, high, round1(forecast.OverallPerWeek), round1(forecast.RecentPerWeek))
		}
	}
	return result
}

// missesThePeriod is work that will still be running when the report closes.
//
// Deliberately narrower than it could be. Work whose own numbers name no
// finishing week at all is already reported by 완료 시점 불명, and a risk list
// that names the same task under two headings is one that stops being read. So
// only the items with a real projection that simply lands too late are counted
// here — a different sentence to a reader: this one finishes, just not inside
// the quarter you are reporting on.
func missesThePeriod(item rollupItem) bool {
	if item.Completed || noLandingDate(item) {
		return false
	}
	return item.PeriodOutlook.Kind == periodOutlookShort
}
