package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// guards: oidcStart
func TestPromptNoneIsUsedOnlyForSilentOIDCStarts(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "unused-for-start", map[string]any{})
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true", "oidc.auto_login": "true"})

	for _, attempt := range []struct {
		name         string
		silent       bool
		returnTo     string
		wantPrompt   string
		wantReturnTo string
	}{
		{"interactive", false, "#/history?report=17", "", "#/history?report=17"},
		{"silent", true, "#/history?report=17", "none", "#/history?report=17"},
		{"unsafe return", true, "https://outside.example/steal", "none", ""},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			query := url.Values{"returnTo": []string{attempt.returnTo}}
			if attempt.silent {
				query.Set("silent", "1")
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/start?"+query.Encode(), nil)
			recorder := httptest.NewRecorder()
			server.app.mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusFound {
				t.Fatalf("start = %d %s", recorder.Code, recorder.Body.String())
			}

			target, err := url.Parse(recorder.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if got := target.Query().Get("prompt"); got != attempt.wantPrompt {
				t.Errorf("prompt = %q, want %q", got, attempt.wantPrompt)
			}
			state := target.Query().Get("state")
			if state == "" {
				t.Fatal("authorization redirect has no state")
			}
			var storedSilent bool
			var storedReturnTo string
			if err := server.app.db.QueryRow(server.ctx(), `SELECT silent, return_to
				FROM oidc_login_states WHERE state_hash=$1`, tokenHash(state)).
				Scan(&storedSilent, &storedReturnTo); err != nil {
				t.Fatal(err)
			}
			if storedSilent != attempt.silent || storedReturnTo != attempt.wantReturnTo {
				t.Errorf("stored silent/return = %v %q, want %v %q",
					storedSilent, storedReturnTo, attempt.silent, attempt.wantReturnTo)
			}
		})
	}
}

