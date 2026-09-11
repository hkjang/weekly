package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The MCP surface was audited by asking it the questions a department asks and
// reading what came back. These are the answers that were wrong.

const mcpServiceFailure = "분석 중 오류가 발생했습니다."

func mcpText(t *testing.T, reply mcpReply) string {
	t.Helper()
	if len(reply.Result.Content) == 0 {
		t.Fatalf("the tool answered with no content at all: %+v", reply)
	}
	return reply.Result.Content[0].Text
}

func mcpPayload(t *testing.T, reply mcpReply) map[string]any {
	t.Helper()
	if reply.Result.IsError {
		t.Fatalf("the tool refused: %s", mcpText(t, reply))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(mcpText(t, reply)), &payload); err != nil {
		t.Fatalf("decode %s: %v", mcpText(t, reply), err)
	}
	return payload
}

// A caller's mistake and a broken service are different answers. Asked for
// weekStart "지난주" — which is exactly what a model writes when it has not been
// told the format — the surface passed the text to PostgreSQL as a date and
// answered "분석 중 오류가 발생했습니다", after writing an ERROR line blaming
// the database. The caller learned nothing it could act on and the operator was
// told the service had failed when it had not.
//
// guards: mcpDateArgument
func TestAnArgumentTheServerCannotUseIsRefusedInTheCallersTerms(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("인자 조직", "MCPARG")
	leader := server.createUser("mcparglead", "TEAM_LEADER", &org)

	for _, tool := range []string{"weekly_reports_search", "weekly_submission_overview"} {
		reply := mcpCall(t, server, leader, "tools/call", map[string]any{
			"name": tool, "arguments": map[string]any{"weekStart": "지난주"},
		})
		if !reply.Result.IsError {
			t.Fatalf("%s accepted a date that is not one: %+v", tool, reply.Result)
		}
		text := mcpText(t, reply)
		if text == mcpServiceFailure {
			t.Fatalf("%s blamed the service for the caller's argument: %q", tool, text)
		}
		if !strings.Contains(text, "YYYY-MM-DD") {
			t.Errorf("%s refused without naming the format it wants: %q", tool, text)
		}
	}
	if server.logged("mcp tool") {
		t.Error("a mistyped argument was logged as a service failure")
	}
}

// Answering zero is worse than refusing. The schema declares the status
// vocabulary and nothing enforced it, so a caller asking for "제출됨" was told
// {"total":0} — a false negative carrying the full authority of a count, which
// a model repeats as 그런 보고는 없습니다.
//
// guards: mcpStatusArgument
func TestAStatusTheServerDoesNotKnowIsRefusedRatherThanAnsweredWithZero(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("상태 조직", "MCPSTAT")
	leader := server.createUser("mcpstatlead", "TEAM_LEADER", &org)
	member := server.createUser("mcpstatmate", "USER", &org)
	server.draft(member, "2026-03-02", "상태 보고")

	reply := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_reports_search", "arguments": map[string]any{"status": "제출됨"},
	})
	if !reply.Result.IsError {
		t.Fatalf("an unknown status was answered instead of refused: %s", mcpText(t, reply))
	}
	if !strings.Contains(mcpText(t, reply), "DRAFT") {
		t.Errorf("the refusal does not say which statuses exist: %q", mcpText(t, reply))
	}

	// The vocabulary it does know still works, and works case-insensitively —
	// refusing a caller who wrote the right word in the wrong case would be the
	// same false negative wearing a different hat.
	lower := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_reports_search", "arguments": map[string]any{"status": "draft"},
	})
	if total, _ := mcpPayload(t, lower)["total"].(float64); total < 1 {
		t.Errorf("a lower case status found none of the drafts that exist: %s", mcpText(t, lower))
	}
}

