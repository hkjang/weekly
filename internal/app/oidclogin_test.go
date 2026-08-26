package app

import (
	"fmt"
	"testing"
)

// guards: oidcCallback
//
// The refusal this exercises has been on the unguarded list since v0.129.
// Auto-provisioning off means the deployment keeps its own roll: somebody the
// identity provider vouches for is still a stranger here until an administrator
// says otherwise. Remove the refusal and every account the provider knows walks
// in, which on a corporate directory is everybody.
func TestSomebodyTheProviderKnowsIsStillAStrangerHere(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", map[string]any{
		"preferred_username": "unknown_person", "name": "모르는 사람", "email": "x@example.com",
	})
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "false"})

	reply := server.signInThrough(t, idp, "state-one", "the-nonce")
	if reply.Code == 200 {
		t.Fatalf("등록되지 않은 사람이 들어왔습니다: %s", reply.Body.String())
	}
	if code := errorCode(reply); code != "OIDC_USER_NOT_PROVISIONED" {
		t.Fatalf("코드가 %q 입니다: %d %s", code, reply.Code, reply.Body.String())
	}

	// And nothing was created on the way out.
	var accounts int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM users WHERE username = 'unknown_person'`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 {
		t.Errorf("거부된 로그인이 계정 %d개를 만들었습니다", accounts)
	}
}

// guards: oidcCallback
//
// With provisioning on, the same person is admitted — and the claim the
// administrator nominated is what their account is named after. Checking only
// the refusal above would let the whole branch be switched off.
func TestWithProvisioningOnTheNominatedClaimBecomesTheAccount(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", map[string]any{
		"preferred_username": "새사람", "name": "새 사람", "email": "new@example.com",
	})
	server.useIDP(t, idp, "weekly", map[string]string{
		"oidc.auto_provision": "true", "oidc.username_claim": "preferred_username",
	})

	if reply := server.signInThrough(t, idp, "state-two", "the-nonce"); reply.Code != 200 && reply.Code != 302 && reply.Code != 303 {
		t.Fatalf("등록이 켜져 있는데 들어오지 못했습니다: %d %s", reply.Code, reply.Body.String())
	}

	var role string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT role FROM users WHERE username = '새사람'`).Scan(&role); err != nil {
		t.Fatalf("지목한 청구항으로 계정이 만들어지지 않았습니다: %v", err)
	}
	// A new arrival is a writer, not an administrator, until somebody says so.
	if role != "USER" {
		t.Errorf("새로 등록된 계정의 역할이 %q 입니다", role)
	}
}

// guards: oidcCallback
//
// Who holds the keys. One claim, one configured group name, and a person the
// directory has never met arrives as an administrator. Both halves of the
// condition matter: no group configured must promote nobody, and a member of
// some other group must stay a writer. Every fixture that puts an admin against
// a stranger settles it on the first half alone.
func TestOnlyTheNominatedGroupArrivesAsAnAdministrator(t *testing.T) {
	for index, arrival := range []struct {
		name        string
		adminGroup  string
		groups      []string
		wantRole    string
		description string
	}{
		{"지목된 무리에 속함", "weekly-admins", []string{"everyone", "weekly-admins"}, "ADMIN", "관리자로 들어와야 합니다"},
		{"다른 무리에 속함", "weekly-admins", []string{"everyone", "sales"}, "USER", "평사원이어야 합니다"},
		{"무리가 없음", "weekly-admins", []string{}, "USER", "평사원이어야 합니다"},
		{"관리자 무리를 지정하지 않음", "", []string{"weekly-admins"}, "USER", "지정이 없으면 아무도 승격되지 않아야 합니다"},
	} {
		t.Run(arrival.name, func(t *testing.T) {
			server := newTestServer(t)
			idp := newIDP(t, "weekly", "the-nonce", map[string]any{
				"preferred_username": "도착한사람", "name": "도착한 사람",
				"email": "arrival@example.com", "groups": arrival.groups,
			})
			server.useIDP(t, idp, "weekly", map[string]string{
				"oidc.auto_provision": "true", "oidc.username_claim": "preferred_username",
				"oidc.groups_claim": "groups", "oidc.admin_group": arrival.adminGroup,
			})

			if reply := server.signInThrough(t, idp, fmt.Sprintf("state-group-%d", index), "the-nonce"); reply.Code >= 400 {
				t.Fatalf("들어오지 못했습니다: %d %s", reply.Code, reply.Body.String())
			}
			var role string
			if err := server.app.db.QueryRow(server.ctx(),
				`SELECT role FROM users WHERE username = '도착한사람'`).Scan(&role); err != nil {
				t.Fatal(err)
			}
			if role != arrival.wantRole {
				t.Errorf("%s — %s, 그런데 %q 입니다", arrival.name, arrival.description, role)
			}
		})
	}
}