// The silent attempt is asked for in a URL, so anyone can ask. Whether it
// happens belongs to the administrator: with oidc.auto_login off — the
// default — ?silent=1 is an ordinary login, in the redirect it produces and
// in how its failures are reported. Otherwise a visitor could add one query
// parameter and turn an error page into a redirect back to the SPA.
//
// guards: oidcStart, oidcAutoLogin=100, authProviders=100
func TestSilentOIDCStartRequiresTheAutoLoginSetting(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "unused-for-start", map[string]any{})
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true"})

	providers := func() map[string]any {
		w := server.request(http.MethodGet, "/api/v1/auth/providers", nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("providers = %d %s", w.Code, w.Body.String())
		}
		return decodeData(t, w)
	}
	silentStart := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/start?silent=1&returnTo=%23%2Fcurrent", nil)
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, request)
		return recorder
	}
	storedSilent := func(recorder *httptest.ResponseRecorder) bool {
		t.Helper()
		target, err := url.Parse(recorder.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		var silent bool
		if err := server.app.db.QueryRow(server.ctx(), `SELECT silent FROM oidc_login_states WHERE state_hash=$1`,
			tokenHash(target.Query().Get("state"))).Scan(&silent); err != nil {
			t.Fatal(err)
		}
		return silent
	}

	// Never saved: the default is off, and the browser is told so.
	if got := providers(); got["oidc"] != true || got["autoLogin"] != false {
		t.Fatalf("default providers = %v, want oidc on and autoLogin off", got)
	}
	recorder := silentStart()
	if recorder.Code != http.StatusFound {
		t.Fatalf("start = %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Header().Get("Location"), "prompt=none") {
		t.Errorf("auto_login off, yet the provider is asked with prompt=none: %s", recorder.Header().Get("Location"))
	}
	if storedSilent(recorder) {
		t.Error("auto_login off, yet the state row is marked silent — its callback would return to the SPA instead of reporting")
	}

	// Switched on by the administrator, the same URL becomes the silent attempt.
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_login": "true"})
	if got := providers(); got["autoLogin"] != true {
		t.Fatalf("auto_login on, providers = %v", got)
	}
	recorder = silentStart()
	if recorder.Code != http.StatusFound || !strings.Contains(recorder.Header().Get("Location"), "prompt=none") {
		t.Fatalf("auto_login on, start = %d %s", recorder.Code, recorder.Header().Get("Location"))
	}
	if !storedSilent(recorder) {
		t.Error("auto_login on, yet the state row is not marked silent")
	}

	// With OIDC off altogether, autoLogin is off no matter what was saved,
	// and a silent start reports the missing configuration like any other
	// caller instead of bouncing the browser back to the SPA.
	if off := server.request(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"settings": map[string]string{"oidc.enabled": "false"}}, server.admin); off.Code != http.StatusOK {
		t.Fatalf("OIDC 를 끄지 못했습니다: %d %s", off.Code, off.Body.String())
	}
	if got := providers(); got["oidc"] != false || got["autoLogin"] != false {
		t.Fatalf("OIDC off, providers = %v", got)
	}
	// The saved auto_login=true must not make ?silent=1 a redirect either.
	if recorder = silentStart(); recorder.Code != http.StatusServiceUnavailable || errorCode(recorder) != "OIDC_UNAVAILABLE" {
		t.Fatalf("OIDC off with auto_login saved on: start = %d %s %s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
}

// The start half of what oidclogin_test.go checks for the callback: with no
// oidc.redirect_url saved, the callback address the provider is told comes
// from the request — its host, and https only when the proxy says so — and
// the state cookie is marked Secure on the same evidence. A cookie the browser
// refuses to send back is a login that always fails at the callback.
//
// guards: oidcStart
func TestOIDCStartDerivesTheCallbackAndCookieFromTheRequest(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "unused-for-start", map[string]any{})
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.redirect_url": ""})

	for _, arrival := range []struct {
		name       string
		forwarded  string
		wantSecure bool
		wantPrefix string
	}{
		{"평문으로 들어옴", "", false, "http://weekly.test/"},
		{"프록시가 https라고 알림", "https", true, "https://weekly.test/"},
	} {
		t.Run(arrival.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://weekly.test/api/v1/auth/oidc/start", nil)
			if arrival.forwarded != "" {
				request.Header.Set("X-Forwarded-Proto", arrival.forwarded)
			}
			recorder := httptest.NewRecorder()
			server.app.mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusFound {
				t.Fatalf("start = %d %s", recorder.Code, recorder.Body.String())
			}
			target, err := url.Parse(recorder.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if got := target.Query().Get("redirect_uri"); !strings.HasPrefix(got, arrival.wantPrefix) || !strings.HasSuffix(got, "/api/v1/auth/oidc/callback") {
				t.Errorf("제공자에게 알린 주소가 %q 입니다, %q 로 시작해 callback 으로 끝나야 합니다", got, arrival.wantPrefix)
			}
			var stateCookie *http.Cookie
			for _, cookie := range recorder.Result().Cookies() {
				if cookie.Name == "weekly_oidc_state" {
					stateCookie = cookie
				}
			}
			if stateCookie == nil {
				t.Fatal("state cookie 가 없습니다")
			}
			if stateCookie.Secure != arrival.wantSecure {
				t.Errorf("state cookie Secure = %v, want %v", stateCookie.Secure, arrival.wantSecure)
			}
		})
	}
}

func insertOIDCCallbackState(t *testing.T, server *testServer, state string, silent bool, returnTo string) {
	t.Helper()
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO oidc_login_states
		(state_hash, nonce, pkce_verifier, expires_at, silent, return_to)
		VALUES($1, 'nonce', 'verifier', now()+interval '10 minutes', $2, $3)`,
		tokenHash(state), silent, returnTo); err != nil {
		t.Fatal(err)
	}
}

func oidcErrorCallback(server *testServer, state, providerError string, cookie *http.Cookie) *httptest.ResponseRecorder {
	query := url.Values{"state": []string{state}}
	if providerError != "" {
		query.Set("error", providerError)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?"+query.Encode(), nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	server.app.mux.ServeHTTP(recorder, request)
	return recorder
}

func oidcStateCount(t *testing.T, server *testServer, state string) int {
	t.Helper()
	var count int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM oidc_login_states WHERE state_hash=$1`, tokenHash(state)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// guards: oidcCallback
func TestSilentProviderErrorsReturnToTheLoginScreenAndConsumeState(t *testing.T) {
	server := newTestServer(t)
	for index, attempt := range []struct {
		name       string
		silent     bool
		provider   string
		wantStatus int
		wantTarget string
		wantCode   string
	}{
		{"no Keycloak session", true, "login_required", http.StatusFound, "/?oidc_auto=miss#/current", ""},
		{"interaction required", true, "interaction_required", http.StatusFound, "/?oidc_auto=miss#/current", ""},
		{"consent required", true, "consent_required", http.StatusFound, "/?oidc_auto=miss#/current", ""},
		{"account choice required", true, "account_selection_required", http.StatusFound, "/?oidc_auto=miss#/current", ""},
		{"unexpected provider failure", true, "server_error", http.StatusFound, "/?oidc_auto=unavailable#/current", ""},
		{"interactive refusal", false, "access_denied", http.StatusUnauthorized, "", "OIDC_PROVIDER_ERROR"},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			state := "provider-error-" + string(rune('a'+index))
			insertOIDCCallbackState(t, server, state, attempt.silent, "#/current")
			cookie := &http.Cookie{Name: "weekly_oidc_state", Value: tokenHash(state)}
			recorder := oidcErrorCallback(server, state, attempt.provider, cookie)
			if recorder.Code != attempt.wantStatus {
				t.Fatalf("callback = %d %s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get("Location"); got != attempt.wantTarget {
				t.Errorf("Location = %q, want %q", got, attempt.wantTarget)
			}
			if got := errorCode(recorder); got != attempt.wantCode {
				t.Errorf("error code = %q, want %q", got, attempt.wantCode)
			}
			if count := oidcStateCount(t, server, state); count != 0 {
				t.Errorf("provider outcome left %d state rows", count)
			}

			// A provider outcome is one-time even if a browser repeats its callback.
			replay := oidcErrorCallback(server, state, attempt.provider, cookie)
			if replay.Code != http.StatusBadRequest || errorCode(replay) != "OIDC_STATE_EXPIRED" {
				t.Errorf("replayed callback = %d %s", replay.Code, replay.Body.String())
			}
		})
	}
}

// guards: oidcCallback
func TestMalformedOrUnmatchedCallbacksDoNotConsumeState(t *testing.T) {
	server := newTestServer(t)
	state := "state-preserved-for-real-provider-answer"
	insertOIDCCallbackState(t, server, state, true, "#/dashboard")

	wrongCookie := &http.Cookie{Name: "weekly_oidc_state", Value: tokenHash("somebody-else")}
	if recorder := oidcErrorCallback(server, state, "login_required", wrongCookie); recorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong cookie = %d %s", recorder.Code, recorder.Body.String())
	}
	if count := oidcStateCount(t, server, state); count != 1 {
		t.Fatalf("wrong cookie consumed state, count = %d", count)
	}

	matching := &http.Cookie{Name: "weekly_oidc_state", Value: tokenHash(state)}
	if recorder := oidcErrorCallback(server, state, "", matching); recorder.Code != http.StatusBadRequest || errorCode(recorder) != "OIDC_INVALID_CALLBACK" {
		t.Fatalf("blank callback = %d %s", recorder.Code, recorder.Body.String())
	}
	if count := oidcStateCount(t, server, state); count != 1 {
		t.Fatalf("blank callback consumed state, count = %d", count)
	}

	if recorder := oidcErrorCallback(server, state, "login_required", matching); recorder.Code != http.StatusFound {
		t.Fatalf("real provider answer after malformed calls = %d %s", recorder.Code, recorder.Body.String())
	}
	if count := oidcStateCount(t, server, state); count != 0 {
		t.Fatalf("real provider answer did not consume state, count = %d", count)
	}
}

