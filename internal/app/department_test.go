package app

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Keycloak 부서(department)로 소속 조직 자동 지정.
//
// Every account SSO created used to land in the default organisation and wait
// there for an administrator. These hold the department sync to what it
// promises: the directory places people only as far as the administrator
// allowed, never under the wrong one of two same-named teams, never over an
// administrator's own choice, and never at the cost of the sign-in.

// guards: departmentClaim
func TestTheDepartmentIsReadInEveryShapeKeycloakSendsIt(t *testing.T) {
	for _, shape := range []struct {
		name   string
		claims map[string]any
		path   string
		want   string
	}{
		{"문자열", map[string]any{"department": "개발1팀"}, "department", "개발1팀"},
		{"앞뒤·안쪽 공백", map[string]any{"department": "  개발   1팀 "}, "department", "개발 1팀"},
		{"다중값 속성은 첫 값", map[string]any{"department": []any{"", "개발1팀", "기획팀"}}, "department", "개발1팀"},
		{"점이 든 클레임 이름은 중첩 객체", map[string]any{"org": map[string]any{"department": "기획팀"}}, "org.department", "기획팀"},
		{"점이 든 이름 그대로의 클레임이 먼저", map[string]any{"org.department": "그대로", "org": map[string]any{"department": "중첩"}}, "org.department", "그대로"},
		{"없음", map[string]any{"name": "누구"}, "department", ""},
		{"클레임 지정 없음", map[string]any{"department": "개발1팀"}, "", ""},
		{"문자열이 아닌 값", map[string]any{"department": 42}, "department", ""},
	} {
		if got := departmentClaim(shape.claims, shape.path); got != shape.want {
			t.Errorf("%s: %q, want %q", shape.name, got, shape.want)
		}
	}
}

// guards: resolve, generatedDepartmentCode
func TestADepartmentResolvesByMappingThenCodeThenAUniqueName(t *testing.T) {
	devID, planAID, planBID, opsID, madeID := int64(1), int64(2), int64(3), int64(4), int64(5)
	directory := &departmentDirectory{
		aliases: map[string]int64{"마케팅": opsID},
		byCode: map[string]int64{"DEV01": devID, "OPS": opsID,
			generatedDepartmentCode("디자인팀"): madeID},
		byName: map[string][]int64{"개발1팀": {devID}, "기획팀": {planAID, planBID}, "운영팀": {opsID},
			// The organisation made for 디자인팀 was renamed afterwards.
			"브랜드디자인팀": {madeID}},
		names: map[int64]string{},
	}
	for _, value := range []struct {
		department string
		via        string
		want       int64
	}{
		{"마케팅", "alias", opsID},
		{"dev01", "code", devID},
		{"개발1팀", "name", devID},
		{"개발1팀 ", "name", devID},
		{"디자인팀", "code", madeID},
		{"기획팀", "ambiguous", 0},
		{"없는팀", "unmatched", 0},
		{"", "unmatched", 0},
	} {
		match := directory.resolve(value.department)
		if match.Via != value.via {
			t.Errorf("%q: matched by %q, want %q", value.department, match.Via, value.via)
			continue
		}
		got := int64(0)
		if match.OrganizationID != nil {
			got = *match.OrganizationID
		}
		if got != value.want {
			t.Errorf("%q: organisation %d, want %d", value.department, got, value.want)
		}
	}
	if candidates := directory.resolve("기획팀").Candidates; len(candidates) != 2 {
		t.Errorf("an ambiguous name should name both organisations, got %v", candidates)
	}
	// A value that is a valid code keeps it; a Korean one gets a stable digest.
	if code := generatedDepartmentCode("dev01"); code != "DEV01" {
		t.Errorf("an ASCII department became %q", code)
	}
	if first, second := generatedDepartmentCode("디자인 팀"), generatedDepartmentCode(" 디자인  팀"); first != second || !organizationCodePattern.MatchString(first) {
		t.Errorf("the generated code is %q and %q; it should be one valid code", first, second)
	}
}

// accountOrganization is where a username sits, by code, with the department
// the provider last sent.
func (s *testServer) accountOrganization(username string) (code, department string, locked bool) {
	s.t.Helper()
	if err := s.app.db.QueryRow(s.ctx(), `SELECT coalesce(o.code,''), coalesce(u.sso_department,''), u.organization_locked
		FROM users u LEFT JOIN organizations o ON o.id=u.organization_id WHERE u.username=$1`, username).
		Scan(&code, &department, &locked); err != nil {
		s.t.Fatalf("read %s: %v", username, err)
	}
	return code, department, locked
}

