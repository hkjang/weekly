package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// README now states this plainly, and a writer is told it on the page they are
// writing on: a draft is not private. canViewReport has no status condition, so
// a leader above them and any administrator can open it before it is handed in.
//
// Whether that is the right policy is a deployment's call. What is not
// negotiable is that the sentence and the code agree — a product that tells a
// writer their draft is visible and then hides it is as wrong as the reverse.

// guards: canViewReport
func TestASubmissionIsNotWhenAReportBecomesVisible(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("초안 조직", "DRAFTVIS")
	leader := server.createUser("draft_lead", "TEAM_LEADER", &organisation)
	writer := server.createUser("draft_writer", "USER", &organisation)
	outsider := server.createUser("draft_outsider", "USER", &organisation)

	id, version := server.draft(writer, "2026-08-24", "아직 제출하지 않은 주")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "아직 제출하지 않은 주", "version": version,
		"items": []map[string]any{{"category": "개발", "title": "초안에만 있는 업무",
			"currentResult": "쓰는 중입니다", "nextPlan": "이어서", "issue": "", "progress": 20}},
	}, writer)
	if filled.Code != http.StatusOK {
		t.Fatalf("fill: %d %s", filled.Code, filled.Body.String())
	}

	// Still a draft — this test is about the state before submission.
	current := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", id), nil, writer)
	if status, _ := decodeData(t, current)["status"].(string); status != "DRAFT" {
		t.Fatalf("the report is %s, so this proves nothing about drafts", status)
	}

	// The leader can read it now, contents and all. Saying so on the editor
	// while the API refused would be its own kind of wrong.
	seen := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", id), nil, leader)
	if seen.Code != http.StatusOK {
		t.Fatalf("the leader cannot read the draft the writer was told they can: %d %s", seen.Code, seen.Body.String())
	}
	if !strings.Contains(seen.Body.String(), "초안에만 있는 업무") {
		t.Errorf("the leader got the report without its contents: %s", seen.Body.String())
	}

	// And a colleague still cannot, which is the line that submission does not
	// move either.
	if denied := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", id), nil, outsider); denied.Code != http.StatusForbidden {
		t.Errorf("a colleague read somebody else's draft: %d", denied.Code)
	}
}
