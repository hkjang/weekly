package app

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// 키클락에는 있고 Weekly 에는 아직 없는 사람이 에이전트로 먼저 들어올 때.
//
// The agent is often the first thing a person points at Weekly. Until this
// release the MCP door refused everybody who had not signed in to the web
// first, so every call an agent made came back 401 with advice the agent could
// not follow. These hold the door to what the web sign-in would have done for
// the same person — create the account when the administrator allows it — and
// to nothing more: no administrator made, no second account, no deactivated
// person let back in, and no account at all when registration is off.

// accountsFor counts the rows an SSO identity could have produced.
func (s *testServer) accountsFor(subject, username string) int {
	s.t.Helper()
	var count int
	if err := s.app.db.QueryRow(s.ctx(),
		`SELECT count(*) FROM users WHERE oidc_subject=$1 OR lower(username)=lower($2)`, subject, username).Scan(&count); err != nil {
		s.t.Fatal(err)
	}
	return count
}

// guards: oauthPrincipal, oauthAccount
func TestAPersonKeycloakKnowsIsRegisteredByTheirFirstMCPCall(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly-web", "", nil)
	server.useIDP(t, idp, "weekly-web", map[string]string{
		"mcp.oauth.enabled": "true", "oidc.auto_provision": "true",
		"oidc.groups_claim": "groups", "oidc.admin_group": "weekly-admins",
	})
	subject, username := "subject-first-mcp", "agent_first"
	token := accessToken(t, idp, "http://weekly.example.test/mcp", map[string]any{
		"sub": subject, "preferred_username": username, "name": "에이전트 먼저", "email": "agent.first@example.com",
		// In the administrator group, and still a writer: see below.
		"groups": []string{"weekly-admins"},
	})

	opened := mcpWith(server, token, listTools)
	if opened.Code != http.StatusOK {
		t.Fatalf("a person Keycloak vouches for was refused on their first MCP call: %d %s", opened.Code, opened.Body.String())
	}
	if !strings.Contains(opened.Body.String(), "weekly_reports_search") {
		t.Errorf("the call was admitted but the listing is empty: %s", opened.Body.String())
	}

	var displayName, email, role, linkedSubject, organization string
	var active bool
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT u.display_name, coalesce(u.email,''), u.role, coalesce(u.oidc_subject,''), u.active, coalesce(o.code,'')
			FROM users u LEFT JOIN organizations o ON o.id=u.organization_id WHERE u.username=$1`, username).
		Scan(&displayName, &email, &role, &linkedSubject, &active, &organization); err != nil {
		t.Fatalf("no account was created for the token's username: %v", err)
	}
	if displayName != "에이전트 먼저" || email != "agent.first@example.com" {
		t.Errorf("the account is named %q <%s>, want the token's name and email", displayName, email)
	}
	// Linked by subject, so a later rename in Keycloak still finds it and the
	// web sign-in adopts it rather than making a second one.
	if linkedSubject != subject {
		t.Errorf("the account is linked to subject %q, want %q", linkedSubject, subject)
	}
	if !active || organization != "DEFAULT" {
		t.Errorf("active=%v organization=%q, want an active account in the default organisation", active, organization)
	}
	// Whether the groups claim reaches an access token is a mapper setting the
	// web sign-in never needed, so the MCP door does not promote anybody.
	if role != "USER" {
		t.Errorf("the MCP door created a %q; only the web sign-in decides who is an administrator", role)
	}

	// The administrator can see where the account came from.
	var audited int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM audit_logs WHERE action='user.sso_provision' AND detail->>'username'=$1 AND detail->>'via'='mcp'`, username).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Errorf("%d audit records for the registration, want 1", audited)
	}

	// The next call finds the account it made, rather than making another.
	if again := mcpWith(server, token, listTools); again.Code != http.StatusOK {
		t.Fatalf("the second call was refused: %d %s", again.Code, again.Body.String())
	}
	if count := server.accountsFor(subject, username); count != 1 {
		t.Errorf("%d accounts after two calls, want 1", count)
	}
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM audit_logs WHERE action='user.sso_provision' AND detail->>'username'=$1`, username).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Errorf("%d registration records after two calls; the second call registered nobody", audited)
	}
}

// guards: oauthAccount
func TestAnAgentsBurstOfFirstCallsMakesOneAccount(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly-web", "", nil)
	server.useIDP(t, idp, "weekly-web", map[string]string{"mcp.oauth.enabled": "true", "oidc.auto_provision": "true"})
	subject, username := "subject-burst", "agent_burst"
	token := accessToken(t, idp, "http://weekly.example.test/mcp", map[string]any{"sub": subject, "preferred_username": username})

	// An MCP client opens with initialize, the initialized notification and a
	// listing, often concurrently. Every one of them finds nobody.
	const calls = 8
	codes := make([]int, calls)
	bodies := make([]string, calls)
	var wait sync.WaitGroup
	for index := range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := mcpWith(server, token, listTools)
			codes[index], bodies[index] = response.Code, response.Body.String()
		}()
	}
	wait.Wait()
	for index, code := range codes {
		if code != http.StatusOK {
			t.Errorf("call %d of a concurrent first connection: %d %s", index, code, bodies[index])
		}
	}
	if count := server.accountsFor(subject, username); count != 1 {
		t.Errorf("%d accounts from one person's first connection, want 1", count)
	}
}

// guards: oauthAccount
func TestTheMCPDoorDoesNotBringBackADeactivatedAccount(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly-web", "", nil)
	server.useIDP(t, idp, "weekly-web", map[string]string{"mcp.oauth.enabled": "true", "oidc.auto_provision": "true"})
	server.createUser("departed_sso", "USER", nil)
	username := server.lastCreatedUsername("departed_sso")
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET active=false, oidc_subject='subject-departed' WHERE username=$1`, username); err != nil {
		t.Fatal(err)
	}

	// The person left; Keycloak may not have caught up. A lookup that only
	// sees active accounts would take them for a stranger and register them.
	for _, claims := range []map[string]any{
		{"sub": "subject-departed", "preferred_username": username},
		// Same subject, renamed in Keycloak.
		{"sub": "subject-departed", "preferred_username": "renamed_after_leaving"},
		// A new subject that carries the old username — a re-created Keycloak user.
		{"sub": "subject-recreated", "preferred_username": strings.ToUpper(username)},
	} {
		response := mcpWith(server, accessToken(t, idp, "http://weekly.example.test/mcp", claims), listTools)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "비활성") {
			t.Errorf("%v: a deactivated person's token was answered %d %s", claims, response.Code, response.Body.String())
		}
	}
	var total, active int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*), count(*) FILTER (WHERE active) FROM users
			WHERE oidc_subject IN ('subject-departed','subject-recreated') OR lower(username) IN (lower($1),'renamed_after_leaving')`, username).
		Scan(&total, &active); err != nil {
		t.Fatal(err)
	}
	if total != 1 || active != 0 {
		t.Errorf("after the refusals there are %d accounts, %d active; want the one deactivated account and nothing else", total, active)
	}
}

// guards: oauthAccount
func TestTheSubjectOutranksALocalAccountThatSharesTheName(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly-web", "", nil)
	server.useIDP(t, idp, "weekly-web", map[string]string{"mcp.oauth.enabled": "true", "oidc.auto_provision": "true"})

	// The SSO person's own account, made by their first MCP call.
	owner := accessToken(t, idp, "http://weekly.example.test/mcp", map[string]any{"sub": "subject-owner", "preferred_username": "sso_owner"})
	if response := mcpWith(server, owner, listTools); response.Code != http.StatusOK {
		t.Fatalf("first call: %d %s", response.Code, response.Body.String())
	}
	// And a local account, never used with SSO, whose name the person is
	// later renamed to in Keycloak.
	server.createUser("local_namesake", "USER", nil)
	namesake := server.lastCreatedUsername("local_namesake")

	// oidc_subject is NULL on the local account; ordered by a bare
	// `oidc_subject=$1 DESC`, NULL sorts first and the token opens the
	// namesake's account instead of its owner's.
	renamed := accessToken(t, idp, "http://weekly.example.test/mcp", map[string]any{"sub": "subject-owner", "preferred_username": namesake})
	principal, err := server.app.oauthPrincipal(server.ctx(), mcpRequest(), renamed)
	if err != nil {
		t.Fatalf("the renamed owner was refused: %v", err)
	}
	if principal.Username != "sso_owner" {
		t.Errorf("a token for subject-owner opened %q's account", principal.Username)
	}
}

func mcpRequest() *http.Request {
	request, _ := http.NewRequest(http.MethodPost, "http://weekly.example.test/mcp", nil)
	request.Host = "weekly.example.test"
	return request
}
