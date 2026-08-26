package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Rules the code states and nothing ran: each of these could be inverted and
// the whole suite stayed green. They are small, and that is the point — the
// small ones are the ones nobody thinks to write a test for.

// guards: addComment
func TestACommentHasToSaySomethingAndNotTooMuch(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("의견 조직", "COMMENTS")
	author := server.createUser("comment_author", "USER", &organisation)
	reportID := server.submitted(author, "2026-08-24", "의견이 달릴 보고")
	path := fmt.Sprintf("/api/v1/reports/%d/comments", reportID)

	for _, refusal := range []struct {
		name    string
		content string
	}{
		{"빈 의견", ""},
		{"공백뿐인 의견", "   \n  "},
		{"5000자를 넘는 의견", strings.Repeat("a", 5001)},
	} {
		w := server.request(http.MethodPost, path, map[string]any{"content": refusal.content}, author)
		if w.Code != http.StatusBadRequest || errorCode(w) != "INVALID_COMMENT" {
			t.Errorf("%s: %d %s", refusal.name, w.Code, w.Body.String())
		}
	}
	// The boundary itself is allowed.
	if w := server.request(http.MethodPost, path, map[string]any{"content": strings.Repeat("a", 5000)}, author); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Errorf("정확히 5000자가 거부됐습니다: %d %s", w.Code, w.Body.String())
	}
}

// guards: mergeWorkItem
func TestFoldingATaskNeedsATaskToFoldItInto(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("merge_target", "USER", nil)
	source, _ := twoWorkItemsOf(t, server, owner)

	for _, into := range []any{0, -5} {
		w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/merge", source),
			map[string]any{"intoId": into}, owner)
		if w.Code != http.StatusBadRequest || errorCode(w) != "INVALID_TARGET" {
			t.Errorf("intoId %v: %d %s", into, w.Code, w.Body.String())
		}
	}
}

// guards: splitWorkItem
func TestSplittingATaskUnderItsOwnNameIsRefused(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("split_same", "USER", nil)
	// Two weeks of one task, so splitting one week off is a real split and the
	// request reaches the name check instead of stopping at "all of them".
	server.weekWithIssue(owner, "2026-08-17", "나눌 업무", "", 30)
	server.weekWithIssue(owner, "2026-08-24", "나눌 업무", "", 40)
	source := server.workItemNamed(server.lastCreatedUsername("split_same"), "나눌 업무")

	title := "나눌 업무"
	var reportItemID int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT i.id FROM report_items i JOIN weekly_reports r ON r.id=i.report_id
			WHERE i.work_item_id=$1 ORDER BY r.week_start LIMIT 1`, source).Scan(&reportItemID); err != nil {
		t.Fatal(err)
	}
	w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/split", source),
		map[string]any{"title": title, "reportItemIds": []int64{reportItemID}}, owner)
	if w.Code != http.StatusBadRequest || errorCode(w) != "SAME_TITLE" {
		t.Errorf("같은 제목으로 분리됐습니다: %d %s", w.Code, w.Body.String())
	}
}

// guards: meetingMode
//
// The agenda grows a 타 조직 대기 section only when something is actually
// waiting on another organisation. Nothing ever put a dependency in front of
// it, so the section could be switched off entirely and every meeting test
// still passed — the room would simply never be told who to connect.
func TestTheAgendaRaisesWorkWaitingOnAnotherOrganisation(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("대기 내 조직", "WAITMINE")
	theirs := server.createOrganization("대기 남의 조직", "WAITTHEIRS")
	waiting := server.createUser("wait_here", "USER", &mine)
	blocking := server.createUser("wait_there", "USER", &theirs)

	// Both ends have to still be running: work that is finished is nothing to
	// connect anybody about, and the query says so.
	server.weekWithIssue(waiting, "2026-08-24", "기다리는 업무", "", 40)
	server.weekWithIssue(blocking, "2026-08-24", "막고 있는 업무", "", 30)
	waitingTask := server.workItemNamed(server.lastCreatedUsername("wait_here"), "기다리는 업무")
	blockingTask := server.workItemNamed(server.lastCreatedUsername("wait_there"), "막고 있는 업무")
	linkBetween(t, server, waiting, waitingTask, blockingTask)

	w := server.request(http.MethodGet, "/api/v1/meeting?kind=YEAR&period=2026&scope=SELF", nil, waiting)
	if w.Code != http.StatusOK {
		t.Fatalf("read the agenda: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Sections []struct {
				Key     string `json:"key"`
				Entries []struct {
					Title string `json:"title"`
				} `json:"entries"`
			} `json:"sections"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, section := range envelope.Data.Sections {
		if section.Key != "DEPENDENCY" {
			continue
		}
		if len(section.Entries) == 0 {
			t.Fatal("the 타 조직 대기 section is there but empty")
		}
		return
	}
	t.Fatalf("the agenda never raised the task waiting on another organisation: %s", w.Body.String())
}

// workItemNamed finds a task by its owner's username and the title reported.
func (s *testServer) workItemNamed(username, title string) int64 {
	s.t.Helper()
	var id int64
	if err := s.app.db.QueryRow(s.ctx(),
		`SELECT w.id FROM work_items w JOIN users u ON u.id = w.user_id
			WHERE u.username = $1 AND w.title = $2`, username, title).Scan(&id); err != nil {
		s.t.Fatalf("find the task %q: %v", title, err)
	}
	return id
}