// A protocol error has to arrive inside the protocol. A torn request used to be
// answered with the product's REST envelope and HTTP 400, which no MCP client
// can read: to the caller the server simply looked broken.
//
// guards: mcp
func TestATornRequestIsAnsweredAsJSONRPC(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("mcptorn", "USER", nil)

	recorder := server.requestRaw(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,`, owner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("a parse error was answered with HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	var reply struct {
		JSONRPC string `json:"jsonrpc"`
		Error   *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	if reply.JSONRPC != "2.0" || reply.Error == nil || reply.Error.Code != -32700 {
		t.Fatalf("a parse error is not a JSON-RPC parse error: %s", recorder.Body.String())
	}

	// And a frame carrying a field this server does not name is not a parse
	// error at all. The decoder used to reject unknown fields, so any client
	// that sends _meta was refused the same opaque way.
	fine := server.requestRaw(http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":{},"_meta":{"progressToken":"x"}}`, owner)
	if !strings.Contains(fine.Body.String(), `"result"`) {
		t.Errorf("a ping carrying _meta was refused: %d %s", fine.Code, fine.Body.String())
	}
}

// The server advertises 2025-03-26, where a batch is part of the protocol, and
// answered one with the REST envelope. A version claimed but not spoken is
// found only by the client it breaks.
//
// guards: mcpBatch
func TestMCPAnswersABatchTheWayTheProtocolAsks(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("mcpbatch", "USER", nil)

	recorder := server.requestRaw(http.MethodPost, "/mcp", `[
		{"jsonrpc":"2.0","id":"a","method":"ping","params":{}},
		{"jsonrpc":"2.0","method":"notifications/initialized"},
		{"jsonrpc":"2.0","id":"b","method":"tools/list","params":{}}]`, owner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("a batch was answered with HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	var replies []struct {
		ID     string `json:"id"`
		Result *struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &replies); err != nil {
		t.Fatalf("a batch was not answered with an array: %s", recorder.Body.String())
	}
	// Two answers, not three: the notification in the middle is owed none.
	if len(replies) != 2 || replies[0].ID != "a" || replies[1].ID != "b" {
		t.Fatalf("the batch answers do not match the calls: %s", recorder.Body.String())
	}
	if replies[1].Result == nil || len(replies[1].Result.Tools) == 0 {
		t.Fatalf("the listing inside the batch came back empty: %s", recorder.Body.String())
	}

	// Nothing but notifications is owed nothing at all — an empty array is the
	// one answer the specification forbids.
	quiet := server.requestRaw(http.MethodPost, "/mcp",
		`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`, owner)
	if quiet.Code != http.StatusAccepted || strings.TrimSpace(quiet.Body.String()) != "" {
		t.Errorf("a batch of notifications was answered: %d %s", quiet.Code, quiet.Body.String())
	}
}

// Search finds a report and returns one summary line. Until this tool there was
// no second step, so a model asked what somebody did quoted that line and
// presented it as the week's work.
//
// guards: mcpReportDetail
func TestTheSearchCanBeFollowedIntoTheReportItFound(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("본문 내 조직", "MCPBODY")
	theirs := server.createOrganization("본문 남의 조직", "MCPBODYX")
	leader := server.createUser("mcpbodylead", "TEAM_LEADER", &mine)
	member := server.createUser("mcpbodymate", "USER", &mine)
	stranger := server.createUser("mcpbodyouter", "USER", &theirs)

	id, version := server.draft(member, "2026-03-02", "본문 보고")
	if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "본문 보고", "version": version,
		"items": []map[string]any{{
			"category": "운영", "title": "야간 배치 안정화",
			"currentResult": "재기동 절차를 문서로 남겼습니다", "nextPlan": "감시 항목을 늘립니다",
			"issue": "장비 반입이 늦어지고 있습니다", "progress": 60,
		}},
	}, member); w.Code != http.StatusOK {
		t.Fatalf("write the report body: %d %s", w.Code, w.Body.String())
	}
	strangerID, _ := server.draft(stranger, "2026-03-02", "남의 조직 보고")

	reply := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"reportId": id},
	})
	body := mcpText(t, reply)
	for _, want := range []string{"야간 배치 안정화", "재기동 절차를 문서로 남겼습니다", "장비 반입이 늦어지고 있습니다"} {
		if !strings.Contains(body, want) {
			t.Errorf("the report body does not carry %q: %s", want, body)
		}
	}

	// A report in another organisation and a report that never existed are
	// refused in the same words: telling them apart tells the caller one exists.
	outside := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"reportId": strangerID},
	})
	absent := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"reportId": strangerID + 100000},
	})
	if !outside.Result.IsError {
		t.Fatalf("a leader read another organisation's report: %s", mcpText(t, outside))
	}
	if mcpText(t, outside) != mcpText(t, absent) {
		t.Errorf("a refused report is distinguishable from a missing one: %q vs %q",
			mcpText(t, outside), mcpText(t, absent))
	}
}

