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
