package app

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// A leader whose account has no organisation sees an empty team list, and until
// now that was indistinguishable from a team that filed nothing. Found by
// measuring a division manager at scale: 76 people under them and a 99-byte
// answer, because an administrator had edited the account and the update
// cleared the organisation it did not mention.
//
// The account is valid and the request is allowed, so this is not an error. It
// is an empty answer that has to say which kind of empty it is.
//
// guards: queryReports
func TestALeaderWithNoOrganisationIsToldWhy(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("본부", "ORPHAN")
	leader := server.createUser("orphan_leader", "ORG_MANAGER", &organisation)
	member := server.createUser("orphan_member", "USER", &organisation)
	server.submitted(member, "2026-08-24", "팀원 보고서")

	// Positive control: with an organisation the leader sees the member.
	full := server.request(http.MethodGet, "/api/v1/team/reports", nil, leader)
	if full.Code != http.StatusOK {
		t.Fatalf("team reports: %d %s", full.Code, full.Body.String())
	}
	items, _ := decodeData(t, full)["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("the leader sees nothing even with an organisation, so the check below proves nothing: %s", full.Body.String())
	}
	if notice, _ := decodeData(t, full)["notice"].(string); notice != "" {
		t.Errorf("a working list carries a notice: %q", notice)
	}

	// Now take the organisation away, exactly as a partial admin update does.
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE users SET organization_id=NULL WHERE id=(SELECT user_id FROM user_sessions WHERE token_hash=$1)`,
		tokenHash(leader.Value)); err != nil {
		t.Fatal(err)
	}
	// The session carries the old principal, so sign in again to pick it up.
	orphaned := server.signIn(server.lastCreatedUsername("orphan_leader"), server.passwordFor("orphan_leader"))

	empty := server.request(http.MethodGet, "/api/v1/team/reports", nil, orphaned)
	if empty.Code != http.StatusOK {
		t.Fatalf("a leader without an organisation answered %d, want 200 — the account is valid", empty.Code)
	}
	data := decodeData(t, empty)
	if rows, _ := data["items"].([]any); len(rows) != 0 {
		t.Errorf("the list is not empty: %d rows", len(rows))
	}
	notice, _ := data["notice"].(string)
	if notice == "" {
		t.Fatal("the empty list says nothing, so it reads as a team that filed nothing")
	}
	for _, phrase := range []string{"소속 조직", "관리자"} {
		if !strings.Contains(notice, phrase) {
			t.Errorf("the notice does not mention %q, so the reader cannot act on it: %q", phrase, notice)
		}
	}
}

// guards: updateUser
//
// docs/openapi.yaml now says a user update replaces what it is sent — omitted
// fields are cleared — except the password, which changes only when supplied.
// That exception is the one nobody would notice breaking until people started
// being locked out, so it is written down and checked.
func TestAnAdminEditKeepsThePasswordUnlessItSendsOne(t *testing.T) {
	server := newTestServer(t)
	cookie := server.createUser("edited_user", "USER", nil)
	if cookie == nil {
		t.Fatal("the account was not created")
	}
	username := server.lastCreatedUsername("edited_user")
	original := server.passwordFor("edited_user")

	var id int64
	if err := server.app.db.QueryRow(server.ctx(), `SELECT id FROM users WHERE username=$1`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/users/" + itoa(id)

	// An edit that says nothing about the password.
	edit := server.request(http.MethodPut, path,
		map[string]any{"displayName": "이름만 바꿈", "role": "USER", "active": true}, server.admin)
	if edit.Code != http.StatusOK {
		t.Fatalf("edit the display name: %d %s", edit.Code, edit.Body.String())
	}
	if again := server.attemptLogin(username, original); again.Code != http.StatusOK {
		t.Fatalf("the password stopped working after an unrelated edit: %d %s", again.Code, again.Body.String())
	}

	// And an edit that does send one.
	const replacement = "ReplacedPassword1234"
	changed := server.request(http.MethodPut, path,
		map[string]any{"displayName": "이름만 바꿈", "role": "USER", "active": true, "password": replacement}, server.admin)
	if changed.Code != http.StatusOK {
		t.Fatalf("set a new password: %d %s", changed.Code, changed.Body.String())
	}
	if fresh := server.attemptLogin(username, replacement); fresh.Code != http.StatusOK {
		t.Errorf("the new password does not work: %d %s", fresh.Code, fresh.Body.String())
	}
	if stale := server.attemptLogin(username, original); stale.Code == http.StatusOK {
		t.Error("the old password still works after it was replaced")
	}
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }
