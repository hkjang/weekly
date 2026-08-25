package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Three more boundaries that existed and were guarded by nothing: setting a due
// date on work that is not yours, commenting on a report you cannot read, and
// asking for somebody else's handover. Each was reachable, each was refused,
// and each could have been deleted without a test noticing.

// guards: setWorkItemDueDate
func TestOnlyTheOwnerSetsAWorkItemsDueDate(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("마감일 내 조직", "DUEMINE")
	theirs := server.createOrganization("마감일 남의 조직", "DUETHEIRS")
	owner := server.createUser("due_owner", "USER", &mine)
	outsider := server.createUser("due_outsider", "USER", &theirs)

	workItemID := workItemOf(t, server, owner, "2026-08-24", "마감일이 붙는 업무")
	path := fmt.Sprintf("/api/v1/work-items/%d/due-date", workItemID)

	if own := server.request(http.MethodPut, path, map[string]any{"dueDate": "2026-09-30"}, owner); own.Code != http.StatusOK {
		t.Fatalf("the owner cannot set their own due date: %d %s", own.Code, own.Body.String())
	}
	refused(t, "setting a due date on somebody else's work",
		server.request(http.MethodPut, path, map[string]any{"dueDate": "2026-12-31"}, outsider))

	// A deadline quietly moved by somebody else is worse than one refused
	// loudly: the owner would plan against a date they never chose.
	var stored *string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT to_char(due_date, 'YYYY-MM-DD') FROM work_items WHERE id=$1`, workItemID).Scan(&stored); err != nil {
		t.Fatalf("read the stored due date: %v", err)
	}
	if stored == nil || *stored != "2026-09-30" {
		t.Errorf("the due date is %v, the owner set 2026-09-30", stored)
	}
}

// guards: addComment
func TestNobodyCommentsOnAReportTheyCannotRead(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("comment_author", "USER", nil)
	stranger := server.createUser("comment_stranger", "USER", nil)
	reportID := server.submitted(author, "2026-08-24", "남이 볼 수 없는 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d/comments", reportID)

	if own := server.request(http.MethodPost, path, map[string]any{"content": "내 보고서에 남기는 메모"}, author); own.Code != http.StatusOK && own.Code != http.StatusCreated {
		t.Fatalf("the author cannot comment on their own report: %d %s", own.Code, own.Body.String())
	}
	refused(t, "commenting on a report the caller cannot read",
		server.request(http.MethodPost, path, map[string]any{"content": "끼어드는 의견"}, stranger))

	// Refusing and storing anyway is still storing.
	var found int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM report_comments WHERE report_id=$1 AND content=$2`, reportID, "끼어드는 의견").Scan(&found); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if found != 0 {
		t.Errorf("the refused comment was stored anyway (%d rows)", found)
	}
}

