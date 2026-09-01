package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fillInclusionTestReport(t *testing.T, server *testServer, cookie *http.Cookie, id int64, version int, summary, title string) {
	t.Helper()
	response := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": summary,
		"version": version,
		"items": []map[string]any{{
			"category": "개발", "title": title, "currentResult": "구현 완료",
			"nextPlan": "운영 검증", "issue": "검토 대기", "managementAsk": "결정 요청", "progress": 75,
		}},
	}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("fill report %d: %d %s", id, response.Code, response.Body.String())
	}
}

func materialRows(t *testing.T, responseBody map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := responseBody[key].([]any)
	if !ok {
		t.Fatalf("%s is not an array: %#v", key, responseBody[key])
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		row, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s contains a non-object: %#v", key, value)
		}
		rows = append(rows, row)
	}
	return rows
}

func rowByUserID(rows []map[string]any, userID int64) map[string]any {
	for _, row := range rows {
		if id, _ := row["userId"].(float64); int64(id) == userID {
			return row
		}
		if id, _ := row["id"].(float64); int64(id) == userID {
			return row
		}
	}
	return nil
}

// guards: myReportInclusions, updateMyReportInclusions, reportInclusionPreferenceFor
func TestReportInclusionPreferencesAreScopedNormalisedAndAudited(t *testing.T) {
	server := newTestServer(t)
	root := server.createOrganization("포함 본부", "INCPREFROOT")
	child := server.createChildOrganization("포함 팀", "INCPREFCHILD", &root)
	other := server.createOrganization("다른 본부", "INCPREFOTHER")

	leader := server.createUser("include_pref_leader", "ORG_MANAGER", &root)
	server.createUser("include_pref_member", "USER", &child)
	inactive := server.createUser("include_pref_inactive", "USER", &child)
	outside := server.createUser("include_pref_outside", "USER", &other)
	plain := server.createUser("include_pref_plain", "USER", &root)

	leaderID := server.userIDOf(server.lastCreatedUsername("include_pref_leader"))
	memberID := server.userIDOf(server.lastCreatedUsername("include_pref_member"))
	inactiveID := server.userIDOf(server.lastCreatedUsername("include_pref_inactive"))
	outsideID := server.userIDOf(server.lastCreatedUsername("include_pref_outside"))
	plainID := server.userIDOf(server.lastCreatedUsername("include_pref_plain"))
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET active=false WHERE id=$1`, inactiveID); err != nil {
		t.Fatal(err)
	}

	initial := server.request(http.MethodGet, "/api/v1/me/report-inclusions", nil, leader)
	if initial.Code != http.StatusOK {
		t.Fatalf("get preferences: %d %s", initial.Code, initial.Body.String())
	}
	view := decodeData(t, initial)
	if view["available"] != true || int(view["maxMembers"].(float64)) != maxReportInclusionMembers {
		t.Fatalf("preference capabilities: %#v", view)
	}
	candidates := materialRows(t, view, "members")
	if rowByUserID(candidates, memberID) == nil || rowByUserID(candidates, plainID) == nil {
		t.Fatalf("eligible subtree members are missing: %#v", candidates)
	}
	if rowByUserID(candidates, leaderID) != nil || rowByUserID(candidates, inactiveID) != nil || rowByUserID(candidates, outsideID) != nil {
		t.Fatalf("self, inactive, or outside member was offered: %#v", candidates)
	}
	memberRow := rowByUserID(candidates, memberID)
	if memberRow["username"] == "" || memberRow["organizationName"] != "포함 팀" {
		t.Errorf("candidate identity is incomplete: %#v", memberRow)
	}

	// Duplicate ids describe one selection and produce one join row.
	saved := server.request(http.MethodPut, "/api/v1/me/report-inclusions",
		map[string]any{"memberIds": []int64{memberID, memberID}}, leader)
	if saved.Code != http.StatusOK {
		t.Fatalf("save selection: %d %s", saved.Code, saved.Body.String())
	}
	selected, _ := decodeData(t, saved)["selectedMemberIds"].([]any)
	if len(selected) != 1 || int64(selected[0].(float64)) != memberID {
		t.Errorf("duplicate selection was not normalised: %#v", selected)
	}
	var rows int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM user_report_inclusions WHERE owner_user_id=$1`, leaderID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("duplicate request stored %d rows", rows)
	}
	var audits int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM audit_logs
		WHERE actor_id=$1 AND action='weekly.report_inclusions'`, leaderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Errorf("selection save wrote %d audit events", audits)
	}

	// Every invalid target gets the same refusal and the previous valid set is
	// left intact. The endpoint must not reveal which of these ids exists.
	for name, id := range map[string]int64{
		"self": leaderID, "inactive": inactiveID, "outside": outsideID, "missing": 9_999_999_999,
	} {
		refused := server.request(http.MethodPut, "/api/v1/me/report-inclusions", map[string]any{"memberIds": []int64{id}}, leader)
		if refused.Code != http.StatusForbidden || errorCode(refused) != "REPORT_INCLUSION_MEMBER_FORBIDDEN" {
			t.Errorf("%s target answered %d/%s: %s", name, refused.Code, errorCode(refused), refused.Body.String())
		}
	}
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM user_report_inclusions
		WHERE owner_user_id=$1 AND member_user_id=$2`, leaderID, memberID).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("refused writes changed the valid selection: rows=%d err=%v", rows, err)
	}

	ordinaryView := decodeData(t, server.request(http.MethodGet, "/api/v1/me/report-inclusions", nil, plain))
	if ordinaryView["available"] != false || len(materialRows(t, ordinaryView, "members")) != 0 {
		t.Fatalf("ordinary user was offered report inclusion: %#v", ordinaryView)
	}
	ordinaryWrite := server.request(http.MethodPut, "/api/v1/me/report-inclusions", map[string]any{"memberIds": []int64{memberID}}, plain)
	if ordinaryWrite.Code != http.StatusForbidden || errorCode(ordinaryWrite) != "REPORT_INCLUSION_ROLE_REQUIRED" {
		t.Fatalf("ordinary user selected a colleague: %d %s", ordinaryWrite.Code, ordinaryWrite.Body.String())
	}
	// Empty is still useful after a demotion: it lets the owner explicitly erase
	// old rows without granting any new visibility.
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_report_inclusions(owner_user_id,member_user_id) VALUES($1,$2)`, plainID, memberID); err != nil {
		t.Fatal(err)
	}
	cleared := server.request(http.MethodPut, "/api/v1/me/report-inclusions", map[string]any{"memberIds": []int64{}}, plain)
	if cleared.Code != http.StatusOK {
		t.Fatalf("ordinary user could not clear stale selection: %d %s", cleared.Code, cleared.Body.String())
	}
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM user_report_inclusions WHERE owner_user_id=$1`, plainID).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("empty selection left %d rows: %v", rows, err)
	}

	// Authentication can race an administrator edit. Exercise the handler with
	// the stale principal that existed at request start while the locked database
	// row says this account is now an ordinary user. The saved response must use
	// the locked authority snapshot, not disclose the stale administrator list.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET role='USER' WHERE id=$1`, leaderID); err != nil {
		t.Fatal(err)
	}
	stale := &principal{ID: leaderID, Role: "ADMIN", OrganizationID: &root}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/me/report-inclusions", strings.NewReader(`{"memberIds":[]}`))
	request = request.WithContext(context.WithValue(request.Context(), principalContext, stale))
	recorder := httptest.NewRecorder()
	server.app.updateMyReportInclusions(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("clear after concurrent demotion: %d %s", recorder.Code, recorder.Body.String())
	}
	postDemotion := decodeData(t, recorder)
	if postDemotion["available"] != false || len(materialRows(t, postDemotion, "members")) != 0 {
		t.Fatalf("save response used stale administrator authority: %#v", postDemotion)
	}

	_ = outside
	_ = inactive
}

