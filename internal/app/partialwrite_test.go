package app

import (
	"fmt"
	"net/http"
	"testing"
)

// PATCH says the body carries the fields to change. Both handlers below take a
// plain struct, so every column is rewritten and the ones the caller left out
// arrive as the empty string. A correction that means to move a decision to DONE
// therefore erases why it was taken and when it is due — and the due date is not
// only prose, because a task with no deadline of its own is offered the one a
// meeting agreed here.

// guards: updateDecision
func TestCorrectingOneFieldOfADecisionKeepsTheRest(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("결정 조직", "PARTDEC")
	author := server.createUser("partial_author", "USER", &organisation)

	workItemID := workItemOf(t, server, author, "2026-08-24", "결정이 붙는 업무")
	created := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/decisions", workItemID),
		map[string]any{"title": "이렇게 가기로 했다", "decidedBy": "회의", "decidedOn": "2026-08-24",
			"rationale": "대안보다 위험이 낮아서", "followUp": "다음 주까지 설계 확정", "dueDate": "2026-09-07"}, author)
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("record a decision: %d %s", created.Code, created.Body.String())
	}
	id := int64(decodeData(t, created)["id"].(float64))

	// Only the required fields plus the one being changed, which is what a
	// caller reading PATCH would send.
	patch := map[string]any{"title": "이렇게 가기로 했다", "decidedBy": "회의",
		"decidedOn": "2026-08-24", "status": "DONE"}
	if w := server.request(http.MethodPatch, fmt.Sprintf("/api/v1/decisions/%d", id), patch, author); w.Code != http.StatusOK {
		t.Fatalf("patch the decision: %d %s", w.Code, w.Body.String())
	}

	var rationale, followUp string
	var due *string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT rationale, follow_up, due_date::text FROM decisions WHERE id=$1`, id).
		Scan(&rationale, &followUp, &due); err != nil {
		t.Fatal(err)
	}
	if rationale == "" {
		t.Error("moving the decision to DONE erased why it was taken")
	}
	if followUp == "" {
		t.Error("moving the decision to DONE erased its follow-up")
	}
	if due == nil {
		t.Error("moving the decision to DONE erased the deadline the meeting agreed")
	}
}

// guards: updateConfluenceCandidate
func TestEditingOneLineOfAnAutomaticDraftKeepsTheRest(t *testing.T) {
	server := newTestServer(t)
	server.createUser("draft_owner", "USER", nil)
	owner := server.signIn(server.lastCreatedUsername("draft_owner"), server.passwordFor("draft_owner"))
	ownerID := server.userIDOf(server.lastCreatedUsername("draft_owner"))

	var id int64
	if err := server.app.db.QueryRow(server.ctx(),
		`INSERT INTO report_candidates(user_id, week_start, normalized_title, category, current_result, next_plan, issue)
			VALUES($1,'2026-08-24','큐 개선','개발','지난주에 큐를 나눴습니다','다음 주에 부하를 겁니다','외부 연동 지연')
			RETURNING id`, ownerID).Scan(&id); err != nil {
		t.Fatal(err)
	}

	patch := map[string]any{"normalizedTitle": "큐 개선", "issue": ""}
	if w := server.request(http.MethodPatch, fmt.Sprintf("/api/v1/report-candidates/%d", id), patch, owner); w.Code != http.StatusOK {
		t.Fatalf("patch the draft: %d %s", w.Code, w.Body.String())
	}

	var category, current, plan, issue string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT category, current_result, next_plan, issue FROM report_candidates WHERE id=$1`, id).
		Scan(&category, &current, &plan, &issue); err != nil {
		t.Fatal(err)
	}
	if category == "" || current == "" || plan == "" {
		t.Errorf("clearing the issue emptied the rest of the draft: 분류=%q 지난주=%q 계획=%q", category, current, plan)
	}
	// The one field the request did name is still cleared.
	if issue != "" {
		t.Errorf("the issue the request cleared is still there: %q", issue)
	}
}