// guards: safeOIDCReturnTo=100
func TestOIDCReturnToCannotLeaveTheSPAHash(t *testing.T) {
	for _, candidate := range []struct {
		value string
		want  string
	}{
		{"#/history?report=17", "#/history?report=17"},
		{"  #/dashboard  ", "#/dashboard"},
		{"", ""},
		{"https://outside.example/steal", ""},
		{"//outside.example/steal", ""},
		{"#/bad\\path", ""},
		{"#/bad\r\nLocation: https://outside.example", ""},
		{"#/bad\tpath", ""},
		{"#/" + strings.Repeat("a", 2048), ""},
	} {
		if got := safeOIDCReturnTo(candidate.value); got != candidate.want {
			t.Errorf("safeOIDCReturnTo(%q) = %q, want %q", candidate.value, got, candidate.want)
		}
	}
}

// guards: oidcCallback
func TestSuccessfulOIDCLoginReturnsToTheStoredHash(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-return-nonce", map[string]any{
		"preferred_username": "돌아갈사람", "name": "돌아갈 사람", "email": "return@example.com",
	})
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true"})
	state := "state-with-return-route"
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO oidc_login_states
		(state_hash, nonce, pkce_verifier, expires_at, silent, return_to)
		VALUES($1, 'the-return-nonce', 'verifier-for-the-test', now()+interval '10 minutes', true, '#/history?report=17')`,
		tokenHash(state)); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/oidc/callback?state="+state+"&code=any-code", nil)
	request.AddCookie(&http.Cookie{Name: "weekly_oidc_state", Value: tokenHash(state)})
	recorder := httptest.NewRecorder()
	server.app.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("callback = %d %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != "/#/history?report=17" {
		t.Errorf("Location = %q", got)
	}
}
