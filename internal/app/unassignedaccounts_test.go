package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An account with no organisation is invisible to every team leader, so what it
// submits waits in a queue nobody can open. v0.104 tells the person; this is the
// other half, because the person cannot fix it — only an administrator can, and
// the directory had no way to find them. On a deployment of a few thousand the
// only method was paging through everybody looking for a blank column.

func decodeUserList(t *testing.T, w *httptest.ResponseRecorder) userListView {
	t.Helper()
	var envelope struct {
		Data userListView `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return envelope.Data
}

// guards: adminUsers
func TestTheDirectoryCountsAndListsAccountsWithNoOrganisation(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("소속 있는 조직", "HASORG")

	server.createUser("assigned", "USER", &organisation)
	assigned := server.lastCreatedUsername("assigned")
	server.createUser("adrift", "USER", nil)
	first := server.lastCreatedUsername("adrift")
	server.createUser("looselead", "TEAM_LEADER", nil)
	second := server.lastCreatedUsername("looselead")

	page := decodeUserList(t, server.request(http.MethodGet, "/api/v1/admin/users?limit=500", nil, server.admin))
	if page.Unassigned < 2 {
		t.Fatalf("two accounts were created without an organisation, the directory counts %d", page.Unassigned)
	}
	baseline := page.Unassigned

	filtered := decodeUserList(t, server.request(http.MethodGet, "/api/v1/admin/users?organization=none&limit=500", nil, server.admin))
	if filtered.Total != baseline {
		t.Fatalf("the filter returned %d accounts, the count says %d", filtered.Total, baseline)
	}
	if filtered.Organization != "none" {
		t.Fatalf("the response does not report the filter it applied: %q", filtered.Organization)
	}
	listed := map[string]bool{}
	for _, item := range filtered.Items {
		if item.OrganizationID != nil {
			t.Fatalf("%s has an organisation and should not be in this list", item.Username)
		}
		listed[item.Username] = true
	}
	for _, username := range []string{first, second} {
		if !listed[username] {
			t.Fatalf("%s has no organisation but the filter did not list it", username)
		}
	}
	if listed[assigned] {
		t.Fatalf("%s has an organisation and appeared in the unassigned list", assigned)
	}

	// Giving one of them an organisation takes it out of both.
	var id int64
	if err := server.app.db.QueryRow(server.ctx(), `SELECT id FROM users WHERE username=$1`, first).Scan(&id); err != nil {
		t.Fatalf("find %s: %v", first, err)
	}
	updated := server.request(http.MethodPut, "/api/v1/admin/users/"+itoa(id), map[string]any{
		"displayName": "이제 소속이 있음", "role": "USER", "active": true, "organizationId": organisation,
	}, server.admin)
	if updated.Code != http.StatusOK {
		t.Fatalf("assign an organisation: %d %s", updated.Code, updated.Body.String())
	}
	after := decodeUserList(t, server.request(http.MethodGet, "/api/v1/admin/users?limit=500", nil, server.admin))
	if after.Unassigned != baseline-1 {
		t.Fatalf("after assigning one account the count went from %d to %d", baseline, after.Unassigned)
	}
}

// guards: adminUsers
func TestAnUnknownOrganizationFilterIsIgnoredRatherThanObeyed(t *testing.T) {
	server := newTestServer(t)
	server.createUser("adrift", "USER", nil)
	// Only "none" means anything. A typo must not silently narrow the directory
	// to nothing and let an administrator conclude the deployment is empty.
	page := decodeUserList(t, server.request(http.MethodGet, "/api/v1/admin/users?organization=nome&limit=500", nil, server.admin))
	if page.Total == 0 {
		t.Fatal("an unrecognised filter emptied the directory")
	}
	if page.Organization != "" {
		t.Fatalf("an unrecognised filter was reported as applied: %q", page.Organization)
	}
}
