package app

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// docs/openapi.yaml is the only thing an integrator on an offline network has.
// Every number in it is a promise, and a promise drifts silently: somebody
// raises a cap in Go, the sentence keeps the old figure, and the file becomes
// confidently wrong — worse than saying nothing.
//
// Each expectation below is built *from the constant*, so changing the constant
// changes what this looks for. The doc and the code move together or this fails.
func TestTheContractsNumbersAreTheOnesTheCodeUses(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Skipf("the API contract is not readable from here: %v", err)
	}
	contract := string(raw)

	cases := []struct {
		what     string
		sentence string
	}{
		{"주간보고 목록", fmt.Sprintf("limit 기본값 %d, 최대 %d", reportPageDefault, reportPageMaximum)},
		{"팀 주간보고 목록", fmt.Sprintf("limit 기본 %d, 최대 %d", reportPageDefault, reportPageMaximum)},
		{"Import 이력", fmt.Sprintf("limit 기본 %d, 최대 %d", importPageDefault, importPageMaximum)},
		{"감사 이력 상한", fmt.Sprintf("limit 상한은 %d이다", auditPageMaximum)},
		{"검색 단어 수", fmt.Sprintf("최대 %d개까지 사용한다", searchMaxTerms)},
		{"기간 보고 결정", fmt.Sprintf("최대 %d건이며 `decisionTotal`", rollupDecisionLimit)},
		{"경영 다이제스트", fmt.Sprintf("최대 %d건을 선정한다", digestMaximumEntries)},
		{"유사·중복 쌍", fmt.Sprintf("관련도 상위 %d건씩만", insightLinkLimit)},
		{"병목", fmt.Sprintf("최대 %d건 담는다", bottleneckLimit)},
		{"인수인계 관련 업무", fmt.Sprintf("업무마다 **최대 %d건**이다", handoverRelatedPerItem)},
		{"결정 제안", fmt.Sprintf("최대 %d건. AI Gateway가", decisionSuggestLimit)},
		{"검색 단계 전환", fmt.Sprintf("정확 일치 결과가 %d건 미만이면", searchThinResult)},
	}
	for _, item := range cases {
		if !strings.Contains(contract, item.sentence) {
			t.Errorf("%s: docs/openapi.yaml does not say %q — the code changed and the sentence did not, or the sentence was reworded and this pairing has to follow",
				item.what, item.sentence)
		}
	}
}