func TestReportInclusionIDNormalisationHasABound(t *testing.T) {
	values := []int64{4, 2, 4, 3}
	normalised, err := normaliseReportInclusionIDs(values)
	if err != nil || fmt.Sprint(normalised) != "[2 3 4]" {
		t.Fatalf("normalise %v = %v, %v", values, normalised, err)
	}
	tooMany := make([]int64, maxReportInclusionMembers+1)
	for index := range tooMany {
		tooMany[index] = int64(index + 1)
	}
	if _, err := normaliseReportInclusionIDs(tooMany); err == nil {
		t.Fatal("an unbounded selection was accepted")
	}
	if _, err := normaliseReportInclusionIDs([]int64{0}); err == nil {
		t.Fatal("zero was accepted as a user id")
	}
}

// guards: currentIncludedMaterials, includedMaterialsFor, loadReport
func TestIncludedMaterialsUseTheExactWeekWithoutCopyingReportItems(t *testing.T) {
	server := newTestServer(t)
	root := server.createOrganization("자료 본부", "INCMATROOT")
	child := server.createChildOrganization("자료 팀", "INCMATCHILD", &root)
	other := server.createOrganization("이동 본부", "INCMATOTHER")
	leader := server.createUser("include_material_leader", "ORG_MANAGER", &root)
	member := server.createUser("include_material_member", "TEAM_LEADER", &child)
	missing := server.createUser("include_material_missing", "USER", &child)
	nested := server.createUser("include_material_nested", "USER", &child)

	leaderID := server.userIDOf(server.lastCreatedUsername("include_material_leader"))
	memberID := server.userIDOf(server.lastCreatedUsername("include_material_member"))
	missingID := server.userIDOf(server.lastCreatedUsername("include_material_missing"))
	nestedID := server.userIDOf(server.lastCreatedUsername("include_material_nested"))
	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	weekText := week.Format("2006-01-02")
	previousText := week.AddDate(0, 0, -7).Format("2006-01-02")

	memberReport, memberVersion := server.draft(member, weekText, "팀원 이번 주 요약")
	fillInclusionTestReport(t, server, member, memberReport, memberVersion, "팀원 이번 주 요약", "팀원 원본 업무")
	// The selected but missing member does have another week. Exact week_start,
	// not "latest report", decides whether material exists.
	oldMissingReport, oldMissingVersion := server.draft(missing, previousText, "지난주만 작성")
	fillInclusionTestReport(t, server, missing, oldMissingReport, oldMissingVersion, "지난주만 작성", "지난주 업무")
	nestedReport, nestedVersion := server.draft(nested, weekText, "재귀되면 안 되는 요약")
	fillInclusionTestReport(t, server, nested, nestedReport, nestedVersion, "재귀되면 안 되는 요약", "재귀되면 안 되는 업무")
	memberSelection := server.request(http.MethodPut, "/api/v1/me/report-inclusions",
		map[string]any{"memberIds": []int64{nestedID}}, member)
	if memberSelection.Code != http.StatusOK {
		t.Fatalf("selected member's own selection: %d %s", memberSelection.Code, memberSelection.Body.String())
	}

	ownerReport, ownerVersion := server.draft(leader, weekText, "팀장 본인 요약")
	fillInclusionTestReport(t, server, leader, ownerReport, ownerVersion, "팀장 본인 요약", "팀장 본인 업무")
	selected := server.request(http.MethodPut, "/api/v1/me/report-inclusions",
		map[string]any{"memberIds": []int64{memberID, missingID}}, leader)
	if selected.Code != http.StatusOK {
		t.Fatalf("select members: %d %s", selected.Code, selected.Body.String())
	}

	current := server.request(http.MethodGet, "/api/v1/reports/current/included-materials", nil, leader)
	if current.Code != http.StatusOK {
		t.Fatalf("current materials: %d %s", current.Code, current.Body.String())
	}
	currentData := decodeData(t, current)
	if currentData["weekStart"] != weekText {
		t.Errorf("materials week=%v, want %s", currentData["weekStart"], weekText)
	}
	materials := materialRows(t, currentData, "materials")
	if len(materials) != 2 {
		t.Fatalf("got %d selected materials, want 2: %s", len(materials), current.Body.String())
	}
	written := rowByUserID(materials, memberID)
	if written == nil || int64(written["reportId"].(float64)) != memberReport || written["status"] != "DRAFT" || written["summary"] != "팀원 이번 주 요약" {
		t.Fatalf("written member material: %#v", written)
	}
	if written["includedMaterials"] != nil || strings.Contains(current.Body.String(), "재귀되면 안 되는") {
		t.Fatalf("a selected leader's own inclusions recursed into the outer report: %s", current.Body.String())
	}
	items := materialRows(t, written, "items")
	if len(items) != 1 || items[0]["title"] != "팀원 원본 업무" || items[0]["managementAsk"] != "결정 요청" {
		t.Fatalf("source report was not carried in full: %#v", items)
	}
	unwritten := rowByUserID(materials, missingID)
	if unwritten == nil || unwritten["reportId"] != nil || len(materialRows(t, unwritten, "items")) != 0 {
		t.Fatalf("missing member was not represented as empty material: %#v", unwritten)
	}

	var before, after int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM report_items`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	detail := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", ownerReport), nil, leader)
	if detail.Code != http.StatusOK {
		t.Fatalf("owner report: %d %s", detail.Code, detail.Body.String())
	}
	detailMaterials := materialRows(t, decodeData(t, detail), "includedMaterials")
	if len(detailMaterials) != 2 || rowByUserID(detailMaterials, memberID) == nil {
		t.Fatalf("loadReport did not attach materials: %s", detail.Body.String())
	}
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM report_items`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("reading inclusion copied report_items: before=%d after=%d", before, after)
	}

	// Moving a selected member out of the owner's current subtree removes the
	// material immediately but retains the preference row for a possible move
	// back. No stale selection is permission.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET organization_id=$2 WHERE id=$1`, memberID, other); err != nil {
		t.Fatal(err)
	}
	afterMove := materialRows(t, decodeData(t, server.request(http.MethodGet,
		"/api/v1/reports/current/included-materials", nil, leader)), "materials")
	if rowByUserID(afterMove, memberID) != nil || rowByUserID(afterMove, missingID) == nil {
		t.Fatalf("current organisation scope was not reapplied: %#v", afterMove)
	}
	var retained int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM user_report_inclusions
		WHERE owner_user_id=$1 AND member_user_id=$2`, leaderID, memberID).Scan(&retained); err != nil || retained != 1 {
		t.Errorf("organisation move erased the stored choice: rows=%d err=%v", retained, err)
	}

	// A demotion masks every old choice, including on an already stored report.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET role='USER' WHERE id=$1`, leaderID); err != nil {
		t.Fatal(err)
	}
	masked := decodeData(t, server.request(http.MethodGet, "/api/v1/me/report-inclusions", nil, leader))
	if masked["available"] != false || len(materialRows(t, masked, "members")) != 0 {
		t.Fatalf("demoted owner still has report inclusion authority: %#v", masked)
	}
	demotedDetail := decodeData(t, server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", ownerReport), nil, leader))
	if len(materialRows(t, demotedDetail, "includedMaterials")) != 0 {
		t.Fatalf("demoted owner's report still exposes included materials: %#v", demotedDetail["includedMaterials"])
	}

}

