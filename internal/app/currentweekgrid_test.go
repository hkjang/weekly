package app

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// setWeekStart moves the reporting grid, confirming the consequence the
// administrator is shown when stored reports would fall off the new grid.
func (s *testServer) setWeekStart(weekday string) {
	s.t.Helper()
	w := s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings":  map[string]string{"workflow.week_start": weekday},
		"confirmed": []string{"workflow.week_start"},
	}, s.admin)
	if w.Code != http.StatusOK {
		s.t.Fatalf("move the week start to %s: %d %s", weekday, w.Code, w.Body.String())
	}
}

// Moving the week start weekday moves the grid, not the reports already on it.
//
// For the one transition week the report an author is writing covers these days
// under a different date. weekIsFree already refuses to let them file a second
// one for the same days — but the screen that would have offered it asked for
// the current report by exact date and was told there is none, so the author
// was shown a blank editor whose save could only ever answer
// REPORT_PERIOD_OVERLAPS. The warning the administrator confirms says the
// author must open the existing report and carry on in it; this is the screen
// doing that for them.
//
// guards: currentReport, weekCoveringDays
func TestCurrentReportOpensTheReportCoveringTheseDaysAfterTheGridMoves(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("gridcurrent_author", "USER", nil)

	location := server.app.serviceLocation(server.ctx())
	// The grid is about to move two days forward, so the week the product will
	// address begins two days after the one this report was written on.
	moved := currentWeekStart(time.Now().In(location), "WEDNESDAY")
	previousGrid := moved.AddDate(0, 0, -2).Format("2006-01-02")
	id, _ := server.draft(author, previousGrid, "옛 격자에서 작성 중")

	server.setWeekStart("WEDNESDAY")

	w := server.request(http.MethodGet, "/api/v1/reports/current", nil, author)
	if w.Code != http.StatusOK {
		t.Fatalf("read the current report: %d %s", w.Code, w.Body.String())
	}
	current := decodeData(t, w)
	if current == nil {
		t.Fatalf("the author's report covering these days disappeared from the writing screen: %s", w.Body.String())
	}
	if opened, _ := current["id"].(float64); int64(opened) != id {
		t.Errorf("opened report %v, want the one covering these days (%d)", current["id"], id)
	}
	// The period it actually covers, not the date the grid now names, so the
	// author can see which week they are carrying on with.
	if current["weekStart"] != previousGrid {
		t.Errorf("weekStart %v, want %s", current["weekStart"], previousGrid)
	}

	// And the refusal it replaces: the blank editor's save had nowhere to go.
	created := server.request(http.MethodPost, "/api/v1/reports",
		map[string]any{"weekStart": moved.Format("2006-01-02"), "summary": "새 격자에서 다시 작성"}, author)
	if created.Code != http.StatusConflict || errorCode(created) != "REPORT_PERIOD_OVERLAPS" {
		t.Errorf("filing a second report for the same days answered %d %s", created.Code, created.Body.String())
	}
}

// The same transition week seen from the leader's deck, where being told wrong
// is louder: a selected member's material was looked up by exact date, so for
// that one week the member who had written — and whom the product would not let
// write again on the new date — came back with no report at all. The PPTX turns
// that into "주간보고 미작성" beside their name in front of management.
//
// guards: includedMaterialsFor, weekCoveringDays
func TestIncludedMaterialsFindTheMemberReportCoveringTheseDaysAfterTheGridMoves(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("격자 자료 본부", "GRIDMATERIAL")
	leader := server.createUser("gridmaterial_leader", "ORG_MANAGER", &org)
	member := server.createUser("gridmaterial_member", "USER", &org)
	memberID := server.userIDOf(server.lastCreatedUsername("gridmaterial_member"))

	location := server.app.serviceLocation(server.ctx())
	moved := currentWeekStart(time.Now().In(location), "WEDNESDAY")
	previousGrid := moved.AddDate(0, 0, -2).Format("2006-01-02")

	memberReport, memberVersion := server.draft(member, previousGrid, "팀원 옛 격자 요약")
	fillInclusionTestReport(t, server, member, memberReport, memberVersion, "팀원 옛 격자 요약", "팀원 옛 격자 업무")
	selected := server.request(http.MethodPut, "/api/v1/me/report-inclusions",
		map[string]any{"memberIds": []int64{memberID}}, leader)
	if selected.Code != http.StatusOK {
		t.Fatalf("select the member: %d %s", selected.Code, selected.Body.String())
	}

	server.setWeekStart("WEDNESDAY")

	current := server.request(http.MethodGet, "/api/v1/reports/current/included-materials", nil, leader)
	if current.Code != http.StatusOK {
		t.Fatalf("current materials: %d %s", current.Code, current.Body.String())
	}
	materials := materialRows(t, decodeData(t, current), "materials")
	written := rowByUserID(materials, memberID)
	if written == nil {
		t.Fatalf("the selected member left the leader's materials entirely: %s", current.Body.String())
	}
	if written["reportId"] == nil {
		t.Fatalf("the member's report covering these days was reported as unwritten: %#v", written)
	}
	if int64(written["reportId"].(float64)) != memberReport || written["summary"] != "팀원 옛 격자 요약" {
		t.Fatalf("wrong member report attached: %#v", written)
	}
	if len(materialRows(t, written, "items")) != 1 {
		t.Errorf("the member's work did not come with the material: %#v", written["items"])
	}

	// A week the member genuinely did not write is still empty; the fix widens
	// the question to the seven days, not to "their latest report".
	nextWeek := moved.AddDate(0, 0, 7).Format("2006-01-02")
	leaderNext, _ := server.draft(leader, nextWeek, "팀장 다음 주 요약")
	detail := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", leaderNext), nil, leader)
	if detail.Code != http.StatusOK {
		t.Fatalf("next week report: %d %s", detail.Code, detail.Body.String())
	}
	nextMaterials := materialRows(t, decodeData(t, detail), "includedMaterials")
	unwritten := rowByUserID(nextMaterials, memberID)
	if unwritten == nil || unwritten["reportId"] != nil {
		t.Errorf("a week the member did not report was filled from another week: %#v", unwritten)
	}
}

