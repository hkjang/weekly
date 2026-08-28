package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The editor keeps what is on screen when a save fails. Its own comment says
// why: overwriting it "would destroy exactly the work the failed save was
// trying to keep". It then re-reads the version and shows the server's
// sentence — which told the reader to refresh the page, the one action that
// throws that work away, and one the client had already made unnecessary.
//
// guards: updateReport
func TestASaveConflictDoesNotSendTheWriterToLoseTheirWriting(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("conflict_author", "USER", nil)
	reportID, version := server.draft(author, "2026-08-24", "처음 요약")

	body := func(version int, summary string) map[string]any {
		return map[string]any{
			"version": version, "summary": summary,
			"items": []map[string]any{
				{"category": "개발", "title": "내 업무", "currentResult": "쓰던 중",
					"nextPlan": "계속", "issue": "", "progress": 10},
			},
		}
	}

	// The other tab saves first.
	first := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID),
		body(version, "다른 탭이 먼저 저장한 요약"), author)
	if first.Code != http.StatusOK {
		t.Fatalf("the first save failed: %d %s", first.Code, first.Body.String())
	}

	// This tab still holds the version it loaded with.
	stale := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID),
		body(version, "내가 지금 쓰고 있던 글"), author)
	if stale.Code != http.StatusConflict {
		t.Fatalf("a stale save answered %d, want 409: %s", stale.Code, stale.Body.String())
	}
	message := refusal(t, stale)
	if strings.Contains(message, "새로고침 후 다시 시도") {
		t.Errorf("the writer is told to refresh, which discards what they wrote: %s", message)
	}
	for _, expected := range []string{"그대로", "저장"} {
		if !strings.Contains(message, expected) {
			t.Errorf("the message does not mention %q: %s", expected, message)
		}
	}

	// And the remedy the message names has to work. This is what the editor
	// does: re-read the version, keep the content, save again.
	current := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", reportID), nil, author)
	var loaded struct {
		Data struct {
			Version int `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(current.Body.Bytes(), &loaded); err != nil {
		t.Fatalf("re-read the report: %v", err)
	}
	again := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID),
		body(loaded.Data.Version, "내가 지금 쓰고 있던 글"), author)
	if again.Code != http.StatusOK {
		t.Fatalf("saving again did not work, so the message promised something false: %d %s",
			again.Code, again.Body.String())
	}
	kept := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", reportID), nil, author)
	if !strings.Contains(kept.Body.String(), "내가 지금 쓰고 있던 글") {
		t.Error("the writing did not survive the second save")
	}
}

// Deleting is the other way round: nothing is being typed, and what would go is
// no longer what the reader was looking at when they decided. Reloading is the
// right advice there, and it has to say why.
//
// guards: deleteReport
func TestADeleteConflictSaysWhatChangedUnderneath(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("conflict_deleter", "USER", nil)
	reportID, version := server.draft(author, "2026-08-24", "지울 보고")

	edited := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID),
		map[string]any{"version": version, "summary": "그사이 고친 요약",
			"items": []map[string]any{{"category": "개발", "title": "업무", "currentResult": "진행",
				"nextPlan": "계속", "issue": "", "progress": 10}}}, author)
	if edited.Code != http.StatusOK {
		t.Fatalf("edit the report: %d %s", edited.Code, edited.Body.String())
	}

	stale := server.request(http.MethodDelete,
		fmt.Sprintf("/api/v1/reports/%d?version=%d", reportID, version), nil, author)
	if stale.Code != http.StatusConflict {
		t.Fatalf("a stale delete answered %d, want 409: %s", stale.Code, stale.Body.String())
	}
	message := refusal(t, stale)
	if !strings.Contains(message, "다시 불러") {
		t.Errorf("the message does not say to reload before deleting: %s", message)
	}
	if !strings.Contains(message, "다를 수 있") {
		t.Errorf("the message does not say why reloading matters here: %s", message)
	}
}
