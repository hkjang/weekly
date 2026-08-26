package app

import (
	"net/http"
	"testing"
)

// An account can exist with no password: every account created by an import or
// by a bulk provisioning script starts that way, and the administrator sets one
// afterwards. Signing in as one has to be refused the same as a wrong password,
// and the check that does it was never run — the sign-in tests all use accounts
// that have one. Loosen the condition by a character and the nil hash reaches
// the comparison, which takes the server down rather than turning the caller
// away.

// guards: login
func TestAnAccountWithNoPasswordIsRefusedRatherThanCrashing(t *testing.T) {
	server := newTestServer(t)

	var id int64
	if err := server.app.db.QueryRow(server.ctx(),
		`INSERT INTO users(username, display_name, role) VALUES($1,$2,'USER') RETURNING id`,
		accountName("nopassword"), "비밀번호 없는 계정").Scan(&id); err != nil {
		t.Fatal(err)
	}
	var username string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT username FROM users WHERE id=$1`, id).Scan(&username); err != nil {
		t.Fatal(err)
	}

	for _, attempt := range []string{"", "AnyPassword1234"} {
		w := server.request(http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": username, "password": attempt}, nil)
		if w.Code == http.StatusOK {
			t.Fatalf("an account with no password signed in with %q", attempt)
		}
		if code := errorCode(w); code != "INVALID_CREDENTIALS" && code != "INVALID_REQUEST" {
			t.Errorf("password %q: %d %s", attempt, w.Code, w.Body.String())
		}
	}

	// And the refusal must not say which of the two it was. An account without
	// a password is a fact about the deployment, not about the caller.
	w := server.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": "AnyPassword1234"}, nil)
	if body := w.Body.String(); containsAny(body, "비밀번호가 없", "password is not set", "no password") {
		t.Errorf("the refusal told the caller the account has no password: %s", body)
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if len(needle) > 0 && len(haystack) >= len(needle) {
			for index := 0; index+len(needle) <= len(haystack); index++ {
				if haystack[index:index+len(needle)] == needle {
					return true
				}
			}
		}
	}
	return false
}
