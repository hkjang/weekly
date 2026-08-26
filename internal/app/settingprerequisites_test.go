package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Turning a feature on with nothing behind it is a configuration that fails at
// use rather than at the switch: every AI screen answers with an error nobody
// can act on, and the person who flipped it has already moved on. The handler
// refuses it — and nothing ran that refusal, so the whole prerequisite block
// could be inverted and the suite stayed green.

// guards: updateSettings
func TestTurningTheAIOnNeedsSomewhereToSendThePrompt(t *testing.T) {
	server := newTestServer(t)

	// Switching it off comes first and on purpose: nothing is configured yet,
	// so a prerequisite check that runs whenever the key is mentioned — rather
	// than only when it is being turned on — refuses here. Put this after the
	// configuration exists and the difference disappears.
	offFirst := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"ai.enabled": "false"},
	}, server.admin)
	if offFirst.Code != http.StatusOK {
		t.Fatalf("the AI could not be switched off before anything was configured: %d %s",
			offFirst.Code, offFirst.Body.String())
	}

	bare := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"ai.enabled": "true"},
	}, server.admin)
	if bare.Code != http.StatusBadRequest || errorCode(bare) != "AI_CONFIGURATION_REQUIRED" {
		t.Fatalf("the AI was switched on with no endpoint: %d %s", bare.Code, bare.Body.String())
	}

	// An endpoint with no model is still nowhere to send it.
	half := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"ai.enabled": "true", "ai.endpoint": "https://ai.internal/v1"},
	}, server.admin)
	if half.Code != http.StatusBadRequest || errorCode(half) != "AI_CONFIGURATION_REQUIRED" {
		t.Errorf("the AI was switched on with no model: %d %s", half.Code, half.Body.String())
	}

	// Both in the same request is the way it is meant to be done.
	together := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": "https://ai.internal/v1", "ai.model": "weekly-1",
		},
	}, server.admin)
	if together.Code != http.StatusOK {
		t.Errorf("a complete configuration was refused: %d %s", together.Code, together.Body.String())
	}

	// And off again once it is configured.
	off := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"ai.enabled": "false"},
	}, server.admin)
	if off.Code != http.StatusOK {
		t.Errorf("the AI could not be switched off: %d %s", off.Code, off.Body.String())
	}
}

// guards: updateSettings
func TestTurningConfluenceOnNeedsAnAddressAndAWayToSignIn(t *testing.T) {
	server := newTestServer(t)

	offFirst := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"confluence.enabled": "false"},
	}, server.admin)
	if offFirst.Code != http.StatusOK {
		t.Fatalf("Confluence could not be switched off before anything was configured: %d %s",
			offFirst.Code, offFirst.Body.String())
	}

	bare := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"confluence.enabled": "true"},
	}, server.admin)
	if bare.Code != http.StatusBadRequest || errorCode(bare) != "CONFLUENCE_CONFIGURATION_REQUIRED" {
		t.Fatalf("Confluence was switched on with no address: %d %s", bare.Code, bare.Body.String())
	}

	// An address but no credentials, with Basic Auth selected.
	noCredentials := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"confluence.enabled": "true", "confluence.base_url": "https://confluence.internal",
			"confluence.auth_mode": "BASIC",
		},
	}, server.admin)
	if noCredentials.Code != http.StatusBadRequest || errorCode(noCredentials) != "CONFLUENCE_CREDENTIAL_REQUIRED" {
		t.Errorf("Basic Auth was accepted with no account: %d %s", noCredentials.Code, noCredentials.Body.String())
	}

	complete := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"confluence.enabled": "true", "confluence.base_url": "https://confluence.internal",
			"confluence.auth_mode": "BASIC", "confluence.username": "weekly",
			"confluence.password": "ConfluencePassword1234",
		},
	}, server.admin)
	if complete.Code != http.StatusOK {
		t.Errorf("a complete configuration was refused: %d %s", complete.Code, complete.Body.String())
	}

	// An empty password in a later save means "leave the stored one alone", not
	// "there is no password". Reading it as the latter locks the administrator
	// out of every other Confluence setting until they retype the secret.
	keepSecret := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"confluence.enabled": "true", "confluence.password": ""},
	}, server.admin)
	if keepSecret.Code != http.StatusOK {
		t.Errorf("saving without retyping the password was refused: %d %s",
			keepSecret.Code, keepSecret.Body.String())
	}
}