// The overview says how many did not report. Nobody could ask who.
//
// guards: mcpMissingSubmitters
func TestTheMissingSubmitterListNamesPeopleWithinTheCallersScope(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("미제출 내 조직", "MCPMISS")
	theirs := server.createOrganization("미제출 남의 조직", "MCPMISSX")
	leader := server.createUser("mcpmisslead", "TEAM_LEADER", &mine)
	filed := server.createUser("mcpmissfiled", "USER", &mine)
	server.createUser("mcpmisssilent", "USER", &mine)
	drafted := server.createUser("mcpmissdraft", "USER", &mine)
	server.createUser("mcpmissouter", "USER", &theirs)

	id, version := server.draft(filed, "2026-03-02", "제출한 보고")
	if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "제출한 보고", "version": version,
		"items": []map[string]any{{"category": "운영", "title": "제출 대상 업무", "currentResult": "했습니다", "progress": 100}},
	}, filed); w.Code != http.StatusOK {
		t.Fatalf("write the report: %d %s", w.Code, w.Body.String())
	}
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id), nil, filed); w.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	// A draft is not a submission, which is how the arrears screen counts too.
	server.draft(drafted, "2026-03-02", "쓰다 만 보고")

	payload := mcpPayload(t, mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_missing_submitters", "arguments": map[string]any{"weekStart": "2026-03-02"},
	}))
	if submitted, _ := payload["submitted"].(float64); submitted != 1 {
		t.Errorf("제출 %v명, want 1 — a draft was counted as a submission", payload["submitted"])
	}
	names := map[string]bool{}
	people, _ := payload["people"].([]any)
	for _, item := range people {
		if person, ok := item.(map[string]any); ok {
			name, _ := person["username"].(string)
			names[name] = true
		}
	}
	for stem, want := range map[string]bool{
		"mcpmisssilent": true, "mcpmissdraft": true, "mcpmisslead": true,
		"mcpmissfiled": false, "mcpmissouter": false,
	} {
		username := server.lastCreatedUsername(stem)
		if names[username] != want {
			t.Errorf("%s in the missing list: %v, want %v (%v)", stem, names[username], want, names)
		}
	}

	// A member is not handed a list of names, and is not offered the tool that
	// would give them one.
	member := server.createUser("mcpmissmember", "USER", &mine)
	refusal := mcpCall(t, server, member, "tools/call", map[string]any{
		"name": "weekly_missing_submitters", "arguments": map[string]any{},
	})
	if !refusal.Result.IsError {
		t.Fatalf("a member read the missing list: %s", mcpText(t, refusal))
	}
	if mcpToolNames(mcpCall(t, server, member, "tools/list", map[string]any{}))["weekly_missing_submitters"] {
		t.Error("a member is offered a tool that refuses them")
	}
}

