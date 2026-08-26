package app

import (
	"net/http"
	"testing"
)

// "이슈 해소까지 중앙값 N주" is a duration, and a duration is measured on the
// calendar. Counting the reports that mentioned an obstacle instead tells a
// leader that obstacles clear faster than they do — and the weeks somebody was
// away are exactly the weeks nothing was being cleared.

// guards: issueClearanceFor
func TestIssueClearanceMeasuresWeeksNotMentions(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("clearance_author", "USER", nil)
	// One ending per task per week is a database rule, so the two endings below
	// belong to two tasks.
	steady, gapped := twoWorkItemsOf(t, server, author)

	record := func(workItemID int64, started, ended string, mentions int) {
		t.Helper()
		if _, err := server.app.db.Exec(server.ctx(),
			`INSERT INTO work_item_issue_outcomes(work_item_id, started_week, ended_week, weeks, outcome, issue_text)
			 VALUES ($1, $2::date, $3::date, $4, 'RESOLVED', '외부 승인 대기')`,
			workItemID, started, ended, mentions); err != nil {
			t.Fatalf("record an ending: %v", err)
		}
	}

	// Reported every week: five reports named it, and it stood five weeks. The
	// two ways of counting agree, and this row is here to prove the change does
	// not move the ordinary case.
	record(steady, "2026-07-06", "2026-08-10", 5)
	// Reported twice across the same span, because two weeks went unwritten.
	// The obstacle still stood five weeks.
	record(gapped, "2026-07-06", "2026-08-10", 2)

	clearance, err := server.app.issueClearanceFor(server.ctx(), "2026-07-01", "2026-08-31", "", nil)
	if err != nil {
		t.Fatalf("read the clearance: %v", err)
	}
	if clearance.Resolved != 2 {
		t.Fatalf("two endings were recorded, the report counts %d", clearance.Resolved)
	}
	if clearance.LongestWeeks != 5 {
		t.Errorf("the longest obstacle stood five weeks, the report says %d", clearance.LongestWeeks)
	}
	// Both stood five weeks, so the median is five however the two are ordered.
	if clearance.MedianWeeks != 5 {
		t.Errorf("both obstacles stood five weeks, the median says %d", clearance.MedianWeeks)
	}
}

// guards: issueClearanceFor
func TestNoRecordedEndingsIsNotAZeroWeekAnswer(t *testing.T) {
	server := newTestServer(t)
	// Nobody has answered the question yet. Zero endings must not read as
	// "obstacles clear in zero weeks", which is what a bare zero would say.
	clearance, err := server.app.issueClearanceFor(server.ctx(), "2026-07-01", "2026-08-31", "", nil)
	if err != nil {
		t.Fatalf("read the clearance: %v", err)
	}
	if clearance.Resolved != 0 {
		t.Fatalf("no endings were recorded, the report counts %d", clearance.Resolved)
	}
	if clearance.MedianWeeks != 0 || clearance.LongestWeeks != 0 {
		t.Errorf("with nothing recorded the figures should stay empty: %+v", clearance)
	}
}

var _ = http.MethodGet

// guards: issueClearanceFor
//
// The case above gives both obstacles the same span on purpose, so that it
// measures weeks rather than mentions. That also means the ordering is never
// exercised: the query asks for the longest first and the reader takes the
// first row, and with two equal spans either row gives the same answer. Take
// the last row instead and the screen names the *shortest* obstacle as the
// worst one.
func TestTheLongestObstacleIsTheOneReported(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("clearance_longest", "USER", nil)
	brief, drawnOut := twoWorkItemsOf(t, server, author)

	record := func(workItemID int64, started, ended string, mentions int) {
		t.Helper()
		if _, err := server.app.db.Exec(server.ctx(),
			`INSERT INTO work_item_issue_outcomes(work_item_id, started_week, ended_week, weeks, outcome, issue_text)
				VALUES($1, $2::date, $3::date, $4, 'RESOLVED', '외부 승인 대기')`,
			workItemID, started, ended, mentions); err != nil {
			t.Fatalf("record an ending: %v", err)
		}
	}
	record(brief, "2026-08-03", "2026-08-10", 2)    // 한 주
	record(drawnOut, "2026-07-06", "2026-08-10", 6) // 다섯 주

	var briefTitle, longTitle string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT title FROM work_items WHERE id=$1`, brief).Scan(&briefTitle); err != nil {
		t.Fatal(err)
	}
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT title FROM work_items WHERE id=$1`, drawnOut).Scan(&longTitle); err != nil {
		t.Fatal(err)
	}

	clearance, err := server.app.issueClearanceFor(server.ctx(), "2026-07-01", "2026-08-31", "", nil)
	if err != nil {
		t.Fatalf("read the clearance: %v", err)
	}
	if clearance.LongestWeeks != 5 {
		t.Errorf("the longest obstacle stood five weeks, the report says %d", clearance.LongestWeeks)
	}
	if clearance.LongestTitle == briefTitle {
		t.Errorf("the report names the briefest obstacle %q as the longest", clearance.LongestTitle)
	}
	if clearance.LongestTitle != longTitle {
		t.Errorf("the longest obstacle is %q, the report names %q", longTitle, clearance.LongestTitle)
	}
}
