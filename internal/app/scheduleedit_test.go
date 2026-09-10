package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 상황판의 네 가지 쓰기 가운데 편집만 시험이 없었습니다.
//
// 추가·체크·삭제는 각각 한 가지 일을 하지만 편집은 줄 전체를 보낸 것으로
// 갈아 끼웁니다 — 그래서 보내지 않은 것이 조용히 사라지는 유일한 쓰기이고,
// 담당자를 남에게 옮기는 권한 판정도 여기서만 걸립니다. 화면이 무엇을 실어
// 보내야 하는지가 이 계약에 달려 있으므로, 계약을 먼저 적어 둡니다.

// scheduleWorkItem files one submitted week for this person and returns the
// work item it created, which is what a board line may be linked to.
func (s *testServer) scheduleWorkItem(cookie *http.Cookie, title string) int64 {
	s.t.Helper()
	s.weekWithIssue(cookie, "2026-08-30", title, "", 40)
	id, _ := s.onlyWorkItem(cookie)["id"].(float64)
	if id == 0 {
		s.t.Fatalf("the submitted week left no work item for %q", title)
	}
	return int64(id)
}

func (s *testServer) scheduleTaskNamed(cookie *http.Cookie, title string) map[string]any {
	s.t.Helper()
	for _, task := range boardTasks(s.board(cookie, "2026-09-01", "2026-09-30", scopeTeam)) {
		if task["title"] == title {
			return task
		}
	}
	s.t.Fatalf("the board has no line called %q", title)
	return nil
}

// PUT is a whole line, not a patch: what the screen leaves out is deleted.
//
// The link to 업무 추적 is the field that makes this matter. It is not drawn
// anywhere on the board, so a person correcting a typo in a title has no way of
// knowing it is there — and if the screen does not send it back, correcting the
// typo unlinks the plan from the work it was planning.

// guards: updateScheduleTask, scheduleWorkItemAllowed
func TestEditingALineReplacesTheWholeLineIncludingItsLinkToTheWork(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("편집 본부", "EDIT")
	leader := server.createUser("edit_leader", "TEAM_LEADER", &org)
	member := server.createUser("edit_member", "USER", &org)
	work := server.scheduleWorkItem(leader, "정기 감사 준비")

	id := server.addScheduleTask(leader, map[string]any{
		"title": "감사 사전 점검", "startDate": "2026-09-08", "endDate": "2026-09-09",
		"priority": "NEEDED", "category": "감사", "note": "회의실 2층", "workItemId": work,
	})
	if got := server.scheduleTaskNamed(leader, "감사 사전 점검")["workItemId"]; got != float64(work) {
		t.Fatalf("the new line's work link is %v, want %d", got, work)
	}

	// The ordinary edit: a title fixed, everything else sent back as it was.
	edit := map[string]any{
		"title": "감사 사전 점검(정정)", "startDate": "2026-09-08", "endDate": "2026-09-09",
		"priority": "URGENT", "category": "감사", "note": "회의실 3층", "workItemId": work,
	}
	if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/schedule/%d", id), edit, leader); w.Code != http.StatusOK {
		t.Fatalf("edit the line: %d %s", w.Code, w.Body.String())
	}
	line := server.scheduleTaskNamed(leader, "감사 사전 점검(정정)")
	if line["priority"] != "URGENT" || line["note"] != "회의실 3층" || line["endDate"] != "2026-09-09" {
		t.Errorf("the edit did not land: %v", line)
	}
	if line["workItemId"] != float64(work) {
		t.Errorf("an edit that sent the work link back lost it: %v", line["workItemId"])
	}

	// And the other half of the same contract: a field left out is gone. This
	// is what the board's editor used to do to every linked row it saved.
	withoutLink := map[string]any{
		"title": "감사 사전 점검(정정)", "startDate": "2026-09-08", "endDate": "2026-09-09",
		"priority": "URGENT", "category": "감사", "note": "회의실 3층",
	}
	if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/schedule/%d", id), withoutLink, leader); w.Code != http.StatusOK {
		t.Fatalf("edit without the link: %d %s", w.Code, w.Body.String())
	}
	if got := server.scheduleTaskNamed(leader, "감사 사전 점검(정정)")["workItemId"]; got != nil {
		t.Errorf("a line edited without workItemId kept %v", got)
	}

	// A link only ever points at the assignee's own work, on edit as on create:
	// otherwise the board and 업무 추적 would be describing two different tasks.
	theirs := server.scheduleWorkItem(member, "타 부서 협의")
	elsewhere := map[string]any{
		"title": "감사 사전 점검(정정)", "startDate": "2026-09-08", "workItemId": theirs,
	}
	w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/schedule/%d", id), elsewhere, leader)
	if w.Code != http.StatusBadRequest || errorCode(w) != "WORK_ITEM_NOT_OWNED" {
		t.Errorf("a line linked to somebody else's task answered %d %s", w.Code, w.Body.String())
	}
	missing := map[string]any{
		"title": "감사 사전 점검(정정)", "startDate": "2026-09-08", "workItemId": work + 100_000,
	}
	if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/schedule/%d", id), missing, leader); errorCode(w) != "WORK_ITEM_NOT_FOUND" {
		t.Errorf("a link to a task that is not there answered %d %s", w.Code, w.Body.String())
	}
}

