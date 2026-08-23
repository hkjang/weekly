package app

import (
	"context"
	"testing"
)

// Who may open whose work. The branches that do not need a database are pinned
// here; the organisation subtree case is exercised against a real one in
// TestDatabaseMigrationsAndSecretRotation.
//
// The rule used to be `p.Role == "USER"` and nothing else, so every leader
// could name any user id. No work leaked — loadWorkItems scopes that — but the
// handler filled in the display name from the users table regardless, and
// returned it as an ordinary empty handover. A leader could read names across
// the whole company, and could not tell "no open work" from "not yours to see".
func TestCanViewPersonWithoutTouchingTheDatabase(t *testing.T) {
	organization := int64(7)
	cases := []struct {
		name   string
		who    *principal
		target int64
		want   bool
	}{
		{"본인은 언제나", &principal{ID: 5, Role: "USER"}, 5, true},
		{"관리자는 누구든", &principal{ID: 1, Role: "ADMIN"}, 99, true},
		{"일반 사용자는 남을 못 본다", &principal{ID: 5, Role: "USER"}, 6, false},
		// A plain user who does belong to an organisation is the case that
		// catches a loosened role check: without one, this falls through to the
		// subtree query and everyone in the org becomes visible to everyone.
		{"조직에 속한 일반 사용자도 남을 못 본다", &principal{ID: 5, Role: "USER", OrganizationID: &organization}, 6, false},
		{"조직 없는 팀장은 본인만", &principal{ID: 5, Role: "TEAM_LEADER"}, 6, false},
		{"조직 없는 조직장도 본인만", &principal{ID: 5, Role: "ORG_MANAGER", OrganizationID: nil}, 6, false},
		{"주체가 없으면 거부", nil, 6, false},
		// A leader with an organisation asks the database; that path is covered
		// by the integration test and must not be reached with a nil db here.
		{"팀장 본인은 질의 없이", &principal{ID: 5, Role: "TEAM_LEADER", OrganizationID: &organization}, 5, true},
	}
	application := &App{}
	for _, testCase := range cases {
		got, err := application.canViewPerson(context.Background(), testCase.who, testCase.target)
		if err != nil {
			t.Errorf("%s: unexpected error %v", testCase.name, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
