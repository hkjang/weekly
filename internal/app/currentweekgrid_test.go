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