func (s *testServer) organizationWithCode(name, code string) int64 {
	s.t.Helper()
	var id int64
	if err := s.app.db.QueryRow(s.ctx(), `INSERT INTO organizations(name, code) VALUES($1, $2) RETURNING id`, name, code).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// arrive signs a person in through idp with the given department.
func (s *testServer) arrive(idp *fakeIDP, subject, username string, department any) {
	s.t.Helper()
	idp.claims = map[string]any{"sub": subject, "preferred_username": username, "name": username, "email": username + "@example.com"}
	if department != nil {
		idp.claims["department"] = department
	}
	if reply := s.signInThrough(s.t, idp, "state-"+subject+fmt.Sprint(harnessAccounts.Add(1)), "the-nonce"); reply.Code >= 400 {
		s.t.Fatalf("%s could not sign in: %d %s", username, reply.Code, reply.Body.String())
	}
}

// guards: oidcCallback, syncUserDepartment
func TestASignInPlacesThePersonInTheirDepartment(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", nil)
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true", "oidc.department_sync": "ALWAYS"})
	server.organizationWithCode("개발1팀", "DEV1")
	server.organizationWithCode("운영팀", "OPS")

	server.arrive(idp, "subject-by-name", "by_name", "개발1팀")
	if code, department, _ := server.accountOrganization("by_name"); code != "DEV1" || department != "개발1팀" {
		t.Errorf("by name: in %q with department %q, want DEV1 / 개발1팀", code, department)
	}
	server.arrive(idp, "subject-by-code", "by_code", []any{"ops"})
	if code, _, _ := server.accountOrganization("by_code"); code != "OPS" {
		t.Errorf("by code: in %q, want OPS", code)
	}
	// A transfer in the directory follows at the next sign-in.
	server.arrive(idp, "subject-by-name", "by_name", "운영팀")
	if code, department, _ := server.accountOrganization("by_name"); code != "OPS" || department != "운영팀" {
		t.Errorf("after a transfer: in %q with %q, want OPS / 운영팀", code, department)
	}
	var audited int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM audit_logs WHERE action='user.department_sync'`).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 3 {
		t.Errorf("%d moves were audited, want 3", audited)
	}
}

// guards: syncUserDepartment
func TestTheSyncGoesOnlyAsFarAsTheAdministratorAllowed(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", nil)
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true", "oidc.department_sync": "OFF"})
	devID := server.organizationWithCode("개발1팀", "DEV1")
	server.organizationWithCode("운영팀", "OPS")

	// OFF records what the directory says and moves nobody.
	server.arrive(idp, "subject-off", "record_only", "개발1팀")
	if code, department, _ := server.accountOrganization("record_only"); code != "DEFAULT" || department != "개발1팀" {
		t.Errorf("OFF: in %q with %q, want DEFAULT and the value recorded", code, department)
	}

	// FILL places somebody still in the default organisation…
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.department_sync": "FILL"})
	server.arrive(idp, "subject-off", "record_only", "개발1팀")
	if code, _, _ := server.accountOrganization("record_only"); code != "DEV1" {
		t.Errorf("FILL from the default organisation: in %q, want DEV1", code)
	}
	// …and leaves somebody already placed where they are.
	server.arrive(idp, "subject-off", "record_only", "운영팀")
	if code, department, _ := server.accountOrganization("record_only"); code != "DEV1" || department != "운영팀" {
		t.Errorf("FILL over a placed account: in %q with %q, want DEV1 kept and 운영팀 recorded", code, department)
	}

	// ALWAYS follows the directory — except where the administrator pinned.
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.department_sync": "ALWAYS"})
	var id int64
	if err := server.app.db.QueryRow(server.ctx(), `SELECT id FROM users WHERE username='record_only'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	pin := server.request(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d", id), map[string]any{
		"displayName": "record_only", "role": "USER", "organizationId": devID, "organizationLocked": true,
	}, server.admin)
	if pin.Code != http.StatusOK {
		t.Fatalf("pin: %d %s", pin.Code, pin.Body.String())
	}
	server.arrive(idp, "subject-off", "record_only", "운영팀")
	if code, _, locked := server.accountOrganization("record_only"); code != "DEV1" || !locked {
		t.Errorf("ALWAYS over a pinned account: in %q locked=%v, want DEV1 kept", code, locked)
	}
	unpin := server.request(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d", id), map[string]any{
		"displayName": "record_only", "role": "USER", "organizationLocked": false,
	}, server.admin)
	if unpin.Code != http.StatusOK {
		t.Fatalf("unpin: %d %s", unpin.Code, unpin.Body.String())
	}
	server.arrive(idp, "subject-off", "record_only", "운영팀")
	if code, _, _ := server.accountOrganization("record_only"); code != "OPS" {
		t.Errorf("ALWAYS after unpinning: in %q, want OPS", code)
	}
}