// The transition week seen from the panel beside the editor, which exists to
// stop the author retyping what they promised.
//
// currentReport now opens the report covering these days even though it carries
// an earlier date — and "last week's plan" asked for the most recent report with
// an earlier date, so it answered with that same open report. The author was
// shown the work in front of them as last week's promises, every item paired
// with itself and everything they had already reported at 100% offered back as
// still owed. The plans they actually made a week ago, the only thing the panel
// is for, were one row further down and never reached.
//
// guards: previousWeekPlan, weekEndedBefore
func TestPreviousPlanSkipsTheOpenReportCoveringTheseDaysAfterTheGridMoves(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("gridprevious_author", "USER", nil)

	location := server.app.serviceLocation(server.ctx())
	moved := currentWeekStart(time.Now().In(location), "WEDNESDAY")
	previousGrid := moved.AddDate(0, 0, -2)
	// The week whose plans the author actually wants back.
	earlier := previousGrid.AddDate(0, 0, -7)

	earlierID, earlierVersion := server.draft(author, earlier.Format("2006-01-02"), "지난주 요약")
	fillInclusionTestReport(t, server, author, earlierID, earlierVersion, "지난주 요약", "지난주 약속한 업무")
	openID, openVersion := server.draft(author, previousGrid.Format("2006-01-02"), "이번 주 요약")
	fillInclusionTestReport(t, server, author, openID, openVersion, "이번 주 요약", "이번 주 쓰고 있는 업무")

	server.setWeekStart("WEDNESDAY")

	w := server.request(http.MethodGet, "/api/v1/reports/previous", nil, author)
	if w.Code != http.StatusOK {
		t.Fatalf("read last week's plans: %d %s", w.Code, w.Body.String())
	}
	previous := decodeData(t, w)
	if previous == nil {
		t.Fatalf("last week's plans went missing: %s", w.Body.String())
	}
	if id, _ := previous["reportId"].(float64); int64(id) == openID {
		t.Fatalf("the report the author is writing came back as last week's plan: %#v", previous)
	} else if int64(id) != earlierID {
		t.Fatalf("reportId %v, want the week before this one (%d)", previous["reportId"], earlierID)
	}
	if previous["weekStart"] != earlier.Format("2006-01-02") {
		t.Errorf("weekStart %v, want %s", previous["weekStart"], earlier.Format("2006-01-02"))
	}
	items := materialRows(t, previous, "items")
	if len(items) != 1 || items[0]["title"] != "지난주 약속한 업무" {
		t.Fatalf("wrong plans offered back: %#v", items)
	}
	if items[0]["carryOver"] != true {
		t.Errorf("the unfinished plan was not offered as still owed: %#v", items[0])
	}
}

