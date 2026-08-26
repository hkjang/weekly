package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Five places decide how wide to read by comparing the requested scope with
// SELF, and every one of them could be inverted without a single test
// noticing. The refusal beside them is guarded — a plain writer asking for
// TEAM is turned away, and that is checked — but nothing ever asked whether
// the switch does anything once you are allowed through it.
//
// It cannot be seen from a plain member's session: with no organisation to
// widen into, SELF and TEAM read the same rows. The difference only exists for
// a leader, and no test read these screens as one.
//
// Inverted, a leader who asks for their own work is shown the whole team's,
// and a leader who asks for the team sees only themselves. The first is the
// worse of the two: the screen says 내 업무 and lists colleagues.

// guards: listWorkItems, searchWorkItems, weeklyChanges, meetingMode
func TestTheScopeSwitchNarrowsAndWidensOnEveryScreenThatOffersIt(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("범위 조직", "SCOPESW")
	leader := server.createUser("scope_lead", "TEAM_LEADER", &organisation)
	mate := server.createUser("scope_mate", "USER", &organisation)

	server.weekWithIssue(mate, "2026-08-17", "동료만의 업무", "동료 쪽 이슈", 30)
	server.weekWithIssue(leader, "2026-08-17", "팀장만의 업무", "팀장 쪽 이슈", 45)

	for _, screen := range []struct{ name, path string }{
		{"업무 목록", "/api/v1/work-items?scope=%s"},
		{"업무 검색", "/api/v1/work-items/search?scope=%s&q=업무"},
		{"주간 변화", "/api/v1/changes?scope=%s&week=2026-08-17"},
		{"회의 자료", "/api/v1/meeting?scope=%s&week=2026-08-17"},
	} {
		body := map[string]string{}
		for _, scope := range []string{"SELF", "TEAM"} {
			w := server.request(http.MethodGet, fmt.Sprintf(screen.path, scope), nil, leader)
			if w.Code != http.StatusOK {
				t.Fatalf("%s (%s): %d %s", screen.name, scope, w.Code, w.Body.String())
			}
			body[scope] = w.Body.String()
		}
		// Asking for your own work must not hand back the team's.
		if strings.Contains(body["SELF"], "동료만의 업무") {
			t.Errorf("%s: SELF carried a colleague's work", screen.name)
		}
		// And asking for the team must actually widen.
		if !strings.Contains(body["TEAM"], "동료만의 업무") {
			t.Errorf("%s: TEAM did not reach past the leader's own work", screen.name)
		}
		// The leader's own work is on both, so a screen that simply came back
		// empty cannot pass either half above by accident.
		for _, scope := range []string{"SELF", "TEAM"} {
			if !strings.Contains(body[scope], "팀장만의 업무") {
				t.Errorf("%s: %s lost the reader's own work", screen.name, scope)
			}
		}
	}
}

// The agenda's 타 조직 대기 section reads the same switch a second time, on a
// separate query, so guarding the work items above leaves it uncovered. It
// answers "whose waiting is this meeting for" — inverted, the room gets handed
// dependencies belonging to people who are not in it.

// guards: crossOrgBlocked
func TestTheMeetingsCrossOrgWaitFollowsTheScopeSwitch(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("대기 조직", "WAITMINE")
	theirs := server.createOrganization("선행 조직", "WAITTHEIRS")
	leader := server.createUser("wait_lead", "TEAM_LEADER", &mine)
	mate := server.createUser("wait_mate", "USER", &mine)
	other := server.createUser("wait_other", "USER", &theirs)

	server.weekWithIssue(mate, "2026-08-17", "동료의 막힌 업무", "", 20)
	server.weekWithIssue(other, "2026-08-17", "다른 조직의 선행 업무", "", 40)
	server.weekWithIssue(leader, "2026-08-17", "팀장 자신의 업무", "", 50)

	blocked := server.workItemNamed(server.lastCreatedUsername("wait_mate"), "동료의 막힌 업무")
	blocker := server.workItemNamed(server.lastCreatedUsername("wait_other"), "다른 조직의 선행 업무")
	linkBetween(t, server, mate, blocked, blocker)

	read := func(scope string) string {
		w := server.request(http.MethodGet, "/api/v1/meeting?scope="+scope+"&week=2026-08-17", nil, leader)
		if w.Code != http.StatusOK {
			t.Fatalf("meeting (%s): %d %s", scope, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	team, self := read("TEAM"), read("SELF")

	if !strings.Contains(team, "타 조직 대기") || !strings.Contains(team, "동료의 막힌 업무") {
		t.Errorf("the team agenda left out a colleague waiting on another organisation: %s", team)
	}
	if strings.Contains(self, "타 조직 대기") {
		t.Errorf("the leader's own agenda carried somebody else's cross-organisation wait: %s", self)
	}
}
