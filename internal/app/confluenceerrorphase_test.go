package app

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Measured on a deployment: a sync that had just walked 120 Confluence pages
// and finished SUCCESS carried 111 AI_CLASSIFY rows and 114 AI_SUMMARY rows,
// every one of them reading "Confluence에 연결하지 못했습니다". Confluence was
// fine. The AI Gateway was the one refusing, and its own connection test — the
// same failure, one screen over — says which of eight things went wrong and
// what to do about it.

// guards: confluenceSyncErrorMessage
func TestASyncFailureNamesTheSystemThatActuallyFailed(t *testing.T) {
	gatewayRefused := &aiStatusError{Status: http.StatusInternalServerError}
	confluenceRefused := &ConfluenceHTTPError{StatusCode: http.StatusUnauthorized}
	stored := errors.New("ERROR: duplicate key value violates unique constraint")

	for _, tc := range []struct {
		phase   string
		err     error
		wants   string
		forbids string
	}{
		{"AI_CLASSIFY", gatewayRefused, "AI Gateway", "Confluence"},
		{"AI_SUMMARY", gatewayRefused, "AI Gateway", "Confluence"},
		{"AI_CONFIGURATION", gatewayRefused, "AI Gateway", "Confluence"},
		{"METADATA_STORE", stored, "데이터베이스", "Confluence"},
		{"DEDUPLICATION", stored, "데이터베이스", "Confluence"},
		{"SEARCH", confluenceRefused, "Confluence", "AI Gateway"},
		{"BODY", confluenceRefused, "Confluence", "데이터베이스"},
	} {
		got := confluenceSyncErrorMessage(tc.phase, tc.err)
		if !strings.Contains(got, tc.wants) {
			t.Errorf("%s: %q 에 %q 가 없습니다", tc.phase, got, tc.wants)
		}
		if strings.Contains(got, tc.forbids) {
			t.Errorf("%s: %q 가 %q 를 탓합니다. 그 시스템은 멀쩡합니다", tc.phase, got, tc.forbids)
		}
	}
}

// The AI phases must say the same thing the administrator's own AI connection
// test says, so one failure does not read as two different problems.
func TestTheSyncsAIFailureMatchesTheAIConnectionTest(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		err := &aiStatusError{Status: status}
		sync := confluenceSyncErrorMessage("AI_CLASSIFY", err)
		if want := aiUserMessage(err); sync != want {
			t.Errorf("HTTP %d: 동기화는 %q, 관리자 화면은 %q", status, sync, want)
		}
	}
}