// Editing is not ticking, but it is guarded by the same answer: whoever may
// tick a line may rewrite it, and nobody else. Moving one onto somebody's board
// is the part that reaches outside the row — it is checked against the scope
// that decides everything else, so a leader cannot hand work to a stranger.

// guards: updateScheduleTask, authorizeScheduleTask, canManageScheduleTask
func TestALineIsRewrittenOnlyByThePeopleWhoCouldTickItAndOnlyOntoTheirOwnDepartment(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("이관 본부", "MOVE")
	other := server.createOrganization("남의 본부", "AWAY")
	leader := server.createUser("move_leader", "TEAM_LEADER", &org)
	member := server.createUser("move_member", "USER", &org)
	stranger := server.createUser("move_stranger", "USER", &other)
	memberID, strangerID := server.meID(member), server.meID(stranger)

	id := server.addScheduleTask(leader, map[string]any{
		"title": "장비 반입", "startDate": "2026-09-14",
	})
	edit := func(cookie *http.Cookie, taskID int64, body map[string]any) *httptest.ResponseRecorder {
		return server.request(http.MethodPut, fmt.Sprintf("/api/v1/schedule/%d", taskID), body, cookie)
	}

	// The member reads the line — it is a shared plan — and cannot rewrite it.
	if w := edit(member, id, map[string]any{"title": "장비 반출", "startDate": "2026-09-14"}); w.Code != http.StatusForbidden {
		t.Errorf("a member rewrote the leader's line: %d %s", w.Code, w.Body.String())
	}
	if w := edit(stranger, id, map[string]any{"title": "장비 반출", "startDate": "2026-09-14"}); w.Code != http.StatusForbidden {
		t.Errorf("somebody outside the department rewrote a line: %d %s", w.Code, w.Body.String())
	}
	if w := edit(leader, id+100_000, map[string]any{"title": "없는 줄", "startDate": "2026-09-14"}); w.Code != http.StatusNotFound {
		t.Errorf("editing a line that is not there answered %d %s", w.Code, w.Body.String())
	}

	// Handing the work to a member of the department: allowed, and the board
	// follows — including who may now tick it.
	if w := edit(leader, id, map[string]any{
		"title": "장비 반입", "startDate": "2026-09-14", "assigneeId": memberID,
	}); w.Code != http.StatusOK {
		t.Fatalf("hand the line to a member: %d %s", w.Code, w.Body.String())
	}
	moved := server.scheduleTaskNamed(member, "장비 반입")
	if moved["userId"] != float64(memberID) || moved["canEdit"] != true {
		t.Errorf("the line did not move to the member: %v", moved)
	}
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", id),
		map[string]any{"done": true}, member); w.Code != http.StatusOK {
		t.Errorf("the new assignee could not tick their line: %d %s", w.Code, w.Body.String())
	}

	// The person who put a row on the board keeps it after it has been handed
	// on: they are not the assignee any more and, as an ordinary member, cannot
	// see who is — but they wrote the line, and correcting it is not the same
	// act as moving it onto somebody. Only a move is measured against scope.
	theirs := server.addScheduleTask(member, map[string]any{"title": "자료 취합", "startDate": "2026-09-16"})
	if w := edit(leader, theirs, map[string]any{
		"title": "자료 취합", "startDate": "2026-09-16", "assigneeId": server.meID(leader),
	}); w.Code != http.StatusOK {
		t.Fatalf("hand the member's line to the leader: %d %s", w.Code, w.Body.String())
	}
	if w := edit(member, theirs, map[string]any{"title": "자료 취합(정정)", "startDate": "2026-09-16"}); w.Code != http.StatusOK {
		t.Errorf("the member who wrote the line could not correct it: %d %s", w.Code, w.Body.String())
	}
	corrected := server.scheduleTaskNamed(member, "자료 취합(정정)")
	if corrected["userId"] != float64(server.meID(leader)) {
		t.Errorf("correcting the line moved it back: %v", corrected)
	}

	// Handing it outside the department is not.
	if w := edit(leader, id, map[string]any{
		"title": "장비 반입", "startDate": "2026-09-14", "assigneeId": strangerID,
	}); w.Code != http.StatusForbidden {
		t.Errorf("a leader put work on a stranger's board: %d %s", w.Code, w.Body.String())
	}
	// A line that cannot be, is refused rather than stored crooked.
	if w := edit(leader, id, map[string]any{
		"title": "장비 반입", "startDate": "2026-09-14", "endDate": "2026-09-10", "assigneeId": memberID,
	}); w.Code != http.StatusBadRequest || errorCode(w) != "INVALID_SCHEDULE" {
		t.Errorf("an end date before the start answered %d %s", w.Code, w.Body.String())
	}
	if w := edit(leader, id, map[string]any{"title": "  ", "startDate": "2026-09-14", "assigneeId": memberID}); w.Code != http.StatusBadRequest {
		t.Errorf("a line with no title answered %d %s", w.Code, w.Body.String())
	}
	// None of the refusals changed anything.
	unchanged := server.scheduleTaskNamed(leader, "장비 반입")
	if unchanged["userId"] != float64(memberID) || unchanged["endDate"] != "2026-09-14" {
		t.Errorf("a refused edit left something behind: %v", unchanged)
	}
}