// guards: syncUserDepartment, createDepartmentOrganization
func TestAnUnknownDepartmentBecomesOneOrganisationOnlyWhenAllowed(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", nil)
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true", "oidc.department_sync": "ALWAYS"})

	server.arrive(idp, "subject-new-1", "first_designer", "디자인팀")
	if code, _, _ := server.accountOrganization("first_designer"); code != "DEFAULT" {
		t.Errorf("with creation off an unknown department moved the account to %q", code)
	}

	server.useIDP(t, idp, "weekly", map[string]string{"oidc.department_create": "true"})
	server.arrive(idp, "subject-new-1", "first_designer", "디자인팀")
	server.arrive(idp, "subject-new-2", "second_designer", " 디자인팀 ")
	first, _, _ := server.accountOrganization("first_designer")
	second, _, _ := server.accountOrganization("second_designer")
	if first == "DEFAULT" || first != second || first != generatedDepartmentCode("디자인팀") {
		t.Errorf("the two designers are in %q and %q, want one organisation coded %q", first, second, generatedDepartmentCode("디자인팀"))
	}
	var made int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM organizations WHERE name='디자인팀'`).Scan(&made); err != nil {
		t.Fatal(err)
	}
	if made != 1 {
		t.Errorf("%d organisations were made for one department", made)
	}
	// Renamed by the administrator, it is still the one the department means.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE organizations SET name='브랜드디자인팀' WHERE code=$1`, first); err != nil {
		t.Fatal(err)
	}
	server.arrive(idp, "subject-new-3", "third_designer", "디자인팀")
	if third, _, _ := server.accountOrganization("third_designer"); third != first {
		t.Errorf("after a rename the department went to %q, want %q", third, first)
	}
}

type departmentOverviewReply struct {
	Data departmentOverview `json:"data"`
}

func (s *testServer) departments() departmentOverview {
	s.t.Helper()
	w := s.request(http.MethodGet, "/api/v1/admin/departments", nil, s.admin)
	if w.Code != http.StatusOK {
		s.t.Fatalf("departments: %d %s", w.Code, w.Body.String())
	}
	var reply departmentOverviewReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		s.t.Fatal(err)
	}
	return reply.Data
}

