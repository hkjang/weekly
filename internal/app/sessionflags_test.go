package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The editor asks Confluence for candidates every time it opens. A deployment
// that does not use Confluence answered that call with a configuration error on
// every visit — a wasted round trip and a red line in every reader's console
// for a feature nobody turned on. The session already told the screen whether
// the workflow and the AI gateway were on; it now says the same about
// Confluence, so the screen can stop asking a question it knows the answer to.

// guards: me
func TestTheSessionSaysWhichOptionalFeaturesAreOn(t *testing.T) {
	server := newTestServer(t)

	read := func() map[string]any {
		t.Helper()
		w := server.request(http.MethodGet, "/api/v1/me", nil, server.admin)
		if w.Code != http.StatusOK {
			t.Fatalf("read the session: %d %s", w.Code, w.Body.String())
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
		return envelope.Data
	}

	for _, flag := range []string{"workflowEnabled", "aiEnabled", "confluenceEnabled"} {
		if _, ok := read()[flag]; !ok {
			t.Fatalf("the session does not say whether %s", flag)
		}
	}
	if read()["confluenceEnabled"] != false {
		t.Fatal("a deployment that has not configured Confluence should not report it as on")
	}

	saved := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"confluence.enabled":   "true",
			"confluence.base_url":  "http://confluence.invalid",
			"confluence.auth_mode": "NONE",
		},
	}, server.admin)
	if saved.Code != http.StatusOK {
		t.Fatalf("turn Confluence on: %d %s", saved.Code, saved.Body.String())
	}
	if read()["confluenceEnabled"] != true {
		t.Fatal("Confluence was turned on and the session still says it is off")
	}
}
