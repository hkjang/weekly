package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// meID reads back the id of the account a cookie belongs to, which is what the
// board's assignee field is written in terms of.
func (s *testServer) meID(cookie *http.Cookie) int64 {
	s.t.Helper()
	w := s.request(http.MethodGet, "/api/v1/me", nil, cookie)
	if w.Code != http.StatusOK {
		s.t.Fatalf("read the session: %d %s", w.Code, w.Body.String())
	}
	user, _ := decodeData(s.t, w)["user"].(map[string]any)
	id, _ := user["id"].(float64)
	if id == 0 {
		s.t.Fatalf("the session has no user id: %s", w.Body.String())
	}
	return int64(id)
}

// board reads one month the way the screen does and returns the payload.
func (s *testServer) board(cookie *http.Cookie, from, to, scope string) map[string]any {
	s.t.Helper()
	w := s.request(http.MethodGet,
		fmt.Sprintf("/api/v1/schedule?from=%s&to=%s&scope=%s", from, to, scope), nil, cookie)
	if w.Code != http.StatusOK {
		s.t.Fatalf("read the board: %d %s", w.Code, w.Body.String())
	}
	return decodeData(s.t, w)
}

func boardTasks(payload map[string]any) []map[string]any {
	raw, _ := payload["tasks"].([]any)
	tasks := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if task, ok := item.(map[string]any); ok {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

func (s *testServer) addScheduleTask(cookie *http.Cookie, body map[string]any) int64 {
	s.t.Helper()
	w := s.request(http.MethodPost, "/api/v1/schedule", body, cookie)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		s.t.Fatalf("add a line: %d %s", w.Code, w.Body.String())
	}
	id, _ := decodeData(s.t, w)["id"].(float64)
	return int64(id)
}

// A department that runs on a schedule plans the month first and reports on it
// afterwards. The whole feature is one screen: the plan, and a checkbox on
// every line of it.

// guards: createScheduleTask, listScheduleTasks, setScheduleTaskDone
func TestTheMonthBoardCarriesTheDepartmentsPlanAndItsCheckboxes(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("일정 본부", "SCHED")
	leader := server.createUser("sched_leader", "TEAM_LEADER", &org)
	member := server.createUser("sched_member", "USER", &org)
	memberID := server.meID(member)

	// The leader lays out the month, including a line for somebody else — which
	// is the half of this that a personal to-do list cannot do.
	own := server.addScheduleTask(leader, map[string]any{
		"title": "감사 준비 회의", "startDate": "2026-09-07", "priority": "URGENT",
	})
	assigned := server.addScheduleTask(leader, map[string]any{
		"title": "감사 자료 제출", "startDate": "2026-09-09", "endDate": "2026-09-11",
		"assigneeId": memberID, "priority": "NEEDED", "category": "감사",
	})

	payload := server.board(leader, "2026-09-01", "2026-09-30", scopeTeam)
	tasks := boardTasks(payload)
	if len(tasks) != 2 {
		t.Fatalf("the board shows %d lines, want 2: %v", len(tasks), tasks)
	}
	// 긴급 first: the day's lines are ordered by what a board is read for.
	if tasks[0]["title"] != "감사 준비 회의" || tasks[0]["priority"] != "URGENT" {
		t.Errorf("the first line is %v", tasks[0])
	}
	if tasks[1]["endDate"] != "2026-09-11" {
		t.Errorf("a task that runs to the 11th arrived as %v", tasks[1]["endDate"])
	}
	summary, _ := payload["summary"].(map[string]any)
	if summary["total"] != float64(2) || summary["done"] != float64(0) || summary["urgent"] != float64(1) {
		t.Errorf("the summary reads %v", summary)
	}

	// The checkbox. One request, one row, and it records who pressed it.
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", own),
		map[string]any{"done": true}, leader); w.Code != http.StatusOK {
		t.Fatalf("tick a line: %d %s", w.Code, w.Body.String())
	}
	after := server.board(leader, "2026-09-01", "2026-09-30", scopeTeam)
	ticked := boardTasks(after)[0]
	if ticked["done"] != true {
		t.Errorf("the ticked line came back as %v", ticked)
	}
	if ticked["doneByName"] == "" || ticked["doneByName"] == nil {
		t.Errorf("the board does not say who ticked it: %v", ticked)
	}
	summaryAfter, _ := after["summary"].(map[string]any)
	if summaryAfter["done"] != float64(1) || summaryAfter["urgent"] != float64(0) {
		t.Errorf("the summary did not follow the checkbox: %v", summaryAfter)
	}

	// Unticking clears the name as well as the time: a board that shows
	// "완료: 팀장" under an empty box is telling two stories at once.
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", own),
		map[string]any{"done": false}, leader); w.Code != http.StatusOK {
		t.Fatalf("untick a line: %d %s", w.Code, w.Body.String())
	}
	cleared := boardTasks(server.board(leader, "2026-09-01", "2026-09-30", scopeTeam))[0]
	if cleared["done"] != false || cleared["doneByName"] != nil && cleared["doneByName"] != "" {
		t.Errorf("unticking left something behind: %v", cleared)
	}
	_ = assigned
}