// guards: adminDepartments, updateDepartmentMapping, applyDepartments
func TestTwoTeamsWithOneNameWaitForTheAdministrator(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", nil)
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true", "oidc.department_sync": "ALWAYS"})
	itPlan := server.organizationWithCode("기획팀", "IT-PLAN")
	server.organizationWithCode("기획팀", "SALES-PLAN")

	server.arrive(idp, "subject-plan-1", "planner_one", "기획팀")
	server.arrive(idp, "subject-plan-2", "planner_two", "기획팀")
	server.arrive(idp, "subject-unknown", "nobody_knows", "신설팀")
	if code, _, _ := server.accountOrganization("planner_one"); code != "DEFAULT" {
		t.Fatalf("an ambiguous name placed somebody in %q", code)
	}

	overview := server.departments()
	if overview.Mode != "ALWAYS" || len(overview.Items) != 2 {
		t.Fatalf("overview: %+v", overview)
	}
	// The value nothing matches first, then the ambiguous one.
	if overview.Items[0].Department != "신설팀" || overview.Items[0].Via != "unmatched" {
		t.Errorf("first row: %+v", overview.Items[0])
	}
	plan := overview.Items[1]
	if plan.Via != "ambiguous" || plan.Users != 2 || len(plan.Candidates) != 2 {
		t.Errorf("기획팀 row: %+v", plan)
	}

	mapped := server.request(http.MethodPut, "/api/v1/admin/departments/mapping",
		map[string]any{"department": " 기획팀", "organizationId": itPlan}, server.admin)
	if mapped.Code != http.StatusOK {
		t.Fatalf("map: %d %s", mapped.Code, mapped.Body.String())
	}
	overview = server.departments()
	for _, row := range overview.Items {
		if row.Department == "기획팀" && (row.Via != "alias" || row.Pending != 2) {
			t.Errorf("after mapping: %+v, want alias with 2 waiting", row)
		}
	}
	// Pinned people are not moved by an apply either.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET organization_locked=true WHERE username='planner_two'`); err != nil {
		t.Fatal(err)
	}
	applied := server.request(http.MethodPost, "/api/v1/admin/departments/apply", map[string]any{"department": "기획팀"}, server.admin)
	if applied.Code != http.StatusOK || decodeData(t, applied)["moved"].(float64) != 1 {
		t.Fatalf("apply: %d %s", applied.Code, applied.Body.String())
	}
	if code, _, _ := server.accountOrganization("planner_one"); code != "IT-PLAN" {
		t.Errorf("planner_one is in %q after apply", code)
	}
	if code, _, _ := server.accountOrganization("planner_two"); code != "DEFAULT" {
		t.Errorf("the pinned planner was moved to %q", code)
	}

	// Removing the mapping returns the value to ambiguous.
	unmapped := server.request(http.MethodPut, "/api/v1/admin/departments/mapping",
		map[string]any{"department": "기획팀", "organizationId": nil}, server.admin)
	if unmapped.Code != http.StatusOK {
		t.Fatalf("unmap: %d %s", unmapped.Code, unmapped.Body.String())
	}
	for _, row := range server.departments().Items {
		if row.Department == "기획팀" && row.Via != "ambiguous" {
			t.Errorf("after unmapping: %+v", row)
		}
	}
	missing := server.request(http.MethodPut, "/api/v1/admin/departments/mapping",
		map[string]any{"department": "기획팀", "organizationId": 999999}, server.admin)
	if missing.Code != http.StatusBadRequest {
		t.Errorf("mapping to no organisation: %d %s", missing.Code, missing.Body.String())
	}
}

// guards: oauthAccount, syncUserDepartment
func TestAnAgentsFirstCallAlsoPlacesThePerson(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly-web", "", nil)
	server.useIDP(t, idp, "weekly-web", map[string]string{
		"mcp.oauth.enabled": "true", "oidc.auto_provision": "true", "oidc.department_sync": "ALWAYS",
	})
	server.organizationWithCode("데이터팀", "DATA")
	token := accessToken(t, idp, "http://weekly.example.test/mcp", map[string]any{
		"sub": "subject-agent-dept", "preferred_username": "agent_dept", "name": "에이전트", "department": "데이터팀",
	})
	if opened := mcpWith(server, token, listTools); opened.Code != http.StatusOK {
		t.Fatalf("first MCP call: %d %s", opened.Code, opened.Body.String())
	}
	if code, department, _ := server.accountOrganization("agent_dept"); code != "DATA" || department != "데이터팀" {
		t.Errorf("the MCP-provisioned account is in %q with %q, want DATA / 데이터팀", code, department)
	}
}

