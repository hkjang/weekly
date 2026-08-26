package app

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Projecting when a task will finish, from the progress it has actually
// reported.
//
// The roadmap asked for a schedule risk forecast. There is no schedule to
// forecast against — work_items.due_date exists in the schema and nothing
// anywhere writes it — so this does not pretend to know a deadline. What it
// does is arithmetic on the reported series: at the pace this work has moved,
// how much longer does 100% take.
//
// Two rules keep it from becoming a fortune teller.
//
// It gives a range, never a point. The pace over the whole period and the pace
// over the recent weeks rarely agree, and the gap between them is the
// uncertainty. Collapsing them into one number and calling it a confidence
// score hides exactly the thing the reader needs. A wide range is the answer
// "nobody can tell yet", said out loud.
//
// And it declines to speak. Two data points draw a line through anything, so a
// task reported twice gets no estimate; work that has not moved gets no date at
// all, because "at this pace, never" is a fact about the past and a date would
// be an invention.

// forecastKind is what can be said about a task's finish, not how sure we are.
const (
	forecastDone         = "DONE"         // already at 100
	forecastStalled      = "STALLED"      // no forward movement: no date exists
	forecastInsufficient = "INSUFFICIENT" // too few reported weeks to say anything
	forecastProjected    = "PROJECTED"    // a range, with the pace it came from
	forecastDistant      = "DISTANT"      // beyond a year — the number would be false precision
)

// forecastMinWeeks is the shortest history that earns an estimate. Two points
// fit a straight line through any pair of numbers, which is how a task in its
// second week acquires a completion date.
const forecastMinWeeks = 3

// forecastHorizonWeeks is where a projection stops being a number. A year out,
// the difference between 61 and 74 weeks is noise dressed as precision.
const forecastHorizonWeeks = 52

type completionForecast struct {
	Kind string `json:"kind"`
	// EarliestWeeks and LatestWeeks bracket the remaining weeks. Equal when the
	// two paces agree.
	EarliestWeeks int `json:"earliestWeeks,omitempty"`
	LatestWeeks   int `json:"latestWeeks,omitempty"`
	// EarliestWeek and LatestWeek are those weeks as dates, so a reader can put
	// them next to a real calendar rather than counting.
	EarliestWeek string `json:"earliestWeek,omitempty"`
	LatestWeek   string `json:"latestWeek,omitempty"`
	// OverallPerWeek and RecentPerWeek are the two paces, in progress points per
	// week. They are the evidence: a reader who disagrees with the projection
	// can see the numbers it came from.
	OverallPerWeek float64 `json:"overallPerWeek"`
	RecentPerWeek  float64 `json:"recentPerWeek"`
	BasedOnWeeks   int     `json:"basedOnWeeks"`
	Note           string  `json:"note"`
}

