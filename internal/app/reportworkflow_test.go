package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// enableWorkflow turns the review workflow on for this server.
func (s *testServer) enableWorkflow() {
	s.t.Helper()
	w := s.request(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"settings": map[string]string{"workflow.enabled": "true"}}, s.admin)
	if w.Code != http.StatusOK {
		s.t.Fatalf("enable the workflow: %d %s", w.Code, w.Body.String())
	}
}

// draft creates a report for the given session and returns its id and version.
func (s *testServer) draft(cookie *http.Cookie, weekStart, summary string) (int64, int) {
	s.t.Helper()
	w := s.request(http.MethodPost, "/api/v1/reports",
		map[string]any{"weekStart": weekStart, "summary": summary}, cookie)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		s.t.Fatalf("create a report: %d %s", w.Code, w.Body.String())
	}
	data := decodeData(s.t, w)
	id, _ := data["id"].(float64)
	version, _ := data["version"].(float64)
	if version == 0 {
		version = 1
	}
	return int64(id), int(version)
}

// submitted creates a report with one work item and hands it in, because an
// empty report cannot be submitted — which is itself a rule worth having.
func (s *testServer) submitted(cookie *http.Cookie, weekStart, summary string) int64 {
	s.t.Helper()
	id, version := s.draft(cookie, weekStart, summary)
	filled := s.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": summary,
		"version": version,
		"items": []map[string]any{{
			"category": "인프라", "title": "회선 이설", "currentResult": "완료",
			"nextPlan": "점검", "issue": "", "progress": 100,
		}},
	}, cookie)
	if filled.Code != http.StatusOK {
		s.t.Fatalf("add an item: %d %s", filled.Code, filled.Body.String())
	}
	nextVersion, _ := decodeData(s.t, filled)["version"].(float64)
	handed := s.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id),
		map[string]any{"version": int(nextVersion)}, cookie)
	if handed.Code != http.StatusOK {
		s.t.Fatalf("submit: %d %s", handed.Code, handed.Body.String())
	}
	return id
}

// handIn fills an existing draft and submits it at whatever version it is on.
func (s *testServer) handIn(cookie *http.Cookie, id int64) {
	s.t.Helper()
	var version int
	if err := s.app.db.QueryRow(s.ctx(), `SELECT version FROM weekly_reports WHERE id=$1`, id).Scan(&version); err != nil {
		s.t.Fatal(err)
	}
	filled := s.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "제출용", "version": version,
		"items": []map[string]any{{"category": "인프라", "title": "회선 이설", "currentResult": "완료", "progress": 100}},
	}, cookie)
	if filled.Code != http.StatusOK {
		s.t.Fatalf("fill: %d %s", filled.Code, filled.Body.String())
	}
	next, _ := decodeData(s.t, filled)["version"].(float64)
	handed := s.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id), map[string]any{"version": int(next)}, cookie)
	if handed.Code != http.StatusOK {
		s.t.Fatalf("submit: %d %s", handed.Code, handed.Body.String())
	}
}

func (s *testServer) reportStatus(cookie *http.Cookie, id int64) string {
	s.t.Helper()
	w := s.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", id), nil, cookie)
	if w.Code != http.StatusOK {
		s.t.Fatalf("read report %d: %d %s", id, w.Code, w.Body.String())
	}
	status, _ := decodeData(s.t, w)["status"].(string)
	return status
}

// guards: reviewReport, canReviewReport
//
// canReviewReport is not asked for full coverage: the route already refuses a
// USER through requireRole, so the same check inside the function is defence in
// depth that no request can reach. Demanding 100%% here would mean writing a
// test that calls it directly, which proves the belt and not the trousers.
//
// A review nobody else performs is not a review. Both rules below were correct
// when read and had never been exercised through the route that enforces them.
func TestNobodyReviewsTheirOwnReport(t *testing.T) {
	server := newTestServer(t)
	server.enableWorkflow()

	// The strongest form of the question: an administrator, who may review
	// anybody, filing a report of their own.
	id := server.submitted(server.admin, "2026-08-24", "관리자 본인 보고서")
	self := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", id), map[string]any{}, server.admin)
	if self.Code == http.StatusOK {
		t.Fatalf("an administrator approved their own report: %s", self.Body.String())
	}
	if self.Code != http.StatusForbidden {
		t.Errorf("self-approval answered %d, want 403", self.Code)
	}

	// And a plain user cannot review at all, not even somebody else's.
	author := server.createUser("wf_author", "USER", nil)
	reviewer := server.createUser("wf_reader", "USER", nil)
	otherID := server.submitted(author, "2026-08-24", "남의 보고서")
	byUser := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", otherID), map[string]any{}, reviewer)
	if byUser.Code == http.StatusOK {
		t.Errorf("a USER approved a report: %s", byUser.Body.String())
	}
}

