package app

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The numbers README publishes have to be the numbers the product uses.
//
// README is not marketing here — it is where this product explains its own
// judgements, and the digest weight table is published precisely so a reader
// can argue with a ranking. meeting.go says so in a comment above those
// constants, and records what happened without a check: "Two rows of that table
// had already gone missing before this was noticed."
//
// v0.244.0 found the same shape one layer over: a comment claiming a PREPARE
// test that did not exist. Prose drifts from code silently, because nothing
// compiles prose.
func TestTheNumbersREADMEPublishesAreTheOnesTheProductUses(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Skipf("README 를 읽을 수 없습니다: %v", err)
	}
	text := string(readme)

	// Each claim is the sentence or table row as it stands in README, so a
	// rewording that drops the number fails here rather than going unnoticed.
	for _, claim := range []struct {
		what  string
		line  string
		value int
	}{
		{"결정·요청 가중치", "| 상위 조직 결정·자원 요청이 열려 있음 | +%d |", digestWeightDecision},
		{"이슈 지속 가중치", "| 이슈 지속 (미완료 업무) | 주당 +%d |", digestWeightIssuePerWeek},
		{"진척 정체 가중치", "| 진척 정체 | 주당 +%d |", digestWeightStalledPerWeek},
		{"보고 누락 가중치", "| 보고 누락 | 주당 +%d |", digestWeightSilentPerWeek},
		{"중복 의심 가중치", "| 조직 간 중복 의심 | +%d |", digestWeightDuplicate},
		{"마감 초과 예상 가중치", "| 마감일 초과 예상 | +%d |", digestWeightDueAtRisk},
		{"반복 업무 최소 주차", "| 보고 주차 | %d주 이상 |", recurringMinimumWeeks},
		{"반복 업무 보고 주기", "| 보고 주기 | 경과 주차 대비 %d%% 이상 |", recurringCadencePercent},
		{"반복 업무 진척 변화", "| 진척 변화 | %d%% 이하 |", recurringMaximumGain},
	} {
		want := fmt.Sprintf(claim.line, claim.value)
		if !strings.Contains(text, want) {
			t.Errorf("%s: README 에 %q 가 없습니다. 코드는 %d 입니다", claim.what, want, claim.value)
		}
	}

	// Two-part rows, where README writes a base and a per-week rate together.
	for _, claim := range []struct {
		what       string
		line       string
		base, rate int
	}{
		{"마감 초과", "| 마감일 초과 | +%d 및 초과 주당 +%d |", digestWeightOverdue, digestWeightOverduePerWeek},
		{"완료", "| %d주 이상 진행한 업무의 완료 | +%d 및 주당 +%d |", digestWeightDone, digestWeightDonePerWeek},
	} {
		var want string
		if claim.what == "완료" {
			want = fmt.Sprintf(claim.line, recurringMinimumWeeks, claim.base, claim.rate)
		} else {
			want = fmt.Sprintf(claim.line, claim.base, claim.rate)
		}
		if !strings.Contains(text, want) {
			t.Errorf("%s: README 에 %q 가 없습니다", claim.what, want)
		}
	}

	// Sentences, where the number sits inside prose rather than a table.
	for _, claim := range []struct {
		what    string
		pattern string
		value   int
	}{
		{"경영 요약 최대 건수", `\*\*최대 (\d+)건\*\* 선정합니다`, digestMaximumEntries},
		{"검색 확장 단계 임계", `정확히 일치하는 결과가 (\d+)건보다 적으면`, searchThinResult},
		{"메일 현황 기간", `최근 (\d+)일의 발송·대기·실패`, mailHealthDays},
		{"중복 의심 제목 일치율", `제목이 (\d+)% 이상 일치하는 업무`, duplicateTitleSimilarity},
		{"유사 업무 제목 일치율", `(\d+)% 이상 일치하는 업무. 참고할`, relatedTitleSimilarity},
	} {
		match := regexp.MustCompile(claim.pattern).FindStringSubmatch(text)
		if match == nil {
			t.Errorf("%s: README 에서 그 문장을 찾지 못했습니다 (%s)", claim.what, claim.pattern)
			continue
		}
		if match[1] != fmt.Sprint(claim.value) {
			t.Errorf("%s: README 는 %s, 코드는 %d 입니다", claim.what, match[1], claim.value)
		}
	}
}
