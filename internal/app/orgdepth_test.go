package app

import (
	"fmt"
	"net/http"
	"testing"
)

// The visibility predicate walks the organisation tree with a recursive lookup,
// and every fixture this product has is two levels deep — a root and its
// children. So "an organisation manager sees the whole subtree beneath them" has
// only ever been checked one hop down, while a real deployment is
// 회사 → 본부 → 실 → 팀.
//
// This builds four levels and asks the two questions that matter: does sight
// reach all the way down, and does it stop at the branch.
//
// guards: canViewReport, teamReports, getReport
func TestSightReachesDownTheWholeSubtreeAndNoFurther(t *testing.T) {
	server := newTestServer(t)

	company := server.createOrganization("회사", "DEPTHCO")
	division := server.createChildOrganization("본부", "DEPTHDIV", &company)
	office := server.createChildOrganization("실", "DEPTHOFF", &division)
	team := server.createChildOrganization("팀", "DEPTHTEAM", &office)
	// A separate branch under the same company, so the refusal below is about
	// the branch and not about depth.
	sibling := server.createChildOrganization("다른 본부", "DEPTHSIB", &company)
	siblingTeam := server.createChildOrganization("다른 팀", "DEPTHSIBT", &sibling)

	divisionManager := server.createUser("depth_manager", "ORG_MANAGER", &division)
	siblingManager := server.createUser("depth_sibling", "ORG_MANAGER", &sibling)
	deepMember := server.createUser("depth_member", "USER", &team)

	// Three levels below the manager.
	reportID := server.submitted(deepMember, "2026-08-24", "세 단계 아래에서 쓴 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d", reportID)

	if own := server.request(http.MethodGet, path, nil, deepMember); own.Code != http.StatusOK {
		t.Fatalf("the author cannot read their own report: %d %s", own.Code, own.Body.String())
	}

	// Down the whole subtree.
	seen := server.request(http.MethodGet, path, nil, divisionManager)
	if seen.Code != http.StatusOK {
		t.Errorf("a 본부 manager cannot see a 팀 member three levels down: %d %s — the recursive lookup stops short",
			seen.Code, seen.Body.String())
	}

	// And not across to the other branch.
	crossed := server.request(http.MethodGet, path, nil, siblingManager)
	if crossed.Code == http.StatusOK {
		t.Errorf("a manager of another branch read the report: %s", crossed.Body.String())
	}

	// The same rule through the list, not only the single fetch: a page that
	// silently omits the subtree is the same failure with no error to notice.
	list := server.request(http.MethodGet, "/api/v1/team/reports", nil, divisionManager)
	if list.Code != http.StatusOK {
		t.Fatalf("team reports: %d %s", list.Code, list.Body.String())
	}
	items, _ := decodeData(t, list)["items"].([]any)
	found := false
	for _, entry := range items {
		row, _ := entry.(map[string]any)
		if id, _ := row["id"].(float64); int64(id) == reportID {
			found = true
		}
	}
	if !found {
		t.Errorf("the deep member's report is missing from the manager's list of %d", len(items))
	}

	// The other branch's list must not contain it.
	otherList := server.request(http.MethodGet, "/api/v1/team/reports", nil, siblingManager)
	if otherList.Code != http.StatusOK {
		t.Fatalf("team reports for the other branch: %d %s", otherList.Code, otherList.Body.String())
	}
	otherItems, _ := decodeData(t, otherList)["items"].([]any)
	for _, entry := range otherItems {
		row, _ := entry.(map[string]any)
		if id, _ := row["id"].(float64); int64(id) == reportID {
			t.Error("another branch's list contains the report")
		}
	}
	_ = siblingTeam
	_ = office
}
