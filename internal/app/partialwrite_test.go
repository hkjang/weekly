package app

import (
	"fmt"
	"net/http"
	"strings"
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

// guards: updateConfluenceCandidate
//
// The draft feeds a report, so the same length limits the report enforces have
// to hold here. Nothing checked them: the handler's whole validation line could
// be inverted and every test still passed.
func TestAnAutomaticDraftKeepsTheLimitsAReportWouldEnforce(t *testing.T) {
	server := newTestServer(t)
	server.createUser("draft_limits", "USER", nil)
	owner := server.signIn(server.lastCreatedUsername("draft_limits"), server.passwordFor("draft_limits"))
	ownerID := server.userIDOf(server.lastCreatedUsername("draft_limits"))

	var id int64
	if err := server.app.db.QueryRow(server.ctx(),
		`INSERT INTO report_candidates(user_id, week_start, normalized_title, category, current_result, next_plan, issue)
			VALUES($1,'2026-08-24','큐 개선','개발','지난주 결과','다음 주 계획','') RETURNING id`, ownerID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/report-candidates/%d", id)

	for _, refusal := range []struct {
		name string
		body map[string]any
	}{
		{"제목 없음", map[string]any{"normalizedTitle": "   "}},
		{"제목이 240자를 넘음", map[string]any{"normalizedTitle": strings.Repeat("가", 241)}},
		{"구분이 80자를 넘음", map[string]any{"normalizedTitle": "큐 개선", "category": strings.Repeat("나", 81)}},
		{"본문이 60000자를 넘음", map[string]any{"normalizedTitle": "큐 개선", "currentResult": strings.Repeat("다", 60001)}},
	} {
		w := server.request(http.MethodPatch, path, refusal.body, owner)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: 받아들여졌습니다 %d %s", refusal.name, w.Code, w.Body.String())
		}
	}

	// A refused edit must not have written anything.
	var title, category, current string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT normalized_title, category, current_result FROM report_candidates WHERE id=$1`, id).
		Scan(&title, &category, &current); err != nil {
		t.Fatal(err)
	}
	if title != "큐 개선" || category != "개발" || current != "지난주 결과" {
		t.Errorf("거부된 수정이 초안을 바꿨습니다: %q / %q / %q", title, category, current)
	}
}