// guards: includedMaterialsFor, loadReport
func TestNestedMaterialsNeverBroadenTheOuterReportPermission(t *testing.T) {
	server := newTestServer(t)
	insideOrg := server.createOrganization("안쪽 조직", "INCSECIN")
	outsideOrg := server.createOrganization("바깥 조직", "INCSECOUT")
	insideLeader := server.createUser("include_security_leader", "TEAM_LEADER", &insideOrg)
	adminOwner := server.createUser("include_security_admin", "ADMIN", &insideOrg)
	outsideMember := server.createUser("include_security_outside", "USER", &outsideOrg)
	adminID := server.userIDOf(server.lastCreatedUsername("include_security_admin"))
	outsideID := server.userIDOf(server.lastCreatedUsername("include_security_outside"))
	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY")).Format("2006-01-02")

	source, sourceVersion := server.draft(outsideMember, week, "다른 조직 비밀 요약")
	fillInclusionTestReport(t, server, outsideMember, source, sourceVersion, "다른 조직 비밀 요약", "다른 조직 비밀 업무")
	outer, outerVersion := server.draft(adminOwner, week, "관리자 본인 보고")
	fillInclusionTestReport(t, server, adminOwner, outer, outerVersion, "관리자 본인 보고", "관리자 업무")
	selected := server.request(http.MethodPut, "/api/v1/me/report-inclusions", map[string]any{"memberIds": []int64{outsideID}}, adminOwner)
	if selected.Code != http.StatusOK {
		t.Fatalf("admin selection: %d %s", selected.Code, selected.Body.String())
	}

	ownerView := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", outer), nil, adminOwner)
	if !strings.Contains(ownerView.Body.String(), "다른 조직 비밀 업무") {
		t.Fatalf("admin owner cannot see selected material: %s", ownerView.Body.String())
	}
	// Existing report rules let this leader view reports authored in their own
	// organisation, including this administrator's. That outer permission must
	// not carry the administrator's company-wide selection with it.
	readerView := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", outer), nil, insideLeader)
	if readerView.Code != http.StatusOK {
		t.Fatalf("inside leader cannot read outer report: %d %s", readerView.Code, readerView.Body.String())
	}
	if strings.Contains(readerView.Body.String(), "다른 조직 비밀") {
		t.Fatalf("nested material broadened the outer permission: %s", readerView.Body.String())
	}
	if len(materialRows(t, decodeData(t, readerView), "includedMaterials")) != 0 {
		t.Fatalf("outside material remained in the nested array: %s", readerView.Body.String())
	}

	var stored int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM user_report_inclusions WHERE owner_user_id=$1`, adminID).Scan(&stored); err != nil || stored != 1 {
		t.Errorf("viewer filtering changed the owner's preference: rows=%d err=%v", stored, err)
	}
}
