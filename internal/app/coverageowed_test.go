package app

import (
	"testing"
	"time"
)

// 보고 커버리지 counts weeks somebody was owed, not weeks the calendar contains.
//
// A period that includes the present touches weeks that have not happened.
// Counting those turned the figure into "how much of the calendar has gone by",
// dressed as a data-quality warning: measured on 2026-08-29, 2026-Q3 read
// "14개 보고 주차 중 9개 · 64.3%" with the sentence that a low coverage makes the
// whole aggregate less trustworthy — while every week that had happened was
// reported. Five of the missing weeks were September.
//
// guards: expectedWeekStarts
func TestCoverageDoesNotCountWeeksNobodyCouldHaveReported(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skip("Asia/Seoul is not available")
	}
	// Saturday 2026-08-29: the week starting 2026-08-31 has not begun, and the
	// week starting 2026-08-24 is not due until 2026-08-31 24:00.
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, seoul)
	// The week that has started is the last one the aggregate can contain.
	owed := currentWeekStart(now, "MONDAY")
	if got := owed.Format("2006-01-02"); got != "2026-08-24" {
		t.Errorf("시작한 마지막 주차가 %s 입니다, 2026-08-24 를 기대합니다", got)
	}

	for _, item := range []struct {
		kind, period string
		want         []string
	}{
		{periodMonth, "2026-08", []string{"2026-07-27", "2026-08-03", "2026-08-10", "2026-08-17", "2026-08-24"}},
		// A period entirely in the past is unchanged: every week is owed.
		{periodMonth, "2026-06", []string{"2026-06-01", "2026-06-08", "2026-06-15", "2026-06-22", "2026-06-29"}},
	} {
		resolved, err := resolvePeriod(item.kind, item.period, now, "MONDAY")
		if err != nil {
			t.Fatal(err)
		}
		got := expectedWeekStarts(resolved, "MONDAY", owed)
		if len(got) != len(item.want) {
			t.Errorf("%s 의 기대 주차가 %v 입니다, %v 를 기대합니다", item.period, got, item.want)
			continue
		}
		for index := range got {
			if got[index] != item.want[index] {
				t.Errorf("%s 의 기대 주차가 %v 입니다, %v 를 기대합니다", item.period, got, item.want)
				break
			}
		}
	}

	// The future never counts, whatever the period.
	future, err := resolvePeriod(periodQuarter, "2026-Q4", now, "MONDAY")
	if err != nil {
		t.Fatal(err)
	}
	if weeks := expectedWeekStarts(future, "MONDAY", owed); len(weeks) != 0 {
		t.Errorf("아직 오지 않은 분기에 기대 주차가 %d개 있습니다: %v", len(weeks), weeks)
	}
}