// guards: canReviewReport
//
// The normal path for this product: a team leader reviewing somebody in their
// own organisation, and refusing somebody outside it.
func TestATeamLeaderReviewsTheirOwnOrganisationAndNoOther(t *testing.T) {
	server := newTestServer(t)
	server.enableWorkflow()

	inside := server.createOrganization("팀 안", "TEAMIN")
	outside := server.createOrganization("팀 밖", "TEAMOUT")
	leader := server.createUser("wf_leader", "TEAM_LEADER", &inside)
	member := server.createUser("wf_member", "USER", &inside)
	outsider := server.createUser("wf_outsider", "USER", &outside)

	own := server.submitted(member, "2026-08-24", "팀원 보고서")
	approved := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", own), map[string]any{}, leader)
	if approved.Code != http.StatusOK {
		t.Fatalf("a leader could not approve their own team member: %d %s", approved.Code, approved.Body.String())
	}

	foreign := server.submitted(outsider, "2026-08-24", "다른 조직 보고서")
	refused := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", foreign), map[string]any{}, leader)
	if refused.Code == http.StatusOK {
		t.Errorf("a leader approved a report from another organisation: %s", refused.Body.String())
	}
	if refused.Code != http.StatusForbidden {
		t.Errorf("reviewing outside the organisation answered %d, want 403", refused.Code)
	}
}

// guards: reviewReport
func TestAReportCanOnlyBeReviewedFromSubmitted(t *testing.T) {
	server := newTestServer(t)
	server.enableWorkflow()
	author := server.createUser("wf_states", "USER", nil)

	id, _ := server.draft(author, "2026-08-24", "초안")
	// A draft has not been handed in; approving it would approve nothing.
	early := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", id), map[string]any{}, server.admin)
	if early.Code != http.StatusConflict {
		t.Errorf("approving a draft answered %d, want 409 — %s", early.Code, early.Body.String())
	}

	server.handIn(author, id)
	if first := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", id), map[string]any{}, server.admin); first.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", first.Code, first.Body.String())
	}
	// Approving again is not idempotent housekeeping — it would stamp a second
	// reviewer over the first.
	second := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", id), map[string]any{}, server.admin)
	if second.Code != http.StatusConflict {
		t.Errorf("approving twice answered %d, want 409 — %s", second.Code, second.Body.String())
	}
	if code := errorCode(second); code != "INVALID_STATUS" {
		t.Errorf("the refusal is coded %q, not INVALID_STATUS", code)
	}
}

// guards: reviewReport
func TestRejectionMustSayWhy(t *testing.T) {
	server := newTestServer(t)
	server.enableWorkflow()
	author := server.createUser("wf_reject", "USER", nil)
	id := server.submitted(author, "2026-08-24", "반려될 보고서")

	silent := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/reject", id), map[string]any{}, server.admin)
	if silent.Code != http.StatusBadRequest {
		t.Errorf("a rejection with no reason answered %d, want 400 — %s", silent.Code, silent.Body.String())
	}
	// The report must not have moved on a refused rejection.
	if status := server.reportStatus(author, id); status != "SUBMITTED" {
		t.Errorf("the report is %q after a refused rejection", status)
	}
	spoken := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/reject", id),
		map[string]any{"comment": "지난주 실적과 이번 주 계획이 같습니다."}, server.admin)
	if spoken.Code != http.StatusOK {
		t.Fatalf("reject with a reason: %d %s", spoken.Code, spoken.Body.String())
	}
}

