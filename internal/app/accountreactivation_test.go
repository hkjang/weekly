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

	// A null is not a value for a flag, so it means the same as leaving the
	// key out: the stored activity stands. Without this case the presence
	// check could be loosened to read the key alone and nothing would notice.
	explicitNull := map[string]any{"displayName": username, "role": "USER", "active": nil}
	if w := server.request(http.MethodPut, "/api/v1/admin/users/"+itoa(id), explicitNull, server.admin); w.Code != http.StatusOK {
		t.Fatalf("an explicit null for activity: %d %s", w.Code, w.Body.String())
	}
	if server.canSignIn(username, "ResetPassword1234") {
		t.Error("sending active: null re-enabled a disabled account")
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

// guards: updateUser
func TestAnAccountUpdateStillNeedsANameAndARealRole(t *testing.T) {
	server := newTestServer(t)
	server.createUser("named", "USER", nil)
	username := server.lastCreatedUsername("named")
	id := server.userIDOf(username)
	path := "/api/v1/admin/users/" + itoa(id)

	blank := server.request(http.MethodPut, path, map[string]any{"displayName": "  ", "role": "USER"}, server.admin)
	if blank.Code != http.StatusBadRequest {
		t.Errorf("an update with no display name was accepted: %d %s", blank.Code, blank.Body.String())
	}
	invented := server.request(http.MethodPut, path, map[string]any{"displayName": "이름", "role": "SUPERUSER"}, server.admin)
	if invented.Code != http.StatusBadRequest {
		t.Errorf("an invented role was accepted: %d %s", invented.Code, invented.Body.String())
	}
	// The rejections must not have written anything on the way out.
	var stored string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT display_name FROM users WHERE id=$1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != username {
		t.Errorf("a refused update changed the name anyway: %q", stored)
	}
}

// guards: updateUser
//
// The lock-out guard exists so the last administrator cannot remove their own
// way back in. It has to apply to exactly that: the account making the request,
// and only when the request actually takes the role or the activity away.
// Widen it by a character and administrators can no longer edit their own
// profile, or demote anybody at all.
func TestTheLockOutGuardCoversTheAdministratorsOwnAccountAndNoOtherCase(t *testing.T) {
	server := newTestServer(t)
	adminID := server.userIDOf(server.adminName)
	own := "/api/v1/admin/users/" + itoa(adminID)

	// Editing your own profile without mentioning activity is ordinary work.
	rename := server.request(http.MethodPut, own,
		map[string]any{"displayName": "관리자 새 이름", "role": "ADMIN"}, server.admin)
	if rename.Code != http.StatusOK {
		t.Errorf("an administrator cannot rename themselves: %d %s", rename.Code, rename.Body.String())
	}

	// Taking your own administrator role away, or your own sign-in, is not.
	demote := server.request(http.MethodPut, own,
		map[string]any{"displayName": "관리자 새 이름", "role": "USER"}, server.admin)
	if demote.Code != http.StatusBadRequest || errorCode(demote) != "SELF_LOCKOUT" {
		t.Errorf("an administrator demoted themselves: %d %s", demote.Code, demote.Body.String())
	}
	disable := server.request(http.MethodPut, own,
		map[string]any{"displayName": "관리자 새 이름", "role": "ADMIN", "active": false}, server.admin)
	if disable.Code != http.StatusBadRequest || errorCode(disable) != "SELF_LOCKOUT" {
		t.Errorf("an administrator disabled themselves: %d %s", disable.Code, disable.Body.String())
	}

	// Somebody else's role is not the lock-out guard's business.
	server.createUser("demotable", "TEAM_LEADER", nil)
	other := server.userIDOf(server.lastCreatedUsername("demotable"))
	someoneElse := server.request(http.MethodPut, "/api/v1/admin/users/"+itoa(other),
		map[string]any{"displayName": "다른 사람", "role": "USER"}, server.admin)
	if someoneElse.Code != http.StatusOK {
		t.Errorf("demoting somebody else was refused as a self lock-out: %d %s", someoneElse.Code, someoneElse.Body.String())
	}
}
