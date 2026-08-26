package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// guards: oidcCallback
//
// The refusal this exercises has been on the unguarded list since v0.129.
// Auto-provisioning off means the deployment keeps its own roll: somebody the
// identity provider vouches for is still a stranger here until an administrator
// says otherwise. Remove the refusal and every account the provider knows walks
// in, which on a corporate directory is everybody.
func TestSomebodyTheProviderKnowsIsStillAStrangerHere(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", map[string]any{
		"preferred_username": "unknown_person", "name": "모르는 사람", "email": "x@example.com",
	})
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "false"})

	reply := server.signInThrough(t, idp, "state-one", "the-nonce")
	if reply.Code == 200 {
		t.Fatalf("등록되지 않은 사람이 들어왔습니다: %s", reply.Body.String())
	}
	if code := errorCode(reply); code != "OIDC_USER_NOT_PROVISIONED" {
		t.Fatalf("코드가 %q 입니다: %d %s", code, reply.Code, reply.Body.String())
	}

	// And nothing was created on the way out.
	var accounts int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM users WHERE username = 'unknown_person'`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 {
		t.Errorf("거부된 로그인이 계정 %d개를 만들었습니다", accounts)
	}
}

// guards: oidcCallback
//
// With provisioning on, the same person is admitted — and the claim the
// administrator nominated is what their account is named after. Checking only
// the refusal above would let the whole branch be switched off.
func TestWithProvisioningOnTheNominatedClaimBecomesTheAccount(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", map[string]any{
		"preferred_username": "새사람", "name": "새 사람", "email": "new@example.com",
	})
	server.useIDP(t, idp, "weekly", map[string]string{
		"oidc.auto_provision": "true", "oidc.username_claim": "preferred_username",
	})

	if reply := server.signInThrough(t, idp, "state-two", "the-nonce"); reply.Code != 200 && reply.Code != 302 && reply.Code != 303 {
		t.Fatalf("등록이 켜져 있는데 들어오지 못했습니다: %d %s", reply.Code, reply.Body.String())
	}

	var role string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT role FROM users WHERE username = '새사람'`).Scan(&role); err != nil {
		t.Fatalf("지목한 청구항으로 계정이 만들어지지 않았습니다: %v", err)
	}
	// A new arrival is a writer, not an administrator, until somebody says so.
	if role != "USER" {
		t.Errorf("새로 등록된 계정의 역할이 %q 입니다", role)
	}
}

// guards: oidcCallback
//
// Who holds the keys. One claim, one configured group name, and a person the
// directory has never met arrives as an administrator. Both halves of the
// condition matter: no group configured must promote nobody, and a member of
// some other group must stay a writer. Every fixture that puts an admin against
// a stranger settles it on the first half alone.
func TestOnlyTheNominatedGroupArrivesAsAnAdministrator(t *testing.T) {
	for index, arrival := range []struct {
		name        string
		adminGroup  string
		groups      []string
		wantRole    string
		description string
	}{
		{"지목된 무리에 속함", "weekly-admins", []string{"everyone", "weekly-admins"}, "ADMIN", "관리자로 들어와야 합니다"},
		{"다른 무리에 속함", "weekly-admins", []string{"everyone", "sales"}, "USER", "평사원이어야 합니다"},
		{"무리가 없음", "weekly-admins", []string{}, "USER", "평사원이어야 합니다"},
		{"관리자 무리를 지정하지 않음", "", []string{"weekly-admins"}, "USER", "지정이 없으면 아무도 승격되지 않아야 합니다"},
	} {
		t.Run(arrival.name, func(t *testing.T) {
			server := newTestServer(t)
			idp := newIDP(t, "weekly", "the-nonce", map[string]any{
				"preferred_username": "도착한사람", "name": "도착한 사람",
				"email": "arrival@example.com", "groups": arrival.groups,
			})
			server.useIDP(t, idp, "weekly", map[string]string{
				"oidc.auto_provision": "true", "oidc.username_claim": "preferred_username",
				"oidc.groups_claim": "groups", "oidc.admin_group": arrival.adminGroup,
			})

			if reply := server.signInThrough(t, idp, fmt.Sprintf("state-group-%d", index), "the-nonce"); reply.Code >= 400 {
				t.Fatalf("들어오지 못했습니다: %d %s", reply.Code, reply.Body.String())
			}
			var role string
			if err := server.app.db.QueryRow(server.ctx(),
				`SELECT role FROM users WHERE username = '도착한사람'`).Scan(&role); err != nil {
				t.Fatal(err)
			}
			if role != arrival.wantRole {
				t.Errorf("%s — %s, 그런데 %q 입니다", arrival.name, arrival.description, role)
			}
		})
	}
}

