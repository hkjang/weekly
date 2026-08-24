package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// "찾지 못했습니다" claims a search happened. When the meaning-based pass never
// ran — no pgvector, no embedding configuration, or it failed — the sentence is
// the same, and only one of those is something an administrator can change.
//
// guards: searchWorkItems
func TestWorkSearchSaysWhenTheMeaningPassDidNotRun(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("notyet_search", "USER", nil)
	server.submitted(author, "2026-08-24", "의미 검색 시험")

	// Embeddings are unconfigured on a fresh deployment, so this is the state
	// every installation starts in.
	nothing := server.request(http.MethodGet, "/api/v1/work-items/search?q="+url.QueryEscape("존재하지않는말"), nil, author)
	if nothing.Code != http.StatusOK {
		t.Fatalf("work item search: %d %s", nothing.Code, nothing.Body.String())
	}
	data := decodeData(t, nothing)
	if semantic, _ := data["semantic"].(bool); semantic {
		t.Fatal("the meaning pass reports as having run with nothing configured")
	}
	reason, _ := data["semanticReason"].(string)
	if reason == "" {
		t.Fatal("the response does not say the meaning pass was skipped, so an empty result reads as a completed search")
	}
	if !strings.Contains(reason, "글자") {
		t.Errorf("the reason does not say what was searched instead: %q", reason)
	}

	// And when it does run, there is nothing to explain.
	full := server.app.capabilities
	if !full.Vector {
		t.Skip("the test database has no pgvector, so the positive control cannot run")
	}
	// Configure embeddings so the pass is attempted rather than skipped.
	if settings := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": "http://ai.invalid/v1/chat/completions",
			"ai.model": "m", "ai.api_key": "k",
			"ai.embedding_enabled": "true", "ai.embedding_model": "embed",
			"ai.embedding_endpoint": "http://ai.invalid/v1/embeddings",
		},
	}, server.admin); settings.Code != http.StatusOK {
		t.Fatalf("configure embeddings: %d %s", settings.Code, settings.Body.String())
	}
	attempted := server.request(http.MethodGet, "/api/v1/work-items/search?q="+url.QueryEscape("존재하지않는말"), nil, author)
	if attempted.Code != http.StatusOK {
		t.Fatalf("work item search with embeddings configured: %d %s", attempted.Code, attempted.Body.String())
	}
	after, _ := decodeData(t, attempted)["semanticReason"].(string)
	if after == reason {
		t.Errorf("configuring embeddings changed nothing about the explanation: %q", after)
	}
}