// The same transition seen one week later, by the author who asked never to
// have to start from a blank page.
//
// The automatic clone already refuses to overwrite a report covering the target
// week, so on the transition week itself it correctly stands aside and lets the
// author carry on in the report they have. The week after that it went looking
// for the source by exact date, found nothing where the moved grid says last
// week began, marked the week processed and produced no copy — the one thing
// the preference exists to do, skipped once, silently, for the author least
// likely to be watching for it.
//
// guards: runAutomaticCloneForUser, weekCoveringDays
func TestAutomaticCloneCopiesTheReportCoveringLastWeekAfterTheGridMoves(t *testing.T) {
	server := newTestServer(t)
	author := createAutomationTestAccount(t, server, "gridclone_author", "USER", nil)

	location := server.app.serviceLocation(server.ctx())
	moved := currentWeekStart(time.Now().In(location), "WEDNESDAY")
	previousGrid := moved.AddDate(0, 0, -2)

	sourceID, version := server.draft(author.cookie, previousGrid.Format("2006-01-02"), "옛 격자 요약")
	fillInclusionTestReport(t, server, author.cookie, sourceID, version, "옛 격자 요약", "옛 격자 업무")
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_weekly_preferences
		(user_id,auto_clone_previous,team_reminder_enabled,team_reminder_weekday)
		VALUES($1,true,false,'FRIDAY')`, author.id); err != nil {
		t.Fatal(err)
	}

	server.setWeekStart("WEDNESDAY")

	// The transition week: the author's existing report covers these days, so
	// there is nothing to clone into and the run stands aside.
	if err := server.app.runAutomaticClones(server.ctx(), moved); err != nil {
		t.Fatal(err)
	}
	var duplicates int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM weekly_reports
		WHERE user_id=$1 AND week_start=$2`, author.id, moved).Scan(&duplicates); err != nil {
		t.Fatal(err)
	}
	if duplicates != 0 {
		t.Fatalf("the clone filed a second report for days the author had already covered")
	}

	// The week after, whose source is that same report sitting two days off the
	// date the arithmetic produces.
	target := moved.AddDate(0, 0, 7)
	if err := server.app.runAutomaticClones(server.ctx(), target); err != nil {
		t.Fatal(err)
	}
	var clonedID int64
	var summary, sourceRef string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT id,summary,source_ref FROM weekly_reports
		WHERE user_id=$1 AND week_start=$2`, author.id, target).Scan(&clonedID, &summary, &sourceRef); err != nil {
		t.Fatalf("the automatic copy of last week's report was skipped: %v", err)
	}
	if summary != "옛 격자 요약" || sourceRef != fmt.Sprintf("report:%d", sourceID) {
		t.Errorf("cloned from summary=%q source=%s, want the report covering last week (%d)", summary, sourceRef, sourceID)
	}
	var carried int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM report_items WHERE report_id=$1`, clonedID).Scan(&carried); err != nil {
		t.Fatal(err)
	}
	if carried != 1 {
		t.Errorf("the copy carried %d items, want the one the source had", carried)
	}
}

// The same transition week on the analytics screen, where reports that exist
// read as reports nobody wrote.
//
// 제출률, 상태별 분포, 미해결 이슈 and 평균 진행률 were all asked by exact date,
// so for the one week whose reports sit on the old grid the leader's dashboard
// answered 0% with an empty breakdown — about a week in which the team had
// reported and, because weekIsFree refuses a second report for the same days,
// could not report again. The MCP week summary reads the same figures, where
// there is no screen to notice they are impossible.
//
// guards: analyticsOverviewContext, weekCoveringDays
func TestAnalyticsOverviewCountsTheReportsCoveringTheseDaysAfterTheGridMoves(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("격자 분석 본부", "GRIDANALYTICS")
	leader := server.createUser("gridanalytics_leader", "ORG_MANAGER", &org)
	member := server.createUser("gridanalytics_member", "USER", &org)

	location := server.app.serviceLocation(server.ctx())
	moved := currentWeekStart(time.Now().In(location), "WEDNESDAY")
	previousGrid := moved.AddDate(0, 0, -2).Format("2006-01-02")
	server.weekWithIssue(member, previousGrid, "격자 분석 과제", "장비 입고가 늦습니다", 40)

	// Somebody outside this leader's organisation who reported for the same
	// days. Widening the question from a date to a period must not also widen
	// whose reports the leader is shown.
	outside := server.createOrganization("격자 밖 본부", "GRIDOUTSIDE")
	stranger := server.createUser("gridanalytics_stranger", "USER", &outside)
	server.weekWithIssue(stranger, previousGrid, "남의 조직 과제", "남의 조직 이슈", 100)

	server.setWeekStart("WEDNESDAY")

	w := server.request(http.MethodGet, "/api/v1/analytics/overview", nil, leader)
	if w.Code != http.StatusOK {
		t.Fatalf("read the analytics overview: %d %s", w.Code, w.Body.String())
	}
	overview := decodeData(t, w)
	if overview["weekStart"] != moved.Format("2006-01-02") {
		t.Fatalf("the overview answered for week %v, want the current week %s", overview["weekStart"], moved.Format("2006-01-02"))
	}
	if submitted, _ := overview["submittedUsers"].(float64); int(submitted) != 1 {
		t.Errorf("제출 %v명, want the one member whose report covers these days — 상태별 %v",
			overview["submittedUsers"], overview["statusCounts"])
	}
	if rate, _ := overview["submissionRate"].(float64); rate <= 0 {
		t.Errorf("제출률 %v%%, want the rate for a week the team reported in", overview["submissionRate"])
	}
	// The other two figures on the same screen have to describe the same
	// reports, not a different week's.
	if issues, _ := overview["openIssues"].(float64); int(issues) != 1 {
		t.Errorf("미해결 이슈 %v건, want the one the member wrote", overview["openIssues"])
	}
	if progress, _ := overview["averageProgress"].(float64); progress != 40 {
		t.Errorf("평균 진행률 %v, want the 40 the member reported", overview["averageProgress"])
	}
}
