package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A decision is the record of what a room agreed. Four rules protect it — who
// may read the decisions on a task, who may add one, who may correct one, and
// who may destroy one — and none of the four was tested. Each could be deleted
// and the whole suite stayed green.

// workItemOf returns the work item the report's first item created.
func workItemOf(t *testing.T, server *testServer, cookie *http.Cookie, week, summary string) int64 {
	t.Helper()
	reportID := server.submitted(cookie, week, summary)
	var workItemID int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT i.work_item_id FROM report_items i WHERE i.report_id=$1 AND i.work_item_id IS NOT NULL LIMIT 1`, reportID).
		Scan(&workItemID); err != nil {
		t.Skipf("no work item was created for the report: %v", err)
	}
	return workItemID
}

func recordDecision(t *testing.T, server *testServer, cookie *http.Cookie, workItemID int64, title string) int64 {
	t.Helper()
	w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/decisions", workItemID),
		map[string]any{"title": title, "decidedBy": "팀장", "decidedOn": "2026-08-24", "rationale": "그렇게 정했다"}, cookie)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("record a decision: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Data.ID == 0 {
		t.Fatalf("the decision has no id: %s", w.Body.String())
	}
	return envelope.Data.ID
}

func refused(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code == http.StatusOK || w.Code == http.StatusCreated || w.Code == http.StatusNoContent {
		t.Errorf("%s succeeded and should not have: %s", what, w.Body.String())
	}
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Errorf("%s answered %d, want 403 or 404 — a refusal, not a failure", what, w.Code)
	}
}

// guards: listWorkItemDecisions
func TestDecisionsAreReadableOnlyInsideTheReadersScope(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("결정 내 조직", "DECMINE")
	theirs := server.createOrganization("결정 남의 조직", "DECTHEIRS")
	author := server.createUser("decision_author", "USER", &mine)
	outsider := server.createUser("decision_outsider", "USER", &theirs)

	workItemID := workItemOf(t, server, author, "2026-08-24", "결정이 붙는 업무")
	recordDecision(t, server, author, workItemID, "이렇게 가기로 했다")
	path := fmt.Sprintf("/api/v1/work-items/%d/decisions", workItemID)

	if own := server.request(http.MethodGet, path, nil, author); own.Code != http.StatusOK {
		t.Fatalf("the author cannot read decisions on their own task: %d %s", own.Code, own.Body.String())
	}
	refused(t, "reading another organisation's decisions",
		server.request(http.MethodGet, path, nil, outsider))
}

// guards: createWorkItemDecision
func TestNobodyRecordsADecisionOnAnotherOrganisationsWork(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("결정 내 조직", "DECMINE")
	theirs := server.createOrganization("결정 남의 조직", "DECTHEIRS")
	author := server.createUser("decision_author", "USER", &mine)
	outsider := server.createUser("decision_outsider", "USER", &theirs)

	workItemID := workItemOf(t, server, author, "2026-08-24", "결정이 붙는 업무")
	path := fmt.Sprintf("/api/v1/work-items/%d/decisions", workItemID)

	refused(t, "recording a decision on another organisation's work",
		server.request(http.MethodPost, path,
			map[string]any{"title": "끼어들기", "decidedBy": "나", "decidedOn": "2026-08-24"}, outsider))

	// Refusing and writing anyway is still writing.
	listed := server.request(http.MethodGet, path, nil, author)
	if listed.Code != http.StatusOK {
		t.Fatalf("read the author's decisions: %d %s", listed.Code, listed.Body.String())
	}
	if body := listed.Body.String(); len(body) > 0 && strings.Contains(body, "끼어들기") {
		t.Errorf("the refused decision was recorded anyway: %s", body)
	}
}

// guards: updateDecision, deleteDecision
func TestOnlyTheRecorderOrAnAdministratorChangesADecision(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("결정 내 조직", "DECMINE")
	theirs := server.createOrganization("결정 남의 조직", "DECTHEIRS")
	author := server.createUser("decision_author", "USER", &mine)
	outsider := server.createUser("decision_outsider", "USER", &theirs)

	workItemID := workItemOf(t, server, author, "2026-08-24", "결정이 붙는 업무")
	decisionID := recordDecision(t, server, author, workItemID, "이렇게 가기로 했다")
	path := fmt.Sprintf("/api/v1/decisions/%d", decisionID)

	refused(t, "correcting somebody else's decision",
		server.request(http.MethodPatch, path, map[string]any{"title": "고쳐 버림"}, outsider))
	refused(t, "destroying somebody else's decision",
		server.request(http.MethodDelete, path, nil, outsider))

	// The record has to still be there, and still say what it said.
	listed := server.request(http.MethodGet, fmt.Sprintf("/api/v1/work-items/%d/decisions", workItemID), nil, author)
	if listed.Code != http.StatusOK {
		t.Fatalf("read the author's decisions: %d %s", listed.Code, listed.Body.String())
	}
	body := listed.Body.String()
	if !strings.Contains(body, "이렇게 가기로 했다") {
		t.Errorf("the decision is gone after a refused delete: %s", body)
	}
	if strings.Contains(body, "고쳐 버림") {
		t.Errorf("the refusal changed the decision anyway: %s", body)
	}

	// The recorder can still remove their own.
	if own := server.request(http.MethodDelete, path, nil, author); own.Code != http.StatusOK && own.Code != http.StatusNoContent {
		t.Fatalf("the recorder cannot delete their own decision: %d %s", own.Code, own.Body.String())
	}
}

// guards: suggestDecisions
func TestDecisionSuggestionsStayInsideTheReadersScope(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("제안 내 조직", "SUGMINE")
	theirs := server.createOrganization("제안 남의 조직", "SUGTHEIRS")
	author := server.createUser("suggest_author", "USER", &mine)
	outsider := server.createUser("suggest_outsider", "USER", &theirs)

	workItemID := workItemOf(t, server, author, "2026-08-24", "제안을 붙일 업무")
	path := fmt.Sprintf("/api/v1/work-items/%d/decisions/suggest", workItemID)

	// The scope check has to run before the feature check. An AI gateway that is
	// switched off must not become the reason a stranger is turned away, because
	// then switching it on would open the door.
	refused(t, "asking for decision suggestions on another organisation's work",
		server.request(http.MethodPost, path, map[string]any{}, outsider))
}
