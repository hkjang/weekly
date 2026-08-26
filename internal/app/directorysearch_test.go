package app

import (
	"net/http"
	"testing"
)

// On a deployment of a few hundred, the search box is how an administrator
// finds anybody at all — v0.129 added it for exactly that. Both filters sit
// behind a condition that decides whether to apply them, and inverting either
// one returns the whole directory to every query while still answering 200 with
// a plausible list.

func directoryUsernames(t *testing.T, server *testServer, path string) map[string]bool {
	t.Helper()
	page := decodeUserList(t, server.request(http.MethodGet, path, nil, server.admin))
	found := map[string]bool{}
	for _, user := range page.Items {
		found[user.Username] = true
	}
	return found
}

// guards: adminUsers
func TestTheDirectorySearchNarrowsToWhatWasTyped(t *testing.T) {
	server := newTestServer(t)
	server.createUser("needle_findme", "USER", nil)
	needle := server.lastCreatedUsername("needle_findme")
	server.createUser("haystack_other", "USER", nil)
	other := server.lastCreatedUsername("haystack_other")

	found := directoryUsernames(t, server, "/api/v1/admin/users?q=needle_findme&limit=500")
	if !found[needle] {
		t.Errorf("검색어와 맞는 계정이 결과에 없습니다: %s", needle)
	}
	if found[other] {
		t.Errorf("검색어와 무관한 계정이 결과에 있습니다: %s", other)
	}

	// A search matching nobody must return nobody rather than everybody.
	if empty := directoryUsernames(t, server, "/api/v1/admin/users?q=zzz_nobody_zzz&limit=500"); len(empty) > 0 {
		t.Errorf("아무와도 맞지 않는 검색이 %d명을 돌려줍니다", len(empty))
	}
}

// guards: adminUsers
func TestTheDirectoryRoleFilterNarrowsToThatRole(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("역할 조직", "ROLEFILT")
	server.createUser("role_writer", "USER", &organisation)
	writer := server.lastCreatedUsername("role_writer")
	server.createUser("role_leader", "TEAM_LEADER", &organisation)
	leader := server.lastCreatedUsername("role_leader")

	leaders := directoryUsernames(t, server, "/api/v1/admin/users?role=TEAM_LEADER&limit=500")
	if !leaders[leader] {
		t.Errorf("팀장 필터에 팀장이 없습니다: %s", leader)
	}
	if leaders[writer] {
		t.Errorf("팀장 필터에 평사원이 있습니다: %s", writer)
	}

	// And no filter still shows both, so the narrowing above is the filter
	// working rather than the directory being empty.
	all := directoryUsernames(t, server, "/api/v1/admin/users?limit=500")
	if !all[leader] || !all[writer] {
		t.Errorf("필터 없이 조회했는데 둘 다 보이지 않습니다: 팀장=%v 평사원=%v", all[leader], all[writer])
	}
}
