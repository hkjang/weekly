package app

import (
	"fmt"
	"net/http"
	"testing"
)

// The most important sentence this product can say is "no". Nothing tested it.
//
// scripts/mutation-check.py found this: turning the || in login's credential
// check into && lets any password through, and the whole suite still passed.
// Every existing login test signs in correctly or exercises an outage; none had
// ever handed over a wrong password and watched it be refused.
//
// guards: login
//
// Not login=100: the throttle delay and the store-outage branch belong to
// TestLoginSaysTheStoreIsDownRatherThanBlamingThePassword and the throttle
// tests. Demanding every branch from one test would mean writing a test about
// the function rather than about the rule.
func TestAWrongPasswordIsRefusedAndARightOneIsNot(t *testing.T) {
	server := newTestServer(t)
	const stem = "cred_user"
	cookie := server.createUser(stem, "USER", nil)
	if cookie == nil {
		t.Fatal("the account was not created")
	}
	var username string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT username FROM users WHERE id=(SELECT user_id FROM user_sessions WHERE token_hash=$1)`,
		tokenHash(cookie.Value)).Scan(&username); err != nil {
		t.Fatal(err)
	}
	password := username + "Password1234"

	// The positive control first. Without it a refusal proves only that this
	// account cannot sign in at all.
	if right := server.attemptLogin(username, password); right.Code != http.StatusOK {
		t.Fatalf("the correct password was refused: %d %s", right.Code, right.Body.String())
	}

	wrong := []struct{ name, password string }{
		{"한 글자 다름", password + "5"},
		{"완전히 다름", "CompletelyOther9999"},
		{"빈 비밀번호", ""},
		{"아이디를 비밀번호로", username},
	}
	for _, item := range wrong {
		w := server.attemptLogin(username, item.password)
		if w.Code == http.StatusOK {
			t.Errorf("%s: signed in with the wrong password", item.name)
			continue
		}
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusBadRequest && w.Code != http.StatusTooManyRequests {
			t.Errorf("%s: answered %d — %s", item.name, w.Code, w.Body.String())
		}
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == sessionCookie && cookie.Value != "" {
				t.Errorf("%s: a session cookie was issued anyway", item.name)
			}
		}
	}

	// An account that does not exist must read the same as a wrong password, or
	// the difference tells an attacker which names are real.
	missing := server.attemptLogin(username+"_nope", password)
	if missing.Code == http.StatusOK {
		t.Error("signed in as an account that does not exist")
	}
	if wrongCode, missingCode := errorCode(server.attemptLogin(username, "CompletelyOther9999")), errorCode(missing); wrongCode != missingCode {
		t.Errorf("a wrong password is coded %q and an unknown account %q; the pair names which accounts exist", wrongCode, missingCode)
	}
}

// guards: authenticate
//
// The API key path had no test of any kind. Mutating the SQL that checks expiry
// — `expires_at>now()` to `<` — accepts every expired key and rejects every live
// one, and nothing noticed.
func TestAnExpiredOrRevokedAPIKeyIsRefused(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("key_owner", "USER", nil)

	created := server.request(http.MethodPost, "/api/v1/keys",
		map[string]any{"name": "시험용 키", "expiresInDays": 30}, owner)
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("create a key: %d %s", created.Code, created.Body.String())
	}
	data := decodeData(t, created)
	token, _ := data["token"].(string)
	id, _ := data["id"].(float64)
	if token == "" {
		t.Fatalf("the key came back without a token: %s", created.Body.String())
	}

	// Positive control: the live key works. Everything below is meaningless
	// without it.
	if live := server.bearer(http.MethodGet, "/api/v1/reports", token); live.Code != http.StatusOK {
		t.Fatalf("a freshly issued key was refused: %d %s", live.Code, live.Body.String())
	}

	// Expired.
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE personal_api_keys SET expires_at = now() - interval '1 day' WHERE id=$1`, int64(id)); err != nil {
		t.Fatal(err)
	}
	if expired := server.bearer(http.MethodGet, "/api/v1/reports", token); expired.Code != http.StatusUnauthorized {
		t.Errorf("an expired key answered %d, want 401 — %s", expired.Code, expired.Body.String())
	}

	// Revoked, with the expiry put back so only the revocation can refuse it.
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE personal_api_keys SET expires_at = now() + interval '30 days' WHERE id=$1`, int64(id)); err != nil {
		t.Fatal(err)
	}
	if revoke := server.request(http.MethodDelete, fmt.Sprintf("/api/v1/keys/%d", int64(id)), nil, owner); revoke.Code != http.StatusOK {
		t.Fatalf("revoke the key: %d %s", revoke.Code, revoke.Body.String())
	}
	if revoked := server.bearer(http.MethodGet, "/api/v1/reports", token); revoked.Code != http.StatusUnauthorized {
		t.Errorf("a revoked key answered %d, want 401 — %s", revoked.Code, revoked.Body.String())
	}

	// And a token nobody ever issued.
	if invented := server.bearer(http.MethodGet, "/api/v1/reports", "wky_"+token[4:]+"x"); invented.Code != http.StatusUnauthorized {
		t.Errorf("an invented token answered %d, want 401", invented.Code)
	}
}
