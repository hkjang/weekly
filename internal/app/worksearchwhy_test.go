package app

import (
	"strings"
	"testing"
)

// The sentence under a search hit is the product's reason for showing it.
//
// "같은 문제를 겪고 해결한 사례입니다" is a claim about the query, and it was made
// whether or not the query had anything to do with the task. The case that
// handles no overlap sat third in the switch, reachable only by a task with no
// issue and no resolution — while a task with a resolution is exactly the one
// the first sentence describes. Measured on a running deployment: a nonsense
// query returned twenty hits, every one of them claiming the same problem, and
// every one of them with matched: [].
//
// guards: describeWorkHit
func TestAMeaningOnlyMatchDoesNotClaimTheSameProblem(t *testing.T) {
	const claim = "같은 문제"
	const caveat = "직접 겹치는 단어는 없지만"

	resolved := workSearchHit{Resolution: "리포트 자동화 진행 상황을 정리했습니다", Resolved: true}
	running := workSearchHit{Issue: "배포가 막혀 있습니다", IssueRunWeeks: 3}

	for _, item := range []struct {
		name string
		hit  workSearchHit
	}{{"해결된 사례", resolved}, {"진행 중 이슈", running}} {
		// No term overlap: the sentence has to say so, and may still carry the
		// fact the reader came for.
		why := describeWorkHit(item.hit, true)
		if strings.Contains(why, claim) {
			t.Errorf("%s · 겹치는 낱말이 없는데 %q 라고 말합니다: %s", item.name, claim, why)
		}
		if !strings.Contains(why, caveat) {
			t.Errorf("%s · 의미로만 맞은 것을 말하지 않습니다: %s", item.name, why)
		}
		if item.hit.Resolution != "" && !strings.Contains(why, item.hit.Resolution) {
			t.Errorf("%s · 해결 경과가 사라졌습니다: %s", item.name, why)
		}

		// With overlap the original sentences stand.
		overlapping := item.hit
		overlapping.Matched = []string{"자동화"}
		if why := describeWorkHit(overlapping, true); strings.Contains(why, caveat) {
			t.Errorf("%s · 낱말이 겹치는데 겹치지 않는다고 말합니다: %s", item.name, why)
		}
	}

	// A plain text search is not a meaning match, so nothing is qualified.
	if why := describeWorkHit(resolved, false); !strings.Contains(why, claim) {
		t.Errorf("글자 검색인데 문장이 바뀌었습니다: %s", why)
	}
}
