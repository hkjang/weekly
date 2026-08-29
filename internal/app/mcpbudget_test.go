package app

import (
	"encoding/json"
	osReadFileImport "os"
	"strings"
	"testing"
)

// The budget has to count what the caller receives.
//
// The protocol asks for the same answer twice — structuredContent for callers
// that understand it, a text mirror for the ones that do not — and the trimming
// measured one of them. Measured on a three-year deployment: the year at team
// scope trimmed its tasks from 264 to 5 to land the text block at 64,977 bytes,
// then shipped 71,471 more beside it. 144 KB reached the caller against a
// documented 64 KiB cap, and a model handed twice its budget cannot give the
// excess back.
//
// guards: encodedSize
func TestTheRollupBudgetCountsBothCopiesOfTheAnswer(t *testing.T) {
	data := map[string]any{"items": []string{strings.Repeat("가", 1000)}}

	once, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	measured := encodedSize(data)
	if measured <= len(once) {
		t.Errorf("예산이 %d바이트로 쟀는데 한 벌이 이미 %d바이트입니다 — 전선에는 두 벌이 갑니다",
			measured, len(once))
	}

	// And it measures the real envelope rather than a guessed multiplier, so it
	// stays true if the shape of the result ever changes.
	assembled, err := json.Marshal(map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(once)}},
		"structuredContent": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if measured != len(assembled) {
		t.Errorf("예산이 %d바이트, 실제로 조립한 결과가 %d바이트입니다", measured, len(assembled))
	}
}

// 기여자 목록은 사람 수만큼 자랍니다.
//
// Measured on a 300 person deployment: 304 contributors, 40,865 bytes — 79% of
// the payload, more than the tasks, the trend, the highlights and the summary
// together. Nothing bounded it, so once the budget counted both copies the
// trimming shed every one of 264 tasks and was still over: the caller lost the
// whole answer to keep a list of names it had not asked for.
//
// guards: mcpRollupContributors
func TestThePeriodSummaryDoesNotNameEverybody(t *testing.T) {
	if mcpRollupContributors <= 0 || mcpRollupContributors > 50 {
		t.Errorf("기여자 상한이 %d명입니다 — 한 화면에서 읽을 수 있는 수여야 합니다", mcpRollupContributors)
	}
	// The cap only means something if the payload says how many there were.
	source, err := readFileForTest("mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, needed := range []string{`"contributorsTotal"`, "mcpRollupContributors", "기여자 %d명 중 상위 %d명만"} {
		if !strings.Contains(source, needed) {
			t.Errorf("기여자를 자르면서 %q 를 말하지 않습니다 — 자른 사실이 응답에 없으면 독자는 전부라고 읽습니다", needed)
		}
	}
}

func readFileForTest(name string) (string, error) {
	body, err := osReadFileImport.ReadFile(name)
	return string(body), err
}
