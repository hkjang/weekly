package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// organizations.parent_id can hold a cycle and nothing stops it. The API cannot
// make one — it only inserts an organisation whose parent already exists — but
// it also offers no way to rename, re-parent or remove an organisation at all,
// so an operator restructuring a chart edits the table by hand. That is exactly
// where a loop gets made.
//
// Measured on a deployment with 전사 → 본부 → 팀 → 전사: every team-scoped read
// for everybody inside the cycle hung until the browser gave up, and PostgreSQL
// grew an intermediate result nobody would ever read. No error reached anybody;
// the screen simply never loaded, for some people and not others.
//
// guards: orgSubtree, periodRollup
func TestALoopInTheOrganisationChartDoesNotHangTheReadsUnderIt(t *testing.T) {
	server := newTestServer(t)
	top := server.createOrganization("고리 전사", "CYCTOP")
	middle := server.createChildOrganization("고리 본부", "CYCMID", &top)
	bottom := server.createChildOrganization("고리 팀", "CYCBOT", &middle)
	manager := server.createUser("cycle_manager", "ORG_MANAGER", &top)

	reportID, version := server.draft(manager, "2026-08-24", "고리 시험")
	written := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID), map[string]any{
		"summary": "고리 시험", "version": version,
		"items": []map[string]any{{"category": "개발", "title": "고리 업무", "currentResult": "진행",
			"nextPlan": "계속", "issue": "", "progress": 30}},
	}, manager)
	if written.Code != http.StatusOK {
		t.Fatalf("write the report: %d %s", written.Code, written.Body.String())
	}

	// What an operator does with no way to re-parent from the product.
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE organizations SET parent_id=$1 WHERE id=$2`, bottom, top); err != nil {
		t.Fatalf("close the loop: %v", err)
	}
	t.Cleanup(func() {
		_, _ = server.app.db.Exec(context.Background(),
			`UPDATE organizations SET parent_id=NULL WHERE id=$1`, top)
	})

	// Every read that resolves a scope walks that tree. None of them may hang.
	for _, path := range []string{
		"/api/v1/rollups?kind=MONTH&period=2026-08&scope=TEAM",
		"/api/v1/team/reports",
		"/api/v1/analytics/overview?scope=TEAM",
		"/api/v1/search?q=고리",
	} {
		done := make(chan int, 1)
		go func() { done <- server.request(http.MethodGet, path, nil, manager).Code }()
		select {
		case code := <-done:
			if code >= 500 {
				t.Errorf("%s answered %d with a loop in the chart", path, code)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s never came back — the walk down the chart has no bound", path)
		}
	}

	// And the answer is still the subtree, not an empty set: the report written
	// by somebody inside the loop is still theirs to read.
	response := server.request(http.MethodGet,
		"/api/v1/rollups?kind=MONTH&period=2026-08&scope=TEAM", nil, manager)
	var body struct {
		Data struct {
			Items []struct {
				Title string `json:"title"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the rollup: %v", err)
	}
	found := false
	for _, item := range body.Data.Items {
		if item.Title == "고리 업무" {
			found = true
		}
	}
	if !found {
		t.Error("bounding the walk lost the work that was inside the scope")
	}
}