// forecastCompletion reads the reported weeks and says what can be said.
//
// weeks must be the item's own series; it is sorted here rather than trusted,
// because a caller that assembles it from a map gets whatever order the map
// gives and the resulting pace would be nonsense with no sign of it.
func forecastCompletion(weeks []rollupItemWeek, progress int) completionForecast {
	if progress >= 100 {
		return completionForecast{Kind: forecastDone, Note: "완료됐습니다."}
	}
	series := append([]rollupItemWeek(nil), weeks...)
	sort.SliceStable(series, func(i, j int) bool { return series[i].WeekStart < series[j].WeekStart })
	if len(series) < forecastMinWeeks {
		// A short series that has not moved at all needs no extrapolation:
		// first equals last is something observed, not a rate inferred from too
		// few points. Answering "wait until three weeks" while the stall rule
		// on the same screen already says 정체 tells a reader to wait for news
		// they have been given.
		if len(series) >= 2 && series[len(series)-1].Progress <= series[0].Progress {
			return completionForecast{
				Kind:         forecastStalled,
				BasedOnWeeks: len(series),
				Note:         fmt.Sprintf("보고된 %d개 주차 동안 진척이 늘지 않았습니다. 이 속도로는 끝나는 시점이 없습니다.", len(series)),
			}
		}
		return completionForecast{
			Kind:         forecastInsufficient,
			BasedOnWeeks: len(series),
			Note:         fmt.Sprintf("보고된 주차가 %d주뿐이라 속도를 말할 수 없습니다. %d주가 쌓이면 추정합니다.", len(series), forecastMinWeeks),
		}
	}

	first, last := series[0], series[len(series)-1]

	// Pace is per week on the calendar, not per report.
	//
	// Dividing by the number of reports made a task that gained thirty points
	// over six weeks look like fifteen a week when only three of those weeks
	// were written down, and put its finish two weeks earlier than the same
	// work reported every week. Reporting less often is not going faster, and
	// this figure is what somebody plans a date against.
	//
	// Where every week was reported the two agree exactly, so an unbroken run
	// is unchanged. A series that cannot be read as dates falls back to the
	// report count rather than refusing to answer.
	overall := (float64(last.Progress) - float64(first.Progress)) / weekSpan(first.WeekStart, last.WeekStart, float64(len(series)-1))

	// The recent pace looks back three reports — enough to notice that work
	// restarted or stopped, short enough that a change of pace is not averaged
	// away by two good months — and again over the weeks they actually cover.
	recentFrom := series[len(series)-3]
	recent := (float64(last.Progress) - float64(recentFrom.Progress)) / weekSpan(recentFrom.WeekStart, last.WeekStart, 2)

	remaining := float64(100 - progress)
	weeksAt := func(pace float64) (int, bool) {
		if pace <= 0 {
			return 0, false
		}
		return int(math.Ceil(remaining / pace)), true
	}
	fastWeeks, fastOK := weeksAt(math.Max(overall, recent))
	slowWeeks, slowOK := weeksAt(math.Min(overall, recent))

	result := completionForecast{
		OverallPerWeek: round1(overall),
		RecentPerWeek:  round1(recent),
		BasedOnWeeks:   len(series),
	}
	if !fastOK {
		// Neither pace is forward. Saying "60주 뒤" from a negative slope would
		// be arithmetic on a number that means the opposite.
		result.Kind = forecastStalled
		result.Note = fmt.Sprintf("최근 %d주 동안 진척이 늘지 않았습니다. 이 속도로는 끝나는 시점이 없습니다.", len(series))
		return result
	}
	if fastWeeks > forecastHorizonWeeks {
		result.Kind = forecastDistant
		result.Note = fmt.Sprintf("주당 %.1f%%씩이면 남은 %d%%에 1년이 넘게 걸립니다. 주 단위로 세는 것은 의미가 없습니다.", math.Max(overall, recent), 100-progress)
		return result
	}

	result.Kind = forecastProjected
	result.EarliestWeeks = fastWeeks
	result.EarliestWeek = shiftISOWeek(last.WeekStart, fastWeeks)
	if !slowOK || slowWeeks > forecastHorizonWeeks {
		// One pace says it lands, the other says it does not. That disagreement
		// is the finding, and a single upper bound would erase it.
		result.LatestWeeks = 0
		result.LatestWeek = ""
		result.Note = fmt.Sprintf("전체 평균으로는 %d주 뒤지만, 두 속도가 엇갈립니다(전체 %.1f%%/주, 최근 %.1f%%/주). 최근 속도로는 끝나는 시점이 없습니다.",
			fastWeeks, round1(overall), round1(recent))
		return result
	}
	result.LatestWeeks = slowWeeks
	result.LatestWeek = shiftISOWeek(last.WeekStart, slowWeeks)
	if fastWeeks == slowWeeks {
		result.Note = fmt.Sprintf("두 속도가 같습니다(%.1f%%/주). 이대로면 %d주 뒤입니다.", round1(overall), fastWeeks)
		return result
	}
	result.Note = fmt.Sprintf("전체 %.1f%%/주, 최근 %.1f%%/주. 이 사이 속도가 유지되면 %d~%d주 뒤입니다.",
		round1(overall), round1(recent), fastWeeks, slowWeeks)
	return result
}

// shiftISOWeek moves a yyyy-mm-dd week start forward by whole weeks. An
// unparseable input returns empty rather than a wrong date, because a
// projection labelled with the wrong Monday is worse than one with no label.
// weekSpan is how many weeks lie between two week starts, falling back to the
// caller's count when either date cannot be read. Never below one: two reports
// in the same week would otherwise divide by zero and call any gain infinite.
func weekSpan(from, to string, fallback float64) float64 {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return math.Max(fallback, 1)
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return math.Max(fallback, 1)
	}
	weeks := end.Sub(start).Hours() / (24 * 7)
	return math.Max(math.Round(weeks), 1)
}

func shiftISOWeek(weekStart string, weeks int) string {
	parsed, err := time.Parse("2006-01-02", weekStart)
	if err != nil {
		return ""
	}
	return parsed.AddDate(0, 0, weeks*7).Format("2006-01-02")
}