// A month that only shows work starting inside it is not a board. The long
// pieces — the ones a department most needs to see — start somewhere else.

// guards: listScheduleTasks, scheduleRange
func TestWorkThatCrossesTheMonthIsOnBothMonths(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("연동 본부", "SPAN")
	leader := server.createUser("span_leader", "TEAM_LEADER", &org)

	server.addScheduleTask(leader, map[string]any{
		"title": "연말 정산 준비", "startDate": "2026-08-24", "endDate": "2026-09-04",
	})
	august := boardTasks(server.board(leader, "2026-08-01", "2026-08-31", scopeTeam))
	september := boardTasks(server.board(leader, "2026-09-01", "2026-09-30", scopeTeam))
	if len(august) != 1 || len(september) != 1 {
		t.Fatalf("a task from 8/24 to 9/4 appears on %d August boards and %d September boards",
			len(august), len(september))
	}
	october := boardTasks(server.board(leader, "2026-10-01", "2026-10-31", scopeTeam))
	if len(october) != 0 {
		t.Errorf("it also turned up in October: %v", october)
	}
}

// The plan is shared and the writing is not. Everyone in the department reads
// the board — that is what a board is — but a member ticking somebody else's
// line would be reporting work they did not do.

// guards: scheduleVisibility, canManageScheduleTask, manageableOwners
func TestEveryoneReadsTheBoardAndOnlyTheirOwnLinesAreTheirsToTick(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("공유 본부", "SHARE")
	other := server.createOrganization("남의 본부", "OTHER")
	leader := server.createUser("share_leader", "TEAM_LEADER", &org)
	member := server.createUser("share_member", "USER", &org)
	stranger := server.createUser("share_stranger", "USER", &other)
	memberID := server.meID(member)

	leaderLine := server.addScheduleTask(leader, map[string]any{
		"title": "본부장 보고", "startDate": "2026-09-14",
	})
	memberLine := server.addScheduleTask(leader, map[string]any{
		"title": "자료 취합", "startDate": "2026-09-15", "assigneeId": memberID,
	})

	// The member sees the department's month, including the leader's line.
	seen := boardTasks(server.board(member, "2026-09-01", "2026-09-30", scopeTeam))
	if len(seen) != 2 {
		t.Fatalf("the member sees %d of the department's 2 lines", len(seen))
	}
	for _, task := range seen {
		mine := task["title"] == "자료 취합"
		if task["canEdit"] != mine {
			t.Errorf("canEdit is %v on %q for the member it does%s belong to",
				task["canEdit"], task["title"], map[bool]string{true: "", false: " not"}[mine])
		}
	}
	// And the server enforces what the screen drew.
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", leaderLine),
		map[string]any{"done": true}, member); w.Code != http.StatusForbidden {
		t.Errorf("a member ticked the leader's line: %d %s", w.Code, w.Body.String())
	}
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", memberLine),
		map[string]any{"done": true}, member); w.Code != http.StatusOK {
		t.Errorf("a member could not tick their own line: %d %s", w.Code, w.Body.String())
	}

	// Another department's board is not this person's business.
	outside := boardTasks(server.board(stranger, "2026-09-01", "2026-09-30", scopeTeam))
	if len(outside) != 0 {
		t.Errorf("somebody outside the department read %d of its lines", len(outside))
	}
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", memberLine),
		map[string]any{"done": true}, stranger); w.Code != http.StatusForbidden {
		t.Errorf("somebody outside the department ticked a line: %d", w.Code)
	}
	// A leader cannot put work on a stranger's board either.
	strangerID := server.meID(stranger)
	if w := server.request(http.MethodPost, "/api/v1/schedule", map[string]any{
		"title": "남의 일", "startDate": "2026-09-16", "assigneeId": strangerID,
	}, leader); w.Code != http.StatusForbidden {
		t.Errorf("a leader assigned work outside their organisation: %d %s", w.Code, w.Body.String())
	}
}

