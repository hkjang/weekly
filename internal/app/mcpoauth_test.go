package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// MCP 를 개인 키 없이 Keycloak 토큰으로.
//
// The authorization flow itself — PKCE, the redirect, the code exchange — is
// Keycloak's and the client's. What is this server's is the resource-server
// half of the specification, and that is what these tests hold it to: it says
// where the authorization server is, it turns a 401 into a pointer there, and
// it accepts exactly the tokens that server issued for this resource, for a
// person Weekly already knows, with the powers a key would have and no more.

// mcpWith sends one MCP call with the given bearer and returns the recorder.
func mcpWith(server *testServer, bearer, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "weekly.example.test"
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	server.app.mux.ServeHTTP(recorder, request)
	return recorder
}

// accessToken is what Keycloak would hand an MCP client after the person
// signed in: signed by the realm key, issued by the issuer, for an audience.
func accessToken(t *testing.T, idp *fakeIDP, audience any, claims map[string]any) string {
	t.Helper()
	payload := map[string]any{
		"iss": idp.server.URL, "aud": audience, "sub": "subject-mcp",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"typ": "Bearer", "preferred_username": "ssomember",
	}
	for key, value := range claims {
		payload[key] = value
	}
	return idp.sign(t, payload)
}

const listTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

// guards: protectedResourceMetadata, wwwAuthenticate
func TestARefusedMCPClientIsToldWhereToSignIn(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly-web", "", nil)

	// Off by default: a deployment without SSO advertises nothing and a
	// refusal is the refusal it always was.
	before := server.request(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil, nil)
	if before.Code != http.StatusNotFound {
		t.Fatalf("metadata is served with SSO off: %d %s", before.Code, before.Body.String())
	}
	if header := mcpWith(server, "", listTools).Header().Get("WWW-Authenticate"); header != "" {
		t.Fatalf("a 401 pointed at an authorization server that is not configured: %q", header)
	}

	server.useIDP(t, idp, "weekly-web", map[string]string{"mcp.oauth.enabled": "true"})

	// RFC 9728: the resource names itself and its authorization server, as a
	// bare document — the reader is an OAuth library, not this product's SPA.
	request := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)
	request.Host = "weekly.example.test"
	request.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	server.app.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata: %d %s", recorder.Code, recorder.Body.String())
	}
	var metadata struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		Scopes               []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &metadata); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	if metadata.Resource != "https://weekly.example.test/mcp" {
		t.Errorf("resource %q, want the endpoint behind the proxy's scheme", metadata.Resource)
	}
	if len(metadata.AuthorizationServers) != 1 || metadata.AuthorizationServers[0] != idp.server.URL {
		t.Errorf("authorization servers %v, want the configured issuer", metadata.AuthorizationServers)
	}
	if len(metadata.Scopes) == 0 {
		t.Error("no scopes advertised, so a client cannot ask for any")
	}

	// And the 401 now carries the pointer. Without it the client has no way
	// to discover the document above, and the refusal is a dead end.
	refusal := mcpWith(server, "", listTools)
	if refusal.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", refusal.Code)
	}
	header := refusal.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(header, "Bearer ") || !strings.Contains(header, `resource_metadata="http://weekly.example.test/.well-known/oauth-protected-resource/mcp"`) {
		t.Errorf("WWW-Authenticate %q does not point at the metadata", header)
	}
}

