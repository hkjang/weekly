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

// guards: accountNotice=100
//
// The team list says why it is empty (above), but the dashboard and the handover
// roster are empty for the same reason and say nothing. Rather than glue an
// explanation to each screen and forget the next one, the session carries it —
// so it appears wherever the reader is, and stops when the account is fixed.
//
// This test used to assert that a writer with no organisation needs no warning,
// on the grounds that "a plain user's own reports do not depend on an
// organisation". Measured on a running deployment, that premise is false. An
// organisation-less writer submitted a report and the team leader's review
// queue stayed at zero; the leader's team list held only the writer who has an
// organisation. The submission is real, and no reviewer can open it. Their own
// reports depend on an organisation after all — not to write, but to be read.
func TestAnAccountThatCannotBeReachedIsToldWhy(t *testing.T) {
	organisation := int64(7)
	cases := []struct {
		name     string
		who      *principal
		workflow bool
		want     bool
	}{
		{"소속 없는 팀장", &principal{Role: "TEAM_LEADER"}, true, true},
		{"소속 없는 조직장", &principal{Role: "ORG_MANAGER"}, true, true},
		// Submitted into a queue no leader can open.
		{"소속 없는 작성자 · 결재 켬", &principal{Role: "USER"}, true, true},
		// No review step, but still absent from every team list and rollup.
		{"소속 없는 작성자 · 결재 끔", &principal{Role: "USER"}, false, true},

		{"소속 있는 팀장", &principal{Role: "TEAM_LEADER", OrganizationID: &organisation}, true, false},
		{"소속 있는 조직장", &principal{Role: "ORG_MANAGER", OrganizationID: &organisation}, true, false},
		{"소속 있는 작성자", &principal{Role: "USER", OrganizationID: &organisation}, true, false},
		// An administrator sees everything regardless, so nothing is wrong.
		{"소속 없는 관리자", &principal{Role: "ADMIN"}, true, false},
		{"로그인 전", nil, true, false},
	}
	for _, item := range cases {
		notice := accountNotice(item.who, item.workflow)
		if (notice != "") != item.want {
			t.Errorf("%s: accountNotice=%q, want warned=%v", item.name, notice, item.want)
		}
		if item.want {
			for _, phrase := range []string{"소속 조직", "관리자"} {
				if !strings.Contains(notice, phrase) {
					t.Errorf("%s: the notice does not mention %q: %q", item.name, phrase, notice)
				}
			}
		}
	}
}

// guards: me
//
// And it has to reach the response, not only exist as a function.
func TestTheSessionCarriesTheAccountNotice(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("소속 있는 본부", "HASORG")
	settled := server.createUser("session_settled", "ORG_MANAGER", &organisation)
	if data := decodeData(t, server.request(http.MethodGet, "/api/v1/me", nil, settled)); data["accountNotice"] != nil && data["accountNotice"] != "" {
		t.Errorf("a leader with an organisation is warned: %v", data["accountNotice"])
	}

	orphan := server.createUser("session_orphan", "ORG_MANAGER", nil)
	notice, _ := decodeData(t, server.request(http.MethodGet, "/api/v1/me", nil, orphan))["accountNotice"].(string)
	if notice == "" {
		t.Error("a leader with no organisation gets no notice from the session, so no screen can show it")
	}
}
