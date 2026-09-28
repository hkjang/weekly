package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// 자동 로그인이 탭을 보내기 전에 Keycloak 에 닿아 보려면, 그 문서의 CSP 가
// Keycloak 한 곳과의 연결을 허락해야 합니다. 그 한 곳만, 그 문서에서만, 자동
// 로그인이 켜졌을 때만.
//
// guards: documentConnectOrigin, silentIssuer
func TestOnlyTheSPADocumentMayReachKeycloakAndOnlyForSilentSignIn(t *testing.T) {
	server := newTestServer(t)
	server.app.web = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Weekly</title>")}}
	idp := newIDP(t, "weekly-web", "", nil)
	origin := oidcIssuerOrigin(idp.server.URL)

	documentCSP := func() string {
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		return recorder.Header().Get("Content-Security-Policy")
	}
	apiCSP := func() string {
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil))
		return recorder.Header().Get("Content-Security-Policy")
	}
	providers := func() map[string]any {
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil))
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", recorder.Body.String(), err)
		}
		return envelope.Data
	}

	// SSO on, silent sign-in off: nothing to probe, so nothing is widened and
	// the issuer is not published.
	server.useIDP(t, idp, "weekly-web", map[string]string{"oidc.auto_login": "false"})
	if strings.Contains(documentCSP(), origin) {
		t.Errorf("the document may reach Keycloak with silent sign-in off: %s", documentCSP())
	}
	if issuer, _ := providers()["oidcIssuer"].(string); issuer != "" {
		t.Errorf("the issuer is published with silent sign-in off: %q", issuer)
	}

	// Silent sign-in on: the document may connect to that one origin, and the
	// providers answer says where it is.
	server.useIDP(t, idp, "weekly-web", map[string]string{"oidc.auto_login": "true"})
	csp := documentCSP()
	if !strings.Contains(csp, "connect-src 'self' "+origin+";") {
		t.Errorf("the document cannot reach the issuer it is about to be sent to: %s", csp)
	}
	if issuer, _ := providers()["oidcIssuer"].(string); issuer != strings.TrimRight(idp.server.URL, "/") {
		t.Errorf("providers publish issuer %q, want %q", issuer, idp.server.URL)
	}

	// And nothing else widens: an API response keeps the narrow policy.
	if strings.Contains(apiCSP(), origin) {
		t.Errorf("an API response carries the widened policy: %s", apiCSP())
	}

	// Only an http(s) origin is ever added — a malformed issuer adds nothing
	// rather than something a policy parser would read unexpectedly.
	for issuer, want := range map[string]string{
		"https://sso.example.com/realms/w": "https://sso.example.com",
		"http://kc:8080/realms/w/":         "http://kc:8080",
		"javascript:alert(1)":              "",
		"sso.example.com/realms/w":         "",
		"":                                 "",
	} {
		if got := oidcIssuerOrigin(issuer); got != want {
			t.Errorf("origin of %q is %q, want %q", issuer, got, want)
		}
	}
}
