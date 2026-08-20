package app

import "testing"

func TestPlanMatchKeyIgnoresSpacingAndCaseOnly(t *testing.T) {
	cases := []struct {
		left, right string
		same        bool
	}{
		{"전표 검증 자동화", "전표검증 자동화", true},
		{"AI Gateway 인증", "ai gateway인증", true},
		{"  월결산 자동화  ", "월결산 자동화", true},
		// The pair that measurement showed trigram similarity cannot separate
		// from the pairs above. Exact match after normalization keeps them apart.
		{"서버 A 점검", "서버 B 점검", false},
		{"1차 개발", "2차 개발", false},
	}
	for _, item := range cases {
		if got := planMatchKey(item.left) == planMatchKey(item.right); got != item.same {
			t.Errorf("planMatchKey(%q)==planMatchKey(%q): got=%v want=%v", item.left, item.right, got, item.same)
		}
	}
}

func TestCarryOverExcludesFinishedAndUnplannedWork(t *testing.T) {
	cases := []struct {
		name     string
		nextPlan string
		progress int
		want     bool
	}{
		{"planned and unfinished", "2차 API 개발", 60, true},
		{"planned but already complete", "인수인계 문서 정리", 100, false},
		{"no plan recorded", "   ", 40, false},
		{"not started but planned", "착수 준비", 0, true},
	}
	for _, item := range cases {
		if got := carriesOver(item.nextPlan, item.progress); got != item.want {
			t.Errorf("%s: carriesOver=%v want=%v", item.name, got, item.want)
		}
	}
}
