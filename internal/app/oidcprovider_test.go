package app

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// A provider that answers, so the sign-in path behind it can be run.
//
// Two authorisation refusals have sat on the unguarded list since v0.129, both
// recorded as needing "a verified OIDC provider". One of them turned out not to
// (see localloginoff_test.go). This is the other, and everything behind it:
// which claim becomes a username, which group makes somebody an administrator,
// and whether a stranger is let in at all. That code decides who holds the keys
// on an SSO deployment and had never executed.

type fakeIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	claims map[string]any
	nonce  string
	// redirectURI is what the deployment told the provider to come back to.
	// With no redirect URL configured it is derived from the request, and the
	// derivation is only visible from here.
	redirectURI string
}

// newIDP serves discovery, JWKS and a token endpoint, and signs an ID token
// carrying claims. The nonce is filled in per exchange from the login this
// deployment started, because the handler refuses a token that answers a
// different request.
func newIDP(t *testing.T, clientID, nonce string, claims map[string]any) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIDP{key: key, claims: claims, nonce: nonce}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.server.URL,
			"authorization_endpoint":                idp.server.URL + "/authorize",
			"token_endpoint":                        idp.server.URL + "/token",
			"jwks_uri":                              idp.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: key.Public(), KeyID: "test", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		idp.redirectURI = r.Form.Get("redirect_uri")
		payload := map[string]any{
			"iss": idp.server.URL, "aud": clientID, "sub": "subject-1",
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"nonce": idp.nonce,
		}
		for key, value := range idp.claims {
			payload[key] = value
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer",
			"id_token": idp.sign(t, payload),
		})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *fakeIDP) sign(t *testing.T, payload map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: idp.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(encoded)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// signInThrough drives one OIDC sign-in: the login state this deployment would
// have stored, then the callback the provider would have redirected to.
func (s *testServer) signInThrough(t *testing.T, idp *fakeIDP, state, nonce string) *httptest.ResponseRecorder {
	t.Helper()
	if _, err := s.app.db.Exec(s.ctx(),
		`INSERT INTO oidc_login_states(state_hash, nonce, pkce_verifier, expires_at)
			VALUES($1, $2, 'verifier-for-the-test', now() + interval '10 minutes')`,
		tokenHash(state), nonce); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/oidc/callback?state="+state+"&code=any-code", nil)
	request.AddCookie(&http.Cookie{Name: "weekly_oidc_state", Value: tokenHash(state)})
	recorder := httptest.NewRecorder()
	s.app.mux.ServeHTTP(recorder, request)
	return recorder
}

// useIDP points the deployment at idp and turns single sign-on on.
func (s *testServer) useIDP(t *testing.T, idp *fakeIDP, clientID string, extra map[string]string) {
	t.Helper()
	settings := map[string]string{
		"oidc.enabled": "true", "oidc.issuer_url": idp.server.URL,
		"oidc.client_id": clientID, "oidc.client_secret": "SsoClientSecret1234",
		"oidc.redirect_url": "http://example.test/api/v1/auth/oidc/callback",
		"oidc.scopes":       "openid profile email",
	}
	for key, value := range extra {
		settings[key] = value
	}
	if on := s.request(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"settings": settings}, s.admin); on.Code != http.StatusOK {
		t.Fatalf("OIDC 설정을 저장하지 못했습니다: %d %s", on.Code, on.Body.String())
	}
}
