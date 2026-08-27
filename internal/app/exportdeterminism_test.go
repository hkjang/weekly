package app

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"testing"
)

// Measured on a deployment: twenty downloads of one unchanged report produced
// four distinct files and three distinct entry orders. Every part's contents
// were identical — only the order of three parts moved, because they were
// written while ranging over a map. Nothing was wrong with the deck, and
// nothing could tell that by comparing two of them either: an operator asking
// "did this change since the copy I kept?" got a false yes about a fifth of
// the time, and a content hash could never deduplicate two identical exports.

// guards: exportReportPPTX
func TestTwoExportsOfOneUnchangedReportAreTheSameFile(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("export_determinism", "USER", nil)
	id, version := server.draft(author, "2026-08-24", "같은 보고서를 두 번 내보냅니다")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "같은 보고서를 두 번 내보냅니다", "version": version,
		"items": []map[string]any{
			{"category": "인프라", "title": "회선 이설", "currentResult": "3개 지사 완료",
				"nextPlan": "잔여 2개 지사", "issue": "임대 회선 일정 지연", "progress": 60},
			{"category": "개발", "title": "결제 연동", "currentResult": "1차 적용",
				"nextPlan": "2차 적용", "issue": "", "progress": 40},
		},
	}, author)
	if filled.Code != http.StatusOK {
		t.Fatalf("보고서 저장: %d %s", filled.Code, filled.Body.String())
	}

	// Several rounds: map order is shuffled per iteration, so one repeat can
	// agree by chance. Three parts have six orders between them.
	digests := map[string]int{}
	const rounds = 8
	for round := 0; round < rounds; round++ {
		deck := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
		if deck.Code != http.StatusOK {
			t.Fatalf("내보내기 %d회차: %d %s", round+1, deck.Code, deck.Body.String())
		}
		digests[fmt.Sprintf("%x", sha256.Sum256(deck.Body.Bytes()))]++
	}
	if len(digests) != 1 {
		t.Errorf("바뀐 것이 없는 보고서를 %d번 내보냈더니 서로 다른 파일이 %d가지 나왔습니다: %v",
			rounds, len(digests), digests)
	}
}
