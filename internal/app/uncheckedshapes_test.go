package app

import (
	"fmt"
	"net/http"
	"testing"
)

// Two rules the sweep never examined: its pattern is a refusal followed by a
// bare return, and neither of these is shaped that way — one sits in an else,
// the other returns three values from a helper. A tool that only sees one shape
// is quiet about the rest, which is not the same as the rest being fine.

// guards: cloneReport
func TestOnlyTheAuthorClonesAReport(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("clone_author", "USER", nil)
	stranger := server.createUser("clone_stranger", "USER", nil)
	reportID := server.submitted(author, "2026-08-24", "복제할 보고서")

	body := map[string]any{"targetWeekStart": "2026-09-07", "mode": "STRUCTURE"}
	refused(t, "cloning somebody else's report",
		server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/clone", reportID), body, stranger))

	// A refused clone must not leave a copy behind, and certainly not one owned
	// by the person who was refused.
	var copies int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM weekly_reports WHERE week_start = DATE '2026-09-07'`).Scan(&copies); err != nil {
		t.Fatalf("count reports: %v", err)
	}
	if copies != 0 {
		t.Errorf("the refused clone created %d report(s)", copies)
	}

	// The author can still clone their own.
	if own := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/clone", reportID), body, author); own.Code != http.StatusOK && own.Code != http.StatusCreated {
		t.Fatalf("the author cannot clone their own report: %d %s", own.Code, own.Body.String())
	}
}

// guards: periodRollup
func TestOrganisationWidePeriodRollupIsForLeaders(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("집계 조직", "ROLLORG")
	leader := server.createUser("rollup_leader", "TEAM_LEADER", &organisation)
	writer := server.createUser("rollup_writer", "USER", &organisation)

	if own := server.request(http.MethodGet, "/api/v1/rollups?kind=MONTH&period=2026-08&scope=SELF", nil, writer); own.Code != http.StatusOK {
		t.Fatalf("a writer cannot read their own rollup: %d %s", own.Code, own.Body.String())
	}
	refused(t, "a writer reading the organisation's rollup",
		server.request(http.MethodGet, "/api/v1/rollups?kind=MONTH&period=2026-08&scope=TEAM", nil, writer))
	if asLeader := server.request(http.MethodGet, "/api/v1/rollups?kind=MONTH&period=2026-08&scope=TEAM", nil, leader); asLeader.Code != http.StatusOK {
		t.Fatalf("a leader cannot read the organisation's rollup: %d %s", asLeader.Code, asLeader.Body.String())
	}

	// The CSV and the deck read the same period through the same gate, so a
	// writer must not reach round them either.
	for _, path := range []string{
		"/api/v1/rollups/export.csv?kind=MONTH&period=2026-08&scope=TEAM",
		"/api/v1/rollups/export.pptx?kind=MONTH&period=2026-08&scope=TEAM",
	} {
		refused(t, "a writer exporting the organisation's rollup ("+path+")",
			server.request(http.MethodGet, path, nil, writer))
	}
}
