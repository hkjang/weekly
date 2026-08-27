package app

import (
	"context"
	"encoding/json"
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

// The figure exists in two period reports — one person's own, and their
// organisation's — and the people clause that narrows it is written once and
// handed to both. It is written against the aliases decisionsInPeriod uses, so
// the clearance query has to use those same names or the personal clause names
// a table it does not have. That failure is caught and logged rather than
// losing the report, which is why nobody saw it: every personal period report
// answered "해소로 기록된 이슈가 아직 없습니다" while PostgreSQL was refusing
// the question. The old tests called issueClearanceFor with an empty clause —
// the one shape the product never passes — so this one goes through the route.
//
// guards: issueClearanceFor, periodRollup
func TestTheClearanceFigureSurvivesBothScopes(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("해소 조직", "CLEARORG")
	leader := server.createUser("clearance_leader", "TEAM_LEADER", &organisation)
	own, _ := twoWorkItemsOf(t, server, leader)
	if _, err := server.app.db.Exec(server.ctx(),
		`INSERT INTO work_item_issue_outcomes(work_item_id, started_week, ended_week, weeks, outcome, issue_text)
		 VALUES ($1, '2026-08-03'::date, '2026-08-24'::date, 4, 'RESOLVED', '외부 승인 대기')`,
		own); err != nil {
		t.Fatalf("record an ending: %v", err)
	}

	read := func(scope string) issueClearance {
		t.Helper()
		response := server.request(http.MethodGet,
			"/api/v1/rollups?kind=MONTH&period=2026-08&scope="+scope, nil, leader)
		if response.Code != http.StatusOK {
			t.Fatalf("read the %s rollup: %d %s", scope, response.Code, response.Body.String())
		}
		var body struct {
			Data struct {
				IssueClearance issueClearance `json:"issueClearance"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode the %s rollup: %v", scope, err)
		}
		return body.Data.IssueClearance
	}

	for _, scope := range []string{"SELF", "TEAM"} {
		clearance := read(scope)
		if clearance.Unread {
			t.Fatalf("%s: the clearance query did not run", scope)
		}
		if clearance.Resolved != 1 {
			t.Fatalf("%s: one ending was recorded, the report counts %d", scope, clearance.Resolved)
		}
		if clearance.LongestWeeks != 3 {
			t.Errorf("%s: the obstacle stood three weeks, the report says %d", scope, clearance.LongestWeeks)
		}
	}
}

// A failure and an empty table both leave the count at zero, and the screen
// turns that zero into a sentence about the data: "해소로 기록된 이슈가 아직
// 없습니다". Said on behalf of a query that never ran, it is a claim about
// rows nobody looked at.
//
// guards: periodRollup
func TestAnUnreadClearanceIsNotAnEmptyOne(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("clearance_unread", "USER", nil)

	// The one failure that reaches this path in the field is a query the
	// database refuses. Renaming the table it reads reproduces that exactly.
	if _, err := server.app.db.Exec(server.ctx(),
		`ALTER TABLE work_item_issue_outcomes RENAME TO work_item_issue_outcomes_hidden`); err != nil {
		t.Fatalf("hide the table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = server.app.db.Exec(context.Background(),
			`ALTER TABLE work_item_issue_outcomes_hidden RENAME TO work_item_issue_outcomes`)
	})

	response := server.request(http.MethodGet,
		"/api/v1/rollups?kind=MONTH&period=2026-08&scope=SELF", nil, author)
	if response.Code != http.StatusOK {
		t.Fatalf("one unreadable figure lost the whole report: %d", response.Code)
	}
	var body struct {
		Data struct {
			IssueClearance issueClearance `json:"issueClearance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the rollup: %v", err)
	}
	if !body.Data.IssueClearance.Unread {
		t.Error("the figure could not be read and the report does not say so")
	}
}
