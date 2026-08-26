package app

import (
	"net/http"
	"testing"
)

// Disabling an account is how a deployment revokes access when someone leaves.
// The administrator update is a full replace, so every field left out of the
// body is written back at its zero value — and for `active` that zero value was
// chosen as true. A later edit that says nothing about activity therefore hands
// the account back its sign-in, which is the one direction an omission must
// never move.
//
// The screen always sends the flag, so this is only reachable from a script or
// another client. That is exactly where a password reset gets written.

func (s *testServer) userIDOf(username string) int64 {
	s.t.Helper()
	var id int64
	if err := s.app.db.QueryRow(s.ctx(), `SELECT id FROM users WHERE username=$1`, username).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *testServer) canSignIn(username, password string) bool {
	s.t.Helper()
	w := s.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password}, nil)
	return w.Code == http.StatusOK
}

// guards: updateUser
func TestAnUpdateThatSaysNothingAboutActivityLeavesADisabledAccountDisabled(t *testing.T) {
	server := newTestServer(t)
	server.createUser("departed", "USER", nil)
	username := server.lastCreatedUsername("departed")
	password := server.passwordFor("departed")
	id := server.userIDOf(username)

	if !server.canSignIn(username, password) {
		t.Fatal("the account should sign in before it is disabled")
	}

	disable := map[string]any{"displayName": username, "role": "USER", "active": false}
	if w := server.request(http.MethodPut, "/api/v1/admin/users/"+itoa(id), disable, server.admin); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if server.canSignIn(username, password) {
		t.Fatal("a disabled account still signed in")
	}

	// The shape a password-reset script sends: the fields it means to change,
	// and nothing about activity.
	reset := map[string]any{"displayName": username, "role": "USER", "password": "ResetPassword1234"}
	if w := server.request(http.MethodPut, "/api/v1/admin/users/"+itoa(id), reset, server.admin); w.Code != http.StatusOK {
		t.Fatalf("reset password: %d %s", w.Code, w.Body.String())
	}

	if server.canSignIn(username, "ResetPassword1234") {
		t.Fatal("resetting the password re-enabled a disabled account")
	}
	if server.canSignIn(username, password) {
		t.Fatal("the disabled account signed in with its old password")
	}
}

// guards: updateUser
func TestAnUpdateKeepsTheFieldsItDoesNotMentionAndStillClearsTheOnesItDoes(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("남아 있어야 할 조직", "KEEPORG")
	server.createUser("staying", "USER", &organisation)
	username := server.lastCreatedUsername("staying")
	id := server.userIDOf(username)

	full := map[string]any{"displayName": username, "role": "USER", "email": "keep@example.com",
		"organizationId": organisation, "active": true}
	if w := server.request(http.MethodPut, "/api/v1/admin/users/"+itoa(id), full, server.admin); w.Code != http.StatusOK {
		t.Fatalf("full update: %d %s", w.Code, w.Body.String())
	}

	// The shape a password-reset script sends.
	reset := map[string]any{"displayName": username, "role": "USER", "password": "ResetPassword1234"}
	if w := server.request(http.MethodPut, "/api/v1/admin/users/"+itoa(id), reset, server.admin); w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}

	email, org := server.contactOf(id)
	if email != "keep@example.com" {
		t.Fatalf("resetting the password wiped the address: %q", email)
	}
	if org == nil || *org != organisation {
		// An account with no organisation is invisible to every team leader, so
		// what it reports waits in a queue nobody can open.
		t.Fatalf("resetting the password dropped the account out of its organisation: %v", org)
	}

	// Saying it explicitly still clears it — the screen sends the whole form.
	clear := map[string]any{"displayName": username, "role": "USER", "email": "", "organizationId": nil}
	if w := server.request(http.MethodPut, "/api/v1/admin/users/"+itoa(id), clear, server.admin); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if email, org = server.contactOf(id); email != "" || org != nil {
		t.Fatalf("an explicit clear left email=%q organisation=%v", email, org)
	}
	if !server.canSignIn(username, "ResetPassword1234") {
		t.Fatal("clearing the contact fields also disabled the account")
	}
}

func (s *testServer) contactOf(id int64) (string, *int64) {
	s.t.Helper()
	var email string
	var organisation *int64
	if err := s.app.db.QueryRow(s.ctx(),
		`SELECT email, organization_id FROM users WHERE id=$1`, id).Scan(&email, &organisation); err != nil {
		s.t.Fatal(err)
	}
	return email, organisation
}