// The board shipped four versions after this surface did and nothing joined
// them up, so a model asked what the department has on this month answered from
// weekly reports — which describe the week that happened, not the work that is
// due on Friday.
//
// guards: mcpScheduleTasks
func TestTheDepartmentBoardIsReadableThroughMCP(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("상황판 조직", "MCPBOARD")
	leader := server.createUser("mcpboardlead", "TEAM_LEADER", &org)
	member := server.createUser("mcpboardmate", "USER", &org)
	memberID := server.meID(member)

	server.addScheduleTask(leader, map[string]any{
		"title": "월말 정산", "assigneeId": memberID,
		"startDate": "2026-05-04", "endDate": "2026-05-08", "priority": "URGENT",
	})
	done := server.addScheduleTask(leader, map[string]any{
		"title": "장비 반입", "assigneeId": memberID,
		"startDate": "2026-05-11", "endDate": "2026-05-15",
	})
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", done),
		map[string]any{"done": true}, leader); w.Code != http.StatusOK {
		t.Fatalf("tick a line: %d %s", w.Code, w.Body.String())
	}

	payload := mcpPayload(t, mcpCall(t, server, leader, "tools/call", map[string]any{
		"name":      "schedule_board_tasks",
		"arguments": map[string]any{"from": "2026-05-01", "to": "2026-05-31"},
	}))
	if total, _ := payload["total"].(float64); total != 2 {
		t.Fatalf("the board holds two lines and the tool returned %v: %v", payload["total"], payload)
	}
	summary, _ := payload["summary"].(map[string]any)
	if urgent, _ := summary["urgent"].(float64); urgent != 1 {
		t.Errorf("긴급 %v건, want 1", summary["urgent"])
	}
	if finished, _ := summary["done"].(float64); finished != 1 {
		t.Errorf("완료 %v건, want 1", summary["done"])
	}

	// The filter is enforced, not merely declared: the lesson the status filter
	// taught, applied to the vocabulary this tool declares.
	open := mcpPayload(t, mcpCall(t, server, leader, "tools/call", map[string]any{
		"name":      "schedule_board_tasks",
		"arguments": map[string]any{"from": "2026-05-01", "to": "2026-05-31", "state": "OPEN"},
	}))
	if total, _ := open["total"].(float64); total != 1 {
		t.Errorf("미완료 %v건, want 1: %v", open["total"], open)
	}
	nonsense := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name":      "schedule_board_tasks",
		"arguments": map[string]any{"state": "늦음"},
	})
	if !nonsense.Result.IsError {
		t.Errorf("an undeclared state was accepted: %s", mcpText(t, nonsense))
	}
}

// Every tool the listing offers answers its own default call.
//
// The sweep exists because the failure it catches has happened twice: a tool
// whose optional argument was read as the four characters "<nil>", and a tool
// listed for a caller who may not call it. Both were found by a person asking
// the obvious first question by hand. A listing is a promise, and this is the
// cheapest possible check that the promise holds for every entry in it.
//
// guards: mcpTools
func TestEveryToolTheListingOffersAnswersItsOwnDefaultCall(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("전수 조직", "MCPALL")
	callers := map[string]*http.Cookie{
		"관리자": server.admin,
		"팀장":  server.createUser("mcpalllead", "TEAM_LEADER", &org),
		"구성원": server.createUser("mcpallmate", "USER", &org),
	}
	// weekly_report_detail is the one tool with a required argument; every caller
	// is given a report of their own to open.
	reports := map[string]int64{}
	for who, cookie := range callers {
		id, _ := server.draft(cookie, "2026-03-02", who+" 전수 점검 보고")
		reports[who] = id
	}

	for who, cookie := range callers {
		for name := range mcpToolNames(mcpCall(t, server, cookie, "tools/list", map[string]any{})) {
			// The two tools with a required argument are given a real one;
			// every other tool must answer with none at all.
			arguments := map[string]any{}
			switch name {
			case "weekly_report_detail":
				arguments["reportId"] = reports[who]
			case "weekly_reports_text_search":
				arguments["q"] = "전수 점검"
			}
			reply := mcpCall(t, server, cookie, "tools/call", map[string]any{
				"name": name, "arguments": arguments,
			})
			if reply.Error != nil {
				t.Errorf("%s: %s answered a protocol error: %s", who, name, reply.Error.Message)
				continue
			}
			if reply.Result.IsError {
				t.Errorf("%s: %s refused the call its own listing invites: %s", who, name, mcpText(t, reply))
				continue
			}
			if strings.Contains(mcpText(t, reply), mcpServiceFailure) {
				t.Errorf("%s: %s failed on its default arguments", who, name)
			}
		}
	}
}