// guards: oauthPrincipal
func TestAKeycloakTokenOpensMCPForAnAccountWeeklyKnows(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly-web", "", nil)
	server.useIDP(t, idp, "weekly-web", map[string]string{"mcp.oauth.enabled": "true"})
	server.createUser("ssomember", "USER", nil)
	username := server.lastCreatedUsername("ssomember")

	token := accessToken(t, idp, "http://weekly.example.test/mcp", map[string]any{"preferred_username": username})
	opened := mcpWith(server, token, listTools)
	if opened.Code != http.StatusOK {
		t.Fatalf("a token for this resource was refused: %d %s", opened.Code, opened.Body.String())
	}
	if !strings.Contains(opened.Body.String(), "weekly_reports_search") {
		t.Errorf("the token opened MCP but the listing is empty: %s", opened.Body.String())
	}

	// Measured against a real Keycloak 26: an access token issued to a client
	// carries `aud: ["account"]` and the client in `azp` — the client id is
	// not in aud, whatever an ID token does. A token minted for Weekly's own
	// web client is therefore recognised by azp, so a deployment does not need
	// an Audience mapper before the first connection works.
	viaClient := mcpWith(server, accessToken(t, idp, "account", map[string]any{"preferred_username": username, "azp": "weekly-web"}), listTools)
	if viaClient.Code != http.StatusOK {
		t.Errorf("a token issued to the web client was refused: %d %s", viaClient.Code, viaClient.Body.String())
	}
	// And a token issued to some other application in the same realm is not
	// ours, however real its signature — that is the passthrough RFC 8707
	// exists to stop.
	other := mcpWith(server, accessToken(t, idp, "account", map[string]any{"preferred_username": username, "azp": "some-other-app"}), listTools)
	if other.Code != http.StatusUnauthorized {
		t.Errorf("a token issued to another application opened MCP: %d %s", other.Code, other.Body.String())
	}

	// Read only, like a key: the token is a person's identity carried by a
	// program, and the program is what the rule is about.
	write := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(`{"weekStart":"2026-03-02","summary":"x"}`))
	write.Header.Set("Content-Type", "application/json")
	write.Header.Set("Authorization", "Bearer "+token)
	write.Host = "weekly.example.test"
	recorder := httptest.NewRecorder()
	server.app.mux.ServeHTTP(recorder, write)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("an SSO token wrote a report: %d %s", recorder.Code, recorder.Body.String())
	}
}

// guards: oauthPrincipal
func TestATokenIsRefusedForTheRightReason(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly-web", "", nil)
	server.useIDP(t, idp, "weekly-web", map[string]string{"mcp.oauth.enabled": "true"})
	server.createUser("ssoknown", "USER", nil)
	known := server.lastCreatedUsername("ssoknown")
	resource := "http://weekly.example.test/mcp"

	refusedWith := func(what, bearer, want string) {
		t.Helper()
		response := mcpWith(server, bearer, listTools)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", what, response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("%s: the refusal does not say %q: %s", what, want, response.Body.String())
		}
	}

	// A token for somebody else's resource. Accepting it would let any
	// Keycloak token in the realm — issued to any application — open Weekly.
	refusedWith("wrong audience",
		accessToken(t, idp, "https://other-app.example.test", map[string]any{"preferred_username": known}), "발급된 것이 아닙니다")

	// A person Keycloak knows and Weekly does not. The web sign-in would
	// provision them; a program presenting a token is not that moment.
	refusedWith("unknown account",
		accessToken(t, idp, resource, map[string]any{"preferred_username": "nobody-here", "sub": "stranger"}), "등록되지 않았")

	// An expired token, and one from a different issuer's key.
	refusedWith("expired",
		accessToken(t, idp, resource, map[string]any{"preferred_username": known, "exp": time.Now().Add(-time.Minute).Unix()}), "유효하지 않습니다")
	other := newIDP(t, "weekly-web", "", nil)
	refusedWith("foreign signature",
		accessToken(t, other, resource, map[string]any{"preferred_username": known, "iss": idp.server.URL}), "유효하지 않습니다")

	// Not a JWT at all — say so rather than "형식이 아닙니다" about a key.
	refusedWith("garbage", "not-a-token", "JWT")

	// An administrator may list a client or audience instead of adding a
	// mapper — the plain path, and it applies to azp as well as aud.
	server.useIDP(t, idp, "weekly-web", map[string]string{
		"mcp.oauth.enabled": "true", "mcp.oauth.audience": "https://other-app.example.test mcp-client",
	})
	allowed := mcpWith(server, accessToken(t, idp, "https://other-app.example.test", map[string]any{"preferred_username": known}), listTools)
	if allowed.Code != http.StatusOK {
		t.Errorf("an audience the administrator listed was refused: %d %s", allowed.Code, allowed.Body.String())
	}
	listedClient := mcpWith(server, accessToken(t, idp, "account", map[string]any{"preferred_username": known, "azp": "mcp-client"}), listTools)
	if listedClient.Code != http.StatusOK {
		t.Errorf("a client the administrator listed was refused: %d %s", listedClient.Code, listedClient.Body.String())
	}

	// Switched off again, a token is refused — and the message says the
	// switch exists, because the person reading it is the one who can flip it.
	server.useIDP(t, idp, "weekly-web", map[string]string{"mcp.oauth.enabled": "false"})
	refusedWith("disabled", accessToken(t, idp, resource, map[string]any{"preferred_username": known}), "SSO")
}