// guards: apiKeyRequestAllowed
//
// TestAnAPIKeyWithoutTheMCPScopeCannotUseMCP checks that a narrow key is turned
// away. It never checks that the scope it does hold opens anything.
//
// One path is not enough either. The reports scope names four prefixes in a
// single alternation, and a test that calls only the first one short-circuits
// before reaching the rest — so three quarters of the branch stayed unrun while
// the test looked like it covered it. That is what a mutation at each `||`
// showed, and what checking by hand at the first one missed.
func TestAScopedKeyOpensEveryPathItsScopeNames(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("키 조직", "KEYSCOPE")
	owner := server.createUser("scope_owner", "TEAM_LEADER", &organisation)
	server.submitted(owner, "2026-08-24", "키로 읽을 보고")

	key := createKey(t, server, owner, "보고서만 읽는 키", []string{"reports:read"})
	get := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+key)
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, request)
		return recorder
	}

	for _, path := range []string{
		"/api/v1/me",
		"/api/v1/reports",
		"/api/v1/team/reports",
		"/api/v1/rollups?kind=YEAR&period=2026&scope=SELF",
		"/api/v1/search?q=보고",
	} {
		if w := get(path); w.Code != http.StatusOK {
			t.Errorf("a key holding reports:read cannot read %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	// And the scope it does not hold stays shut.
	if w := get("/api/v1/analytics/overview"); w.Code == http.StatusOK {
		t.Errorf("a key without analytics:read read the analytics anyway: %s", w.Body.String())
	}
}

// guards: mcp
//
// The envelope checks: a request that is not JSON-RPC 2.0 is refused, and a
// protocol version the server does not implement is not echoed back as though
// it were agreed. Nothing ran either, so the surface would have accepted
// whatever a caller sent and told them it spoke their version.
func TestTheMCPSurfaceChecksTheEnvelopeItAnswers(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("mcp_envelope", "USER", nil)

	send := func(body string) *httptest.ResponseRecorder {
		return server.requestRaw(http.MethodPost, "/mcp", body, owner)
	}

	wrongVersion := send(`{"jsonrpc":"1.0","id":1,"method":"tools/list","params":{}}`)
	if !strings.Contains(wrongVersion.Body.String(), "-32600") {
		t.Errorf("a request that is not JSON-RPC 2.0 was answered normally: %s", wrongVersion.Body.String())
	}
	noMethod := send(`{"jsonrpc":"2.0","id":1,"params":{}}`)
	if !strings.Contains(noMethod.Body.String(), "-32600") {
		t.Errorf("a request naming no method was answered normally: %s", noMethod.Body.String())
	}

	// An unknown protocol version is answered with the one the server does
	// implement, not with the caller's.
	negotiated := send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if strings.Contains(negotiated.Body.String(), "1999-01-01") {
		t.Errorf("the server agreed to a version it does not implement: %s", negotiated.Body.String())
	}
	if !strings.Contains(negotiated.Body.String(), mcpProtocolVersion) {
		t.Errorf("the reply does not name the version the server does implement: %s", negotiated.Body.String())
	}
}

// requestRaw sends a body the harness would otherwise encode, so a test can put
// a malformed envelope on the wire.
func (s *testServer) requestRaw(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	s.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.test")
	request.Host = "example.test"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	s.app.mux.ServeHTTP(recorder, request)
	return recorder
}

// guards: updateSettings
//
// TestLocalLoginCannotBeTurnedOffWithNoOtherWayIn checks the refusal. Turning
// passwords back on is the way out of that corner, and nothing ran it — so the
// prerequisite could be widened to fire on any mention of the setting, and a
// deployment with no identity provider would be unable to re-enable the only
// sign-in it has.
func TestLocalLoginCanBeTurnedBackOnWithoutAnIdentityProvider(t *testing.T) {
	server := newTestServer(t)

	on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"auth.local_enabled": "true"},
	}, server.admin)
	if on.Code != http.StatusOK {
		t.Fatalf("local login could not be turned on: %d %s", on.Code, on.Body.String())
	}
}
