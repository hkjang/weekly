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

// The same pairing for the document an operator actually reads.
//
// Only the contract was covered, and README drifted: it promised the executive
// digest picks "5~10건" when nothing sets a floor, and its table of what each
// observed fact is worth had lost two of its rows. That table is the product's
// argument for itself — 근거를 볼 수 없는 요약은 읽는 사람이 반박할 수 없다 —
// so a weight published there and nowhere paired with the code is the one
// number that must not drift.

// guards: buildDigest, recurringWorkItems
func TestTheREADMEsNumbersAreTheOnesTheCodeUses(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Skipf("the README is not readable from here: %v", err)
	}
	readme := string(raw)

	for _, item := range []struct{ what, sentence string }{
		{"경영 요약 상한", fmt.Sprintf("**최대 %d건** 선정합니다", digestMaximumEntries)},
		{"결정·자원 요청 가중치", fmt.Sprintf("| 상위 조직 결정·자원 요청이 열려 있음 | +%d |", digestWeightDecision)},
		{"이슈 지속 가중치", fmt.Sprintf("| 이슈 지속 (미완료 업무) | 주당 +%d |", digestWeightIssuePerWeek)},
		{"진척 정체 가중치", fmt.Sprintf("| 진척 정체 | 주당 +%d |", digestWeightStalledPerWeek)},
		{"보고 누락 가중치", fmt.Sprintf("| 보고 누락 | 주당 +%d |", digestWeightSilentPerWeek)},
		{"중복 의심 가중치", fmt.Sprintf("| 조직 간 중복 의심 | +%d |", digestWeightDuplicate)},
		{"마감일 초과 가중치", fmt.Sprintf("| 마감일 초과 | +%d 및 초과 주당 +%d |", digestWeightOverdue, digestWeightOverduePerWeek)},
		{"마감일 초과 예상 가중치", fmt.Sprintf("| 마감일 초과 예상 | +%d |", digestWeightDueAtRisk)},
		{"완료 가중치", fmt.Sprintf("| 4주 이상 진행한 업무의 완료 | +%d 및 주당 +%d |", digestWeightDone, digestWeightDonePerWeek)},
		{"중복 판정 임계", fmt.Sprintf("제목이 %d%% 이상 일치", duplicateTitleSimilarity)},
		{"유사 판정 임계", fmt.Sprintf("%d%% 이상 일치하는 업무", relatedTitleSimilarity)},
		{"반복 업무 보고 주차", fmt.Sprintf("| 보고 주차 | %d주 이상 |", recurringMinimumWeeks)},
		{"반복 업무 보고 주기", fmt.Sprintf("| 보고 주기 | 경과 주차 대비 %d%% 이상 |", recurringCadencePercent)},
		{"반복 업무 진척 변화", fmt.Sprintf("| 진척 변화 | %d%% 이하 |", recurringMaximumGain)},
		{"검색 단계 전환", fmt.Sprintf("정확히 일치하는 결과가 %d건보다 적으면", searchThinResult)},
	} {
		if !strings.Contains(readme, item.sentence) {
			t.Errorf("%s: README.md does not say %q — the code changed and the sentence did not, or the sentence was reworded and this pairing has to follow",
				item.what, item.sentence)
		}
	}
}
