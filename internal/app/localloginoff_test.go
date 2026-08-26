package app

import (
	"net/http"
	"testing"
)

// This refusal has been on the unguarded list since v0.129, recorded three
// times as needing a verified identity provider. Reading what "verified" means
// here says otherwise: the check that stands between an administrator and a
// deployment nobody can enter reads settings and makes no network call. The
// provider was never needed to reach it — only to believe it.

// guards: login
func TestWithLocalLoginTurnedOffThePasswordFormIsRefusedAndSaysWhy(t *testing.T) {
	server := newTestServer(t)
	server.createUser("shut_out", "USER", nil)
	username := server.lastCreatedUsername("shut_out")
	password := server.passwordFor("shut_out")

	// It works while passwords are the way in.
	if !server.canSignIn(username, password) {
		t.Fatal("로컬 로그인이 켜져 있는데 들어가지 못합니다")
	}

	// Turning it off requires somewhere else to go, which is the rule
	// v0.14x guards. Configure that first.
	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"oidc.enabled": "true", "oidc.issuer_url": "https://sso.internal",
			"oidc.client_id": "weekly", "oidc.client_secret": "SsoClientSecret1234",
			"oidc.redirect_url": "https://weekly.internal/oidc/callback",
			"oidc.scopes":       "openid profile email",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("OIDC 설정을 저장하지 못했습니다: %d %s", on.Code, on.Body.String())
	}
	if off := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"auth.local_enabled": "false"},
	}, server.admin); off.Code != http.StatusOK {
		t.Fatalf("로컬 로그인을 끄지 못했습니다: %d %s", off.Code, off.Body.String())
	}

	// Now the password form is closed, and the person in front of it has to be
	// told which door to use instead — a bare refusal leaves them retyping a
	// password that is correct.
	w := server.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password}, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("로컬 로그인이 꺼졌는데 %d 로 답합니다: %s", w.Code, w.Body.String())
	}
	if code := errorCode(w); code != "LOCAL_LOGIN_DISABLED" {
		t.Errorf("코드가 %q 입니다: %s", code, w.Body.String())
	}
	if !containsAny(w.Body.String(), "비활성화") {
		t.Errorf("왜 거부됐는지 말하지 않습니다: %s", w.Body.String())
	}

	// And a wrong password must not be told anything different — the closed
	// door is a fact about the deployment, not about the credentials.
	wrong := server.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": "WrongPassword1234"}, nil)
	if errorCode(wrong) != "LOCAL_LOGIN_DISABLED" {
		t.Errorf("틀린 비밀번호에는 다르게 답합니다: %s", wrong.Body.String())
	}
}