// The 개인 설정 MCP card is where a person finds out what this surface can do
// before they connect anything to it, and it named two tools while the server
// offered four — period_report_rollup had been missing from it since the day it
// shipped. A list maintained by hand in a second place is a list that is wrong.
//
// guards: mcpTools
func TestTheProfileCardNamesEveryMCPToolTheServerOffers(t *testing.T) {
	app := &App{}
	card, err := os.ReadFile("../../frontend/src/pages/ProfilePage.tsx")
	if err != nil {
		t.Fatalf("read the profile screen: %v", err)
	}
	for _, tool := range app.mcpTools(&principal{Role: "ADMIN"}) {
		name, _ := tool["name"].(string)
		if !strings.Contains(string(card), name) {
			t.Errorf("the MCP card on 개인 설정 does not name %s, so nobody connecting a client learns it exists", name)
		}
	}
}

// A leader's report is often written from the team's, and those materials are
// whole reports of their own — a leader with thirty members carries thirty of
// them. The tool names them and gives the id to open each; handing the caller
// the department's entire week to answer a question about one person's is the
// opposite of what a bounded payload is for.
//
// guards: mcpReportDetail
func TestALeadersReportNamesItsMaterialsWithoutCarryingThem(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("재료 조직", "MCPMAT")
	leader := server.createUser("mcpmatlead", "TEAM_LEADER", &org)
	member := server.createUser("mcpmatmate", "USER", &org)
	memberID := server.userIDOf(server.lastCreatedUsername("mcpmatmate"))

	memberReport, memberVersion := server.draft(member, "2026-03-02", "팀원 요약")
	fillInclusionTestReport(t, server, member, memberReport, memberVersion, "팀원 요약", "팀원이 한 일")
	if w := server.request(http.MethodPut, "/api/v1/me/report-inclusions",
		map[string]any{"memberIds": []int64{memberID}}, leader); w.Code != http.StatusOK {
		t.Fatalf("select the member: %d %s", w.Code, w.Body.String())
	}
	leaderReport, leaderVersion := server.draft(leader, "2026-03-02", "팀장 요약")
	fillInclusionTestReport(t, server, leader, leaderReport, leaderVersion, "팀장 요약", "팀장이 한 일")

	payload := mcpPayload(t, mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"reportId": leaderReport},
	}))
	materials, _ := payload["includedMaterials"].([]any)
	if len(materials) != 1 {
		t.Fatalf("the leader's report was written from one member's and the tool named %d: %v", len(materials), payload)
	}
	material, _ := materials[0].(map[string]any)
	if id, _ := material["reportId"].(float64); int64(id) != memberReport {
		t.Errorf("the material does not carry the id needed to open it: %v", material)
	}
	if count, _ := material["itemCount"].(float64); count != 1 {
		t.Errorf("the material does not say how much is in it: %v", material)
	}
	// The member's own lines are not inlined — the caller is told where they
	// are, and asks for them if it wants them.
	if strings.Contains(mcpText(t, mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"reportId": leaderReport},
	})), "팀원이 한 일") {
		t.Error("the leader's report carried the whole body of every material it was written from")
	}
}

