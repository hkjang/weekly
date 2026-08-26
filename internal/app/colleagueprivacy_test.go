package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestOnePersonsReportIsNotAnothersToRead puts both people in no organisation
// at all, so the branch that decides what a colleague may see never runs. That
// branch is the ordinary case: two people on one team. Loosen its condition by
// one operator and a plain member reads everything their team writes, with the
// suite still green.

// guards: canViewReport
func TestAColleagueOnTheSameTeamStillCannotReadYourReport(t *testing.T) {
	server := newTestServer(t)
	team := server.createOrganization("같은 팀", "SAMETEAM")
	other := server.createOrganization("다른 팀", "OTHERTEAM")

	author := server.createUser("colleague_author", "USER", &team)
	colleague := server.createUser("colleague_peer", "USER", &team)
	leader := server.createUser("colleague_leader", "TEAM_LEADER", &team)
	outsideLeader := server.createUser("colleague_outside_leader", "TEAM_LEADER", &other)

	reportID := server.submitted(author, "2026-08-24", "같은 팀 동료도 보면 안 되는 요약")
	path := fmt.Sprintf("/api/v1/reports/%d", reportID)

	if own := server.request(http.MethodGet, path, nil, author); own.Code != http.StatusOK {
		t.Fatalf("the author cannot read their own report: %d %s", own.Code, own.Body.String())
	}

	// The member sitting next to them, in the same organisation.
	refused(t, "a colleague on the same team reading a report", server.request(http.MethodGet, path, nil, colleague))
	if body := server.request(http.MethodGet, path, nil, colleague).Body.String(); strings.Contains(body, "같은 팀 동료도 보면 안 되는 요약") {
		t.Errorf("the refusal carried the summary anyway: %s", body)
	}

	// The other half of the same rule: the leader of that organisation may.
	// Checking only the refusal would let the branch be switched off entirely.
	if seen := server.request(http.MethodGet, path, nil, leader); seen.Code != http.StatusOK {
		t.Errorf("the team leader cannot read their own team's report: %d %s", seen.Code, seen.Body.String())
	}

	// And a leader of a different organisation may not.
	refused(t, "another organisation's leader reading a report",
		server.request(http.MethodGet, path, nil, outsideLeader))
}
