package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Two doors an operator can close, and one an attacker should not be able to
// walk through. All three were reachable, all three refused correctly, and none
// of them was tested.

// guards: updateSettings
func TestLocalLoginCannotBeTurnedOffWithNoOtherWayIn(t *testing.T) {
	server := newTestServer(t)

	// Turning off passwords without a working single sign-on leaves a
	// deployment nobody can enter — including the administrator making the
	// change, who would have no way to undo it.
	attempt := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"auth.local_enabled": "false"},
	}, server.admin)
	if attempt.Code == http.StatusOK {
		t.Fatal("local login was turned off with no identity provider configured")
	}
	if !strings.Contains(attempt.Body.String(), "LOGIN_REQUIRED") {
		t.Errorf("the refusal does not name the reason: %s", attempt.Body.String())
	}
	// And it has to say what to do, not only that it refused.
	if !strings.Contains(attempt.Body.String(), "OIDC") {
		t.Errorf("the refusal does not say what to set up first: %s", attempt.Body.String())
	}

	// The door is still open afterwards.
	after := server.attemptLogin(server.adminName, "HarnessAdmin1234")
	if after.Code != http.StatusOK {
		t.Fatalf("the administrator cannot sign in after the refused change: %d %s", after.Code, after.Body.String())
	}
}

// guards: mcp
func TestMCPRefusesARequestFromAnotherSite(t *testing.T) {
	server := newTestServer(t)

	call := func(origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		request.Header.Set("Content-Type", "application/json")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		request.AddCookie(server.admin)
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, request)
		return recorder
	}

	// A tool speaking the protocol directly sends no Origin at all, and must
	// keep working: this check exists for browsers, not for agents.
	if none := call(""); none.Code != http.StatusOK {
		t.Fatalf("a request with no Origin was refused: %d %s", none.Code, none.Body.String())
	}
	// httptest gives every request the host "example.com"; the check compares
	// Origin against that host, so this is what "the same site" means here.
	if same := call("http://example.com"); same.Code != http.StatusOK {
		t.Fatalf("a request from this service was refused: %d %s", same.Code, same.Body.String())
	}

	other := call("https://evil.invalid")
	if other.Code == http.StatusOK {
		t.Fatalf("a page on another site drove MCP with somebody's session: %s", other.Body.String())
	}
	if other.Code != http.StatusForbidden {
		t.Errorf("the refusal answered %d, want 403", other.Code)
	}
	if strings.Contains(other.Body.String(), "tools") {
		t.Errorf("the refusal listed the tools anyway: %s", other.Body.String())
	}
}