// Typing an SR number and getting the title is the point of the ITSM link. The
// two addresses are the part that is easy to get wrong: the one that answers a
// machine is not the one a person opens.

// guards: lookupITSM, itsmLookup, itsmURL, itsmTitle
func TestAnSRNumberBringsBackItsTitleAndLinksToThePortalNotTheAPI(t *testing.T) {
	server := newTestServer(t)
	asked := make(chan string, 4)
	itsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.URL.String()
		if r.URL.Query().Get("sr_id") != "SR2609-00001" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer itsm-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"result":{"request":[{"subject":"인사시스템 권한 변경 요청"}]}}`)
	}))
	defer itsm.Close()

	if w := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{
		"itsm.enabled":     "true",
		"itsm.lookup_url":  itsm.URL + "/api/sr",
		"itsm.link_url":    "https://portal.internal.test/sr/{id}",
		"itsm.title_path":  "result.request.0.subject",
		"itsm.auth_header": "Authorization",
		"itsm.auth_token":  "Bearer itsm-token",
		"itsm.label":       "SR",
	}}, server.admin); w.Code != http.StatusOK {
		t.Fatalf("configure the ITSM link: %d %s", w.Code, w.Body.String())
	}

	writer := server.createUser("itsm_writer", "USER", nil)
	lookup := server.request(http.MethodGet, "/api/v1/itsm/lookup?id=SR2609-00001", nil, writer)
	if lookup.Code != http.StatusOK {
		t.Fatalf("look up an SR: %d %s", lookup.Code, lookup.Body.String())
	}
	found := decodeData(t, lookup)
	if found["title"] != "인사시스템 권한 변경 요청" {
		t.Errorf("the lookup returned %v", found["title"])
	}
	// The link is the portal, never the API the title came from.
	if found["url"] != "https://portal.internal.test/sr/SR2609-00001" {
		t.Errorf("the link points at %v", found["url"])
	}
	select {
	case url := <-asked:
		if url != "/api/sr?sr_id=SR2609-00001" {
			t.Errorf("the ITSM was asked for %q", url)
		}
	default:
		t.Error("the ITSM was never asked")
	}

	// The number goes on the board, and the board resolves the link the same way.
	id := server.addScheduleTask(writer, map[string]any{
		"title": found["title"], "startDate": "2026-09-21", "srId": "SR2609-00001", "priority": "IMPORTANT",
	})
	line := boardTasks(server.board(writer, "2026-09-01", "2026-09-30", scopeSelf))[0]
	if line["srId"] != "SR2609-00001" || line["srUrl"] != "https://portal.internal.test/sr/SR2609-00001" {
		t.Errorf("the board line lost its SR link: %v", line)
	}
	if line["priority"] != "IMPORTANT" {
		t.Errorf("the line's priority is %v", line["priority"])
	}
	_ = id

	// A number that is not there is said plainly, not as a transport error.
	missing := server.request(http.MethodGet, "/api/v1/itsm/lookup?id=SR2609-99999", nil, writer)
	if missing.Code != http.StatusBadGateway || !strings.Contains(missing.Body.String(), "찾지 못했습니다") {
		t.Errorf("an unknown SR answered %d %s", missing.Code, missing.Body.String())
	}
	// And a number that could change the shape of the URL never reaches it.
	bad := server.request(http.MethodGet, "/api/v1/itsm/lookup?id=../../etc/passwd", nil, writer)
	if bad.Code != http.StatusBadGateway || !strings.Contains(bad.Body.String(), "형식") {
		t.Errorf("a malformed SR number answered %d %s", bad.Code, bad.Body.String())
	}
}

// Some of these systems have no API at all. The only thing available is the
// portal page a person would open, where the title is in <title>.

// guards: itsmTitle
func TestAnITSMWithNoAPIIsReadFromItsPage(t *testing.T) {
	server := newTestServer(t)
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<html><head><title>[%s] 방화벽 정책 변경</title></head><body>…</body></html>",
			r.URL.Query().Get("sr_id"))
	}))
	defer portal.Close()

	if w := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{
		"itsm.enabled":     "true",
		"itsm.lookup_url":  portal.URL + "/sr/view",
		"itsm.title_regex": "<title>([^<]+)</title>",
	}}, server.admin); w.Code != http.StatusOK {
		t.Fatalf("configure the ITSM link: %d %s", w.Code, w.Body.String())
	}
	reader := server.createUser("itsm_html", "USER", nil)
	found := server.request(http.MethodGet, "/api/v1/itsm/lookup?id=SR2609-00007", nil, reader)
	if found.Code != http.StatusOK {
		t.Fatalf("look up an SR from a page: %d %s", found.Code, found.Body.String())
	}
	data := decodeData(t, found)
	if data["title"] != "[SR2609-00007] 방화벽 정책 변경" {
		t.Errorf("the page's title arrived as %v", data["title"])
	}
	// With no separate portal configured the link falls back to the address
	// that answered, which for this kind of ITSM is the page itself.
	if url, _ := data["url"].(string); !strings.HasPrefix(url, portal.URL) {
		t.Errorf("the link points at %q", url)
	}
}

// A setting that cannot work has to be refused where it is typed. A regular
// expression that does not compile, saved, fails later with a message about
// somebody else's service.

// guards: validOptionalRegex, validITSMTemplate
func TestASettingThatCannotWorkIsRefusedAtTheBox(t *testing.T) {
	server := newTestServer(t)
	for _, bad := range []map[string]string{
		{"itsm.title_regex": "<title>([^<"},
		{"itsm.id_pattern": "("},
		{"itsm.lookup_url": "file:///etc/passwd"},
		{"itsm.link_url": "notaurl"},
	} {
		w := server.request(http.MethodPut, "/api/v1/admin/settings",
			map[string]any{"settings": bad}, server.admin)
		if w.Code == http.StatusOK {
			t.Errorf("%v was accepted", bad)
		}
	}
	// The shapes that do work are accepted, including a placeholder in a path.
	ok := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{
		"itsm.lookup_url": "https://itsm.internal.test/api/sr",
		"itsm.link_url":   "https://portal.internal.test/sr/{id}",
		"itsm.id_pattern": `^SR[0-9]{4}-[0-9]{5}$`,
	}}, server.admin)
	if ok.Code != http.StatusOK {
		t.Errorf("a working configuration was refused: %d %s", ok.Code, ok.Body.String())
	}
}

// Four levels, drawn in four colours. Anything else becomes 일반 rather than a
// row on the board in a colour nothing draws.

// guards: normalizePriority
func TestAnUnknownPriorityBecomesTheOrdinaryOne(t *testing.T) {
	for value, want := range map[string]string{
		"URGENT": priorityUrgent, "urgent": priorityUrgent,
		"IMPORTANT": priorityImportant, "NEEDED": priorityNeeded,
		"NORMAL": priorityNormal, "": priorityNormal, "CRITICAL": priorityNormal, "1": priorityNormal,
	} {
		if got := normalizePriority(value); got != want {
			t.Errorf("normalizePriority(%q) = %q, want %q", value, got, want)
		}
	}
}
