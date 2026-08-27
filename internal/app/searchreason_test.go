package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// A short result list is the reader's evidence that nothing was written about
// what they searched for. It is only evidence if the passes that widen a thin
// search actually ran — and two of them can silently not run: the extension is
// missing, the embedding is unconfigured, or the pass failed. All three were
// swallowed into a server log, and the screen showed a short list with nothing
// beside it.
//
// The work search on the next screen over already answers this exact question
// in three sentences of its own. This is the same answer for the report search.
//
// guards: searchReports
func TestAThinSearchSaysWhichPassCouldNotRun(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("search_reason", "USER", nil)
	reportID, version := server.draft(author, "2026-08-24", "검색 사유 보고")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID), map[string]any{
		"summary": "검색 사유 보고",
		"version": version,
		"items": []map[string]any{
			{"category": "개발", "title": "고유단어치환기", "currentResult": "진행", "nextPlan": "계속", "issue": "", "progress": 10},
		},
	}, author)
	if filled.Code != http.StatusOK {
		t.Fatalf("write the report: %d %s", filled.Code, filled.Body.String())
	}

	read := func(query string) searchResponse {
		t.Helper()
		response := server.request(http.MethodGet, "/api/v1/search?q="+query, nil, author)
		if response.Code != http.StatusOK {
			t.Fatalf("search %q: %d %s", query, response.Code, response.Body.String())
		}
		var body struct {
			Data searchResponse `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode the search: %v", err)
		}
		return body.Data
	}

	// Embedding is off in a fresh deployment, so the meaning-based pass cannot
	// run. The sentence saying so is already written by embeddingConfig — the
	// administration screen prints the same one.
	thin := read("고유단어치환기")
	if thin.Reason == "" {
		t.Fatal("a thin search ran with the semantic pass unavailable and said nothing about it")
	}

	// And it stays quiet when there was nothing to widen. Telling a reader who
	// got a full page which extension is missing answers a question they did
	// not ask.
	for index := 0; index < searchThinResult+2; index++ {
		other := server.createUser(fmt.Sprintf("search_reason_%d", index), "USER", nil)
		id, v := server.draft(other, "2026-08-24", "흔한 낱말 보고")
		wrote := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
			"summary": "흔한 낱말 보고",
			"version": v,
			"items": []map[string]any{
				{"category": "개발", "title": "흔한낱말", "currentResult": "진행", "nextPlan": "계속", "issue": "", "progress": 10},
			},
		}, other)
		if wrote.Code != http.StatusOK {
			t.Fatalf("write a report: %d %s", wrote.Code, wrote.Body.String())
		}
	}
	admin := server.createUser("search_reason_admin", "ADMIN", nil)
	response := server.request(http.MethodGet, "/api/v1/search?q=흔한낱말", nil, admin)
	if response.Code != http.StatusOK {
		t.Fatalf("search as an administrator: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Data searchResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the search: %v", err)
	}
	if len(body.Data.Hits) < searchThinResult {
		t.Fatalf("this case needs a full result, got %d", len(body.Data.Hits))
	}
	if body.Data.Reason != "" {
		t.Errorf("a full result explained a widening it never needed: %q", body.Data.Reason)
	}
}