// 어느 주인지 모르는 질문. The report search filters by week and status, so an
// agent asked "그 장애 얘기가 어느 보고서에 있었나" could only page weeks and
// read summaries — while the product has had a content search, with the
// caller's own visibility rules and snippets, the whole time.
//
// guards: mcpTextSearch
func TestReportsCanBeFoundByWhatTheySayNotOnlyByWhen(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("검색 조직", "MCPTXT")
	theirs := server.createOrganization("검색 남의 조직", "MCPTXTX")
	leader := server.createUser("mcptxtlead", "TEAM_LEADER", &mine)
	member := server.createUser("mcptxtmate", "USER", &mine)
	stranger := server.createUser("mcptxtouter", "USER", &theirs)

	id, version := server.draft(member, "2026-03-02", "3월 첫째 주")
	fillInclusionTestReport(t, server, member, id, version, "3월 첫째 주", "야간 배치 장애 대응")
	outsideID, outsideVersion := server.draft(stranger, "2026-03-02", "남의 조직 주간보고")
	fillInclusionTestReport(t, server, stranger, outsideID, outsideVersion, "남의 조직 주간보고", "야간 배치 장애 대응")

	payload := mcpPayload(t, mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_reports_text_search", "arguments": map[string]any{"q": "야간 배치"},
	}))
	hits, _ := payload["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("the search found %d reports, want the one in the caller's own organisation: %v", len(hits), payload)
	}
	hit, _ := hits[0].(map[string]any)
	if reportID, _ := hit["reportId"].(float64); int64(reportID) != id {
		t.Errorf("the search reached outside the caller's organisation: %v", hit)
	}
	// A hit that carries no snippet is a citation the caller cannot check.
	matches, _ := hit["matches"].([]any)
	if len(matches) == 0 {
		t.Errorf("the hit carries no snippet of what matched: %v", hit)
	}
	// And the answer says where the body is, because the hit is not the body.
	if note, _ := payload["note"].(string); !strings.Contains(note, "weekly_report_detail") {
		t.Errorf("the result does not say how to read the report it found: %q", note)
	}

	// Two characters is the floor the screen uses; below it the search returns
	// everything or nothing, and either is a wrong answer stated confidently.
	short := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_reports_text_search", "arguments": map[string]any{"q": "야"},
	})
	if !short.Result.IsError {
		t.Errorf("a one character query was answered instead of refused: %s", mcpText(t, short))
	}

	// A near miss, not a word that appears nowhere: a query matching nothing
	// leaves the widening passes empty either way, and the flags stay false
	// whether the gate held or not.
	const nearMiss = "야간 배티"
	capabilities := server.app.capabilities
	if capabilities.Trigram {
		widened := mcpPayload(t, mcpCall(t, server, leader, "tools/call", map[string]any{
			"name": "weekly_reports_text_search", "arguments": map[string]any{"q": nearMiss},
		}))
		if fuzzy, _ := widened["fuzzy"].(bool); !fuzzy {
			t.Fatalf("the near miss did not reach the approximate pass even with pg_trgm present: %v", widened)
		}
		// A match that is not literal must say so. A model told only "찾았습니다"
		// quotes an approximate hit as though it were the report's own words.
		if note, _ := widened["note"].(string); !strings.Contains(note, "원문 인용처럼 다루지 마세요") {
			t.Errorf("an approximate result was handed over without saying it was approximate: %q", note)
		}
	}

	// And when the widening passes cannot run at all, the answer says why.
	// Without it a search that could not widen looks exactly like one that
	// widened and found nothing — and the reader concludes it does not exist.
	server.app.capabilities = databaseCapabilities{}
	t.Cleanup(func() { server.app.capabilities = capabilities })
	narrow := mcpPayload(t, mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_reports_text_search", "arguments": map[string]any{"q": nearMiss},
	}))
	if note, _ := narrow["note"].(string); !strings.Contains(note, "pg_trgm") {
		t.Errorf("a thin result does not say why it stayed thin: %q", note)
	}
}