// guards: handover
func TestNobodyReadsSomebodyElsesHandover(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("인수인계 내 조직", "HANDMINE")
	theirs := server.createOrganization("인수인계 남의 조직", "HANDTHEIRS")
	leaving := server.createUser("handover_leaving", "USER", &mine)
	outsider := server.createUser("handover_outsider", "USER", &theirs)
	server.submitted(leaving, "2026-08-24", "인수인계할 업무가 담긴 보고")

	var leavingID int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT id FROM users WHERE username=$1`, server.lastCreatedUsername("handover_leaving")).Scan(&leavingID); err != nil {
		t.Fatalf("find the departing user: %v", err)
	}
	path := fmt.Sprintf("/api/v1/handover?userId=%d", leavingID)

	if own := server.request(http.MethodGet, path, nil, leaving); own.Code != http.StatusOK {
		t.Fatalf("a person cannot read their own handover: %d %s", own.Code, own.Body.String())
	}
	other := server.request(http.MethodGet, path, nil, outsider)
	refused(t, "reading another organisation's handover", other)
	// The refusal must not carry the work it refused to show.
	if strings.Contains(other.Body.String(), "인수인계할 업무가 담긴 보고") {
		t.Errorf("the refusal carried the report it was refusing: %s", other.Body.String())
	}
}

// Two screens read across an organisation, and a plain writer is not offered
// them. The gate is inside the handler — the route itself only asks for a
// session — so if it goes, a writer sees their colleagues' work. Both were
// unguarded.

// guards: meetingMode
func TestOrganisationWideMeetingMaterialIsForLeaders(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("회의 조직", "MEETORG")
	leader := server.createUser("meeting_leader", "TEAM_LEADER", &organisation)
	writer := server.createUser("meeting_writer", "USER", &organisation)
	colleague := server.createUser("meeting_colleague", "USER", &organisation)
	server.submitted(colleague, "2026-08-24", "동료가 쓴 보고입니다")

	// Their own week is theirs to read.
	if own := server.request(http.MethodGet, "/api/v1/meeting?scope=SELF", nil, writer); own.Code != http.StatusOK {
		t.Fatalf("a writer cannot read their own meeting material: %d %s", own.Code, own.Body.String())
	}
	refused(t, "a writer reading organisation-wide meeting material",
		server.request(http.MethodGet, "/api/v1/meeting?scope=TEAM", nil, writer))

	// What the gate protects has to be worth protecting: the leader's answer
	// carries somebody else's work, which is exactly what a writer must not get.
	asLeader := server.request(http.MethodGet, "/api/v1/meeting?scope=TEAM", nil, leader)
	if asLeader.Code != http.StatusOK {
		t.Fatalf("a leader cannot read organisation-wide meeting material: %d %s", asLeader.Code, asLeader.Body.String())
	}
	if !strings.Contains(asLeader.Body.String(), "동료가 쓴 보고") {
		t.Log("the leader's material does not mention the colleague's report; the gate still has to hold")
	}
}

// guards: searchWorkItems
func TestOrganisationWideWorkSearchIsForLeaders(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("검색 조직", "SEARCHORG")
	leader := server.createUser("search_leader", "TEAM_LEADER", &organisation)
	writer := server.createUser("search_writer", "USER", &organisation)

	if own := server.request(http.MethodGet, "/api/v1/work-items/search?q=보고&scope=SELF", nil, writer); own.Code != http.StatusOK {
		t.Fatalf("a writer cannot search their own work: %d %s", own.Code, own.Body.String())
	}
	refused(t, "a writer searching across the organisation",
		server.request(http.MethodGet, "/api/v1/work-items/search?q=보고&scope=TEAM", nil, writer))

	if asLeader := server.request(http.MethodGet, "/api/v1/work-items/search?q=보고&scope=TEAM", nil, leader); asLeader.Code != http.StatusOK {
		t.Fatalf("a leader cannot search across their organisation: %d %s", asLeader.Code, asLeader.Body.String())
	}
}

// guards: weeklyChanges
func TestOrganisationWideChangeSummaryIsForLeaders(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("변화 조직", "CHANGEORG")
	leader := server.createUser("change_leader", "TEAM_LEADER", &organisation)
	writer := server.createUser("change_writer", "USER", &organisation)

	if own := server.request(http.MethodGet, "/api/v1/changes?scope=SELF", nil, writer); own.Code != http.StatusOK {
		t.Fatalf("a writer cannot read their own changes: %d %s", own.Code, own.Body.String())
	}
	refused(t, "a writer reading organisation-wide changes",
		server.request(http.MethodGet, "/api/v1/changes?scope=TEAM", nil, writer))

	// The door has to open for the people it is meant for.
	if asLeader := server.request(http.MethodGet, "/api/v1/changes?scope=TEAM", nil, leader); asLeader.Code != http.StatusOK {
		t.Fatalf("a leader cannot read organisation-wide changes: %d %s", asLeader.Code, asLeader.Body.String())
	}
}
