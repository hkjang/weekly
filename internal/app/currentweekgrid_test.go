package app

import (
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
