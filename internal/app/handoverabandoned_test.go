package app

import (
	"strings"
	"testing"
)

// Measured on a deployment: 평가 통합, last mentioned seven months ago at 49%,
// came through the handover with 멈춤 false, silentWeeks 0 and no caution at
// all — while a task reported that same week carried "32주째 진척이 없습니다"
// and one that missed a single week said so.
//
// Every other measure is computed inside the task's own reported span, and a
// task that simply stopped has nothing wrong inside it. The screen warned about
// a one-week gap and said nothing about a seven-month disappearance, on the one
// screen whose whole question is "what does this person still owe?".

// guards: cautionFor, weeksSince
func TestHandoverSaysWhenNobodyHasMentionedTheWorkInMonths(t *testing.T) {
	const current = "2026-08-24"

	abandoned := workItemView{LastWeek: "2026-01-26", Progress: 49, ReportedWeeks: 22, AgeWeeks: 22}
	caution := cautionFor(abandoned, current)
	if caution == "" {
		t.Fatal("일곱 달 전이 마지막인 업무에 아무 말도 하지 않습니다")
	}
	if !strings.Contains(caution, "2026-01-26") {
		t.Errorf("마지막 기록이 언제인지 말하지 않습니다: %q", caution)
	}
	if !strings.Contains(caution, "30주째") {
		t.Errorf("몇 주째인지 틀렸습니다: %q", caution)
	}

	// It outranks the measures taken inside the span: a task nobody has
	// mentioned in months is the thing to say first, whatever its own history
	// looked like while it was alive.
	noisy := workItemView{LastWeek: "2026-01-26", Stalled: true, StalledWeeks: 9, IssueRunWeeks: 5, SilentWeeks: 3}
	if got := cautionFor(noisy, current); !strings.Contains(got, "언급한 보고가 없습니다") {
		t.Errorf("멈춤과 이슈가 사라진 사실을 가렸습니다: %q", got)
	}

	// Work reported this week keeps the caution its own history earns.
	live := workItemView{LastWeek: current, Stalled: true, StalledWeeks: 32}
	if got := cautionFor(live, current); !strings.Contains(got, "32주째 진척이 없습니다") {
		t.Errorf("살아 있는 업무의 경고가 바뀌었습니다: %q", got)
	}

	// A fortnight is a holiday, not an abandonment.
	recent := workItemView{LastWeek: "2026-08-10"}
	if got := cautionFor(recent, current); strings.Contains(got, "언급한 보고가 없습니다") {
		t.Errorf("2주 만에 버려졌다고 말합니다: %q", got)
	}

	// Completed work that stopped being mentioned is not abandoned; it is done.
	done := workItemView{LastWeek: "2026-01-26", Completed: true}
	if got := cautionFor(done, current); !strings.Contains(got, "완료로 보고된") {
		t.Errorf("완료된 업무를 버려진 것으로 다룹니다: %q", got)
	}

	for _, bad := range []struct{ last, now string }{
		{"", current}, {current, ""}, {"어제", current}, {"2026-12-31", current},
	} {
		if got := weeksSince(bad.last, bad.now); got != 0 {
			t.Errorf("weeksSince(%q, %q) = %d, want 0", bad.last, bad.now, got)
		}
	}
}