// guards: updateReport
//
// An approval that survives a rewrite is a signature on a document somebody
// changed afterwards.
func TestEditingAnApprovedReportTakesTheApprovalBackAndSaysSo(t *testing.T) {
	server := newTestServer(t)
	server.enableWorkflow()
	author := server.createUser("wf_edit", "USER", nil)

	id := server.submitted(author, "2026-08-24", "승인받을 내용")
	if approved := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", id), map[string]any{}, server.admin); approved.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}
	if status := server.reportStatus(author, id); status != "APPROVED" {
		t.Fatalf("the report is %q, not APPROVED", status)
	}

	var stored int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT version FROM weekly_reports WHERE id=$1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	edited := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id),
		map[string]any{"summary": "승인 뒤에 바꾼 내용", "version": stored, "items": []any{}}, author)
	if edited.Code != http.StatusOK {
		t.Fatalf("edit after approval: %d %s", edited.Code, edited.Body.String())
	}
	if status := server.reportStatus(author, id); status != "DRAFT" {
		t.Errorf("the report is %q after being rewritten; the approval outlived the content", status)
	}

	// The approver signed something that no longer exists, so the reversal has
	// to be on the record rather than only in the current status.
	var reason string
	err := server.app.db.QueryRow(server.ctx(),
		`SELECT comment FROM report_status_history WHERE report_id=$1 AND from_status='APPROVED' AND to_status='DRAFT' ORDER BY id DESC LIMIT 1`, id).Scan(&reason)
	if err != nil {
		t.Fatalf("the approval was undone with no history row: %v", err)
	}
	if reason == "" {
		t.Error("the history row does not say why the approval was undone")
	}

	// Reviewed-by must be cleared, or the screen still credits an approver.
	var reviewer *int64
	if err := server.app.db.QueryRow(server.ctx(), `SELECT reviewed_by FROM weekly_reports WHERE id=$1`, id).Scan(&reviewer); err != nil {
		t.Fatal(err)
	}
	if reviewer != nil {
		t.Errorf("the report still credits reviewer %d after being rewritten", *reviewer)
	}
}

// reviewed_by has been written on every approve and reject since the workflow
// existed, cleared on every resubmit, and read by nothing. Meanwhile the editor
// told a writer whose reason had gone missing to "검토자에게 직접 확인해
// 주세요" without naming them, and an approved report showed a timestamp with
// nobody's name on it — which is the one fact an approval is for.
//
// guards: loadReport, approveReport, rejectReport
func TestAReviewedReportSaysWhoReviewedIt(t *testing.T) {
	server := newTestServer(t)
	server.enableWorkflow()
	organisation := server.createOrganization("검토자 조직", "REVWHO")
	leader := server.createUser("review_who_leader", "TEAM_LEADER", &organisation)
	author := server.createUser("review_who_author", "USER", &organisation)
	reportID, version := server.draft(author, "2026-08-24", "검토자 시험")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID), map[string]any{
		"summary": "검토자 시험", "version": version,
		"items": []map[string]any{{"category": "개발", "title": "업무", "currentResult": "진행",
			"nextPlan": "계속", "issue": "", "progress": 10}},
	}, author)
	if filled.Code != http.StatusOK {
		t.Fatalf("write the report: %d %s", filled.Code, filled.Body.String())
	}

	reviewerOf := func() (string, string) {
		t.Helper()
		response := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", reportID), nil, author)
		if response.Code != http.StatusOK {
			t.Fatalf("read the report: %d %s", response.Code, response.Body.String())
		}
		var body struct {
			Data struct {
				Status     string `json:"status"`
				ReviewedBy string `json:"reviewedBy"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode the report: %v", err)
		}
		return body.Data.Status, body.Data.ReviewedBy
	}

	submit := func() {
		t.Helper()
		_, current := reviewerOf()
		_ = current
		var loaded struct {
			Data struct {
				Version int `json:"version"`
			} `json:"data"`
		}
		read := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", reportID), nil, author)
		if err := json.Unmarshal(read.Body.Bytes(), &loaded); err != nil {
			t.Fatalf("read the version: %v", err)
		}
		sent := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", reportID),
			map[string]any{"version": loaded.Data.Version}, author)
		if sent.Code != http.StatusOK {
			t.Fatalf("submit: %d %s", sent.Code, sent.Body.String())
		}
	}

	submit()
	rejected := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/reject", reportID),
		map[string]any{"comment": "근거를 붙여 주세요"}, leader)
	if rejected.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rejected.Code, rejected.Body.String())
	}
	status, reviewer := reviewerOf()
	if status != "REVISION_REQUESTED" {
		t.Fatalf("status after a rejection: %s", status)
	}
	if reviewer == "" {
		t.Error("the writer is told to ask the reviewer and is not told who that is")
	}

	// Resubmitting clears the review, and the name has to go with it — leaving
	// it would credit somebody with a decision they have not made yet.
	submit()
	if status, reviewer = reviewerOf(); reviewer != "" {
		t.Errorf("a resubmitted report still names %q as its reviewer", reviewer)
	}

	approved := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", reportID),
		map[string]any{"comment": "확인했습니다"}, leader)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}
	if status, reviewer = reviewerOf(); reviewer == "" {
		t.Errorf("an approved report (%s) records no approver", status)
	}
}