// guards: bulkUpdateUsers
func TestManyAccountsChangeTogetherButNeverTheAdministratorsOwn(t *testing.T) {
	server := newTestServer(t)
	team := server.createOrganization("이관팀", "MOVE")
	ids := []int64{}
	for index := range 3 {
		username := accountName(fmt.Sprintf("bulk%d", index))
		created := server.request(http.MethodPost, "/api/v1/admin/users",
			map[string]any{"username": username, "displayName": username, "role": "USER"}, server.admin)
		if created.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", created.Code, created.Body.String())
		}
		ids = append(ids, int64(decodeData(t, created)["id"].(float64)))
	}
	moved := server.request(http.MethodPost, "/api/v1/admin/users/bulk",
		map[string]any{"ids": ids, "organizationId": team, "organizationLocked": true}, server.admin)
	if moved.Code != http.StatusOK || decodeData(t, moved)["updated"].(float64) != 3 {
		t.Fatalf("bulk move: %d %s", moved.Code, moved.Body.String())
	}
	var placed int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM users WHERE id = ANY($1) AND organization_id=$2 AND organization_locked AND active AND role='USER'`, ids, team).
		Scan(&placed); err != nil {
		t.Fatal(err)
	}
	if placed != 3 {
		t.Errorf("%d of 3 accounts were moved, and nothing else about them should have changed", placed)
	}

	var self int64
	if err := server.app.db.QueryRow(server.ctx(), `SELECT id FROM users WHERE username=$1`, server.adminName).Scan(&self); err != nil {
		t.Fatal(err)
	}
	for _, change := range []map[string]any{{"active": false}, {"role": "USER"}} {
		change["ids"] = append([]int64{self}, ids...)
		refused := server.request(http.MethodPost, "/api/v1/admin/users/bulk", change, server.admin)
		if refused.Code != http.StatusBadRequest || errorCode(refused) != "SELF_LOCKOUT" {
			t.Errorf("%v with the administrator selected: %d %s", change, refused.Code, refused.Body.String())
		}
	}
	var stillActive int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM users WHERE id = ANY($1) AND active`, ids).Scan(&stillActive); err != nil {
		t.Fatal(err)
	}
	if stillActive != 3 {
		t.Errorf("a refused bulk change still deactivated %d accounts", 3-stillActive)
	}
	for _, empty := range []map[string]any{{"ids": []int64{}, "active": false}, {"ids": ids}} {
		if w := server.request(http.MethodPost, "/api/v1/admin/users/bulk", empty, server.admin); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", empty, w.Code, w.Body.String())
		}
	}
	deactivated := server.request(http.MethodPost, "/api/v1/admin/users/bulk", map[string]any{"ids": ids, "active": false}, server.admin)
	if deactivated.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", deactivated.Code, deactivated.Body.String())
	}
	listed := server.request(http.MethodGet, fmt.Sprintf("/api/v1/admin/users?status=inactive&organization=%d", team), nil, server.admin)
	if total := decodeData(t, listed)["total"].(float64); total != 3 {
		t.Errorf("the inactive filter in that organisation found %v accounts, want 3", total)
	}
}

// guards: adminUsers, exportUsers
func TestTheDirectoryFiltersAndItsFileAgree(t *testing.T) {
	server := newTestServer(t)
	idp := newIDP(t, "weekly", "the-nonce", nil)
	server.useIDP(t, idp, "weekly", map[string]string{"oidc.auto_provision": "true", "oidc.department_sync": "OFF"})
	devID := server.organizationWithCode("개발1팀", "DEV1")
	server.arrive(idp, "subject-dir", "=formula_person", "개발1팀")
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE users SET last_login_at = now() - interval '200 days' WHERE username='=formula_person'`); err != nil {
		t.Fatal(err)
	}

	listed := server.request(http.MethodGet, "/api/v1/admin/users?activity=stale&q="+url.QueryEscape("개발1팀"), nil, server.admin)
	if listed.Code != http.StatusOK {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
	var page struct {
		Data userListView `json:"data"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Data.Total != 1 || len(page.Data.Items) != 1 {
		t.Fatalf("stale + department search found %d", page.Data.Total)
	}
	person := page.Data.Items[0]
	if !person.SSO || person.LocalPassword || person.SSODepartment == nil || *person.SSODepartment != "개발1팀" ||
		person.DepartmentOrganizationID == nil || *person.DepartmentOrganizationID != devID || page.Data.DepartmentSync != "OFF" {
		t.Errorf("the listed account does not say how it signs in or where the directory puts it: %+v", person)
	}

	export := server.request(http.MethodGet, "/api/v1/admin/users/export?activity=stale", nil, server.admin)
	if export.Code != http.StatusOK || !strings.HasPrefix(export.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("export: %d %s", export.Code, export.Header().Get("Content-Type"))
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(export.Body.String(), "\uFEFF"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// The administrator signed in just now and is not stale, so the file is
	// the header and the one person the screen showed.
	if len(records) != 2 {
		t.Fatalf("the file has %d rows, want the header and 1: %v", len(records), records)
	}
	if records[1][0] != "'=formula_person" {
		t.Errorf("a username that reads as a formula was written as %q", records[1][0])
	}
	if records[1][6] != "개발1팀" || records[1][7] != "개발1팀" || records[1][9] != "Y" {
		t.Errorf("the department columns read %v", records[1])
	}
}
