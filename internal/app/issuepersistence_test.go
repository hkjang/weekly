package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// "이슈가 N주째 지속되고 있습니다" and the setting called persistent_issue_weeks
// both describe an issue that is open now. The number behind them counted every
// week in the whole history that carried one, consecutive or not, so a task that
// raised something in scattered weeks months ago and has reported clean ever
// since was filed in the risk register with a sentence claiming it was still
// open. On the seeded deployment that was 47 of 56 risk flags.

// weekWithIssue files one submitted report for the given week carrying a single
// task, whose issue box holds issue (empty means none).
func (s *testServer) weekWithIssue(cookie *http.Cookie, weekStart, title, issue string, progress int) {
	s.t.Helper()
	id, version := s.draft(cookie, weekStart, weekStart+" 주간보고")
	filled := s.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": weekStart + " 주간보고",
		"version": version,
		"items": []map[string]any{{
			"category": "개발", "title": title, "currentResult": "진행했습니다",
			"nextPlan": "이어서 합니다", "issue": issue, "progress": progress,
		}},
	}, cookie)
	if filled.Code != http.StatusOK {
		s.t.Fatalf("fill %s: %d %s", weekStart, filled.Code, filled.Body.String())
	}
	next := decodeData(s.t, filled)["version"].(float64)
	handed := s.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id),
		map[string]any{"version": int(next)}, cookie)
	if handed.Code != http.StatusOK {
		s.t.Fatalf("submit %s: %d %s", weekStart, handed.Code, handed.Body.String())
	}
}

func (s *testServer) onlyWorkItem(cookie *http.Cookie) map[string]any {
	s.t.Helper()
	w := s.request(http.MethodGet, "/api/v1/work-items?scope=SELF&limit=20", nil, cookie)
	if w.Code != http.StatusOK {
		s.t.Fatalf("list work items: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		s.t.Fatal(err)
	}
	if len(envelope.Data.Items) != 1 {
		s.t.Fatalf("expected exactly one task, got %d: %s", len(envelope.Data.Items), w.Body.String())
	}
	return envelope.Data.Items[0]
}

// guards: summarizeWorkItem
func TestAnIssueAnsweredWeeksAgoIsNotAnOpenOne(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("issue_history", "USER", nil)

	// Raised in two scattered weeks, then answered and clean ever since.
	server.weekWithIssue(owner, "2026-06-01", "큐 개선", "외부 연동 지연", 10)
	server.weekWithIssue(owner, "2026-06-08", "큐 개선", "", 20)
	server.weekWithIssue(owner, "2026-06-15", "큐 개선", "장비 반입 지연", 30)
	server.weekWithIssue(owner, "2026-06-22", "큐 개선", "", 40)
	server.weekWithIssue(owner, "2026-06-29", "큐 개선", "", 50)

	item := server.onlyWorkItem(owner)
	if history := int(item["issueWeeks"].(float64)); history != 2 {
		t.Errorf("the history should still record both weeks that carried an issue, got %d", history)
	}
	if open := int(item["issueRunWeeks"].(float64)); open != 0 {
		t.Errorf("no issue is open, yet the task reports %d week(s) of one", open)
	}
	if item["atRisk"].(bool) {
		t.Error("the task is in the risk register for an issue its last three reports say is gone")
	}
}

// guards: summarizeWorkItem
func TestAnOpenIssueIsMeasuredAcrossTheWeeksItSpannedNotTheReportsThatMentionedIt(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("issue_open", "USER", nil)

	// Two reports, six weeks apart, both carrying the same unanswered issue.
	// Nobody reported in between, which does not mean the issue went away.
	server.weekWithIssue(owner, "2026-05-04", "큐 개선", "외부 연동 지연", 30)
	server.weekWithIssue(owner, "2026-06-15", "큐 개선", "외부 연동 지연", 30)

	item := server.onlyWorkItem(owner)
	open := int(item["issueRunWeeks"].(float64))
	if open < 6 {
		t.Errorf("an issue open from 5월 4일 to 6월 15일 has been open for seven weeks, not %d", open)
	}
	if !item["atRisk"].(bool) {
		t.Error("an issue open across six weeks is exactly what the risk register is for")
	}
}