// guards: oidcCallback
//
// The callback is the moment a stranger's browser hands this deployment a code
// and says it belongs to a login somebody here started. Four things are checked
// before that is believed, and any one of them alone is the difference between
// finishing your own sign-in and finishing somebody else's. Nothing had run any
// of them: a malformed callback had never been sent.
func TestACallbackThatCannotBeMatchedToALoginIsRefused(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", map[string]any{
		"preferred_username": "침입자", "name": "침입자", "email": "x@example.com",
	})
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true"})

	// A login this deployment really started, so the only thing wrong with each
	// attempt below is the thing being tested.
	state := "state-callback"
	if _, err := server.app.db.Exec(server.ctx(),
		`INSERT INTO oidc_login_states(state_hash, nonce, pkce_verifier, expires_at)
			VALUES($1, 'the-nonce', 'verifier-for-the-test', now() + interval '10 minutes')`,
		tokenHash(state)); err != nil {
		t.Fatal(err)
	}

	call := func(query string, cookie *http.Cookie) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback"+query, nil)
		if cookie != nil {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, request)
		return recorder
	}
	matching := &http.Cookie{Name: "weekly_oidc_state", Value: tokenHash(state)}

	for _, attempt := range []struct {
		name   string
		query  string
		cookie *http.Cookie
	}{
		{"상태 없음", "?code=any-code", matching},
		{"코드 없음", "?state=" + state, matching},
		{"쿠키 없음", "?state=" + state + "&code=any-code", nil},
		{"다른 로그인의 쿠키", "?state=" + state + "&code=any-code",
			&http.Cookie{Name: "weekly_oidc_state", Value: tokenHash("somebody-elses-state")}},
	} {
		w := call(attempt.query, attempt.cookie)
		if w.Code != http.StatusBadRequest || errorCode(w) != "OIDC_INVALID_CALLBACK" {
			t.Errorf("%s: %d %s", attempt.name, w.Code, w.Body.String())
		}
	}

	// None of the refusals may have let anybody in.
	var accounts int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM users WHERE username = '침입자'`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 {
		t.Errorf("거부된 콜백이 계정 %d개를 만들었습니다", accounts)
	}

	// And the login that was started is still usable — the refusals must not
	// have consumed it.
	if ok := server.signInThrough(t, idp, "state-after", "the-nonce"); ok.Code >= 400 {
		t.Errorf("거부들 뒤에 정상 로그인이 막혔습니다: %d %s", ok.Code, ok.Body.String())
	}
}

// guards: oidcCallback
//
// With no redirect URL configured the deployment works one out from the request
// it is answering, and the provider is told to come back there. Get the scheme
// wrong and the token exchange is refused by any provider that checks it — the
// sign-in fails at the last step with nothing on screen to explain it.
func TestWithNoRedirectConfiguredTheSchemeComesFromTheRequest(t *testing.T) {
	for _, arrival := range []struct {
		name       string
		forwarded  string
		wantPrefix string
	}{
		{"평문으로 들어옴", "", "http://"},
		{"프록시가 https라고 알림", "https", "https://"},
	} {
		t.Run(arrival.name, func(t *testing.T) {
			server := newTestServer(t)
			idp := newIDP(t, "weekly", "the-nonce", map[string]any{
				"preferred_username": "돌아온사람", "name": "돌아온 사람", "email": "back@example.com",
			})
			// No oidc.redirect_url: that is the branch under test.
			server.useIDP(t, idp, "weekly", map[string]string{
				"oidc.auto_provision": "true", "oidc.redirect_url": "",
			})

			state := "state-scheme-" + arrival.forwarded
			if _, err := server.app.db.Exec(server.ctx(),
				`INSERT INTO oidc_login_states(state_hash, nonce, pkce_verifier, expires_at)
					VALUES($1, 'the-nonce', 'verifier-for-the-test', now() + interval '10 minutes')`,
				tokenHash(state)); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/auth/oidc/callback?state="+state+"&code=any-code", nil)
			request.AddCookie(&http.Cookie{Name: "weekly_oidc_state", Value: tokenHash(state)})
			if arrival.forwarded != "" {
				request.Header.Set("X-Forwarded-Proto", arrival.forwarded)
			}
			recorder := httptest.NewRecorder()
			server.app.mux.ServeHTTP(recorder, request)
			if recorder.Code >= 400 {
				t.Fatalf("%s: 로그인이 실패했습니다 %d %s", arrival.name, recorder.Code, recorder.Body.String())
			}
			if !strings.HasPrefix(idp.redirectURI, arrival.wantPrefix) {
				t.Errorf("%s: 제공자에게 알린 주소가 %q 입니다, %q 로 시작해야 합니다",
					arrival.name, idp.redirectURI, arrival.wantPrefix)
			}
		})
	}
}
