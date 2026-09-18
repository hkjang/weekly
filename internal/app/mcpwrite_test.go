package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 주간보고를 MCP 로 쓰기.
//
// The tools write through the editor's own functions, so what these tests
// hold them to is the shape a model needs and the two things a model must
// not be able to do: overwrite what it did not read, and touch anybody else's
// report.

func mcpAs(server *testServer, bearer string, params map[string]any) mcpReply {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+bearer)
	recorder := httptest.NewRecorder()
	server.app.mux.ServeHTTP(recorder, request)
	var reply mcpReply
	_ = json.Unmarshal(recorder.Body.Bytes(), &reply)
	return reply
}

func mcpData(t *testing.T, reply mcpReply) map[string]any {
	t.Helper()
	if reply.Error != nil {
		t.Fatalf("protocol error: %s", reply.Error.Message)
	}
	if reply.Result.IsError {
		t.Fatalf("refused: %s", mcpText(t, reply))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(mcpText(t, reply)), &data); err != nil {
		t.Fatalf("decode %s: %v", mcpText(t, reply), err)
	}
	return data
}

// guards: mcpCreateReport, mcpUpdateReport
func TestAModelCanWriteItsOwnReportWithoutOverwritingWhatItDidNotRead(t *testing.T) {
	server := newTestServer(t)
	writer := server.createUser("mcpwriter", "USER", nil)
	key := createKey(t, server, writer, "쓰는 키", []string{"reports:read", "mcp:read", "mcp:write"})

	// Create, with a mid-week date that is snapped to the week it is in.
	created := mcpData(t, mcpAs(server, key, map[string]any{
		"name": "weekly_report_create", "arguments": map[string]any{
			"weekStart": "2026-03-04", "summary": "MCP 로 쓴 요약",
			"items": []map[string]any{
				{"title": "인증 연동", "category": "개발", "currentResult": "설계 완료", "progress": 40},
				{"title": "결산 자동화", "category": "운영", "issue": "장비 반입 지연", "progress": 10},
			},
		},
	}))
	if created["weekStart"] != "2026-03-02" {
		t.Errorf("a Wednesday was filed as %v, want the Monday of its week", created["weekStart"])
	}
	if created["status"] != "DRAFT" {
		t.Errorf("created as %v, want DRAFT — a model does not submit", created["status"])
	}
	id := int64(created["id"].(float64))

	// Read it back the way the model would — by the week, with no id in hand
	// — and keep the version.
	detail := mcpData(t, mcpAs(server, key, map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"weekStart": "2026-03-05"},
	}))
	if int64(detail["id"].(float64)) != id {
		t.Fatalf("asking for my report of that week found %v, want %d", detail["id"], id)
	}
	// And a week with nothing in it says so, and says what to do.
	none := mcpAs(server, key, map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"weekStart": "2025-06-02"},
	})
	if !none.Result.IsError || !strings.Contains(mcpText(t, none), "weekly_report_create") {
		t.Errorf("an empty week did not point at the create tool: %+v", none.Result)
	}
	items, _ := detail["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("the report has %d items, want 2", len(items))
	}
	version := detail["version"].(float64)
	firstID := items[0].(map[string]any)["id"].(float64)

	// Summary only: the items stay. The editor's PUT would have wiped them.
	updated := mcpData(t, mcpAs(server, key, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{
			"reportId": id, "version": version, "summary": "요약만 고침",
		},
	}))
	if count, _ := updated["itemCount"].(float64); count != 2 {
		t.Errorf("a summary-only update left %v items, want 2", updated["itemCount"])
	}
	version = updated["version"].(float64)

	// Append: the two stay and a third arrives.
	appended := mcpData(t, mcpAs(server, key, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{
			"reportId": id, "version": version, "mode": "APPEND",
			"items": []map[string]any{{"title": "배치 정리", "progress": 0}},
		},
	}))
	if count, _ := appended["itemCount"].(float64); count != 3 {
		t.Errorf("append left %v items, want 3", appended["itemCount"])
	}
	version = appended["version"].(float64)

	// Replace with one carrying its id: the identity survives, the rest go.
	replaced := mcpData(t, mcpAs(server, key, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{
			"reportId": id, "version": version,
			"items": []map[string]any{{"id": firstID, "title": "인증 연동", "category": "개발", "currentResult": "구현 완료", "progress": 80}},
		},
	}))
	if count, _ := replaced["itemCount"].(float64); count != 1 {
		t.Errorf("replace left %v items, want 1", replaced["itemCount"])
	}
	after := mcpData(t, mcpAs(server, key, map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"reportId": id},
	}))
	afterItems, _ := after["items"].([]any)
	if len(afterItems) != 1 || afterItems[0].(map[string]any)["id"].(float64) != firstID {
		t.Errorf("the kept item lost its identity: %v", afterItems)
	}
	if after["summary"] != "요약만 고침" {
		t.Errorf("an items-only update changed the summary to %v", after["summary"])
	}

	// A draft edited stays a draft and says nothing about it; a submitted
	// report edited goes back to draft, and the answer says so — the person
	// has to hand it in again, and a model that is not told will not tell them.
	if _, noted := replaced["note"]; noted {
		t.Errorf("editing a draft claimed a status change: %v", replaced["note"])
	}
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id), nil, writer); w.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	version = mcpData(t, mcpAs(server, key, map[string]any{
		"name": "weekly_report_detail", "arguments": map[string]any{"reportId": id},
	}))["version"].(float64)
	reopened := mcpData(t, mcpAs(server, key, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{"reportId": id, "version": version, "summary": "제출 뒤 고침"},
	}))
	if reopened["status"] != "DRAFT" {
		t.Errorf("editing a submitted report left it %v, want DRAFT", reopened["status"])
	}
	if note, _ := reopened["note"].(string); !strings.Contains(note, "작성 중") {
		t.Errorf("the answer does not say the report went back to draft: %v", reopened["note"])
	}
	version = reopened["version"].(float64)

	// A stale version is refused — the model must read before it writes, or
	// it overwrites a save it never saw.
	stale := mcpAs(server, key, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{
			"reportId": id, "version": version - 1, "summary": "옛 버전으로",
		},
	})
	if !stale.Result.IsError || !strings.Contains(mcpText(t, stale), "먼저 저장") {
		t.Errorf("a stale version was accepted or refused without saying why: %+v", stale.Result)
	}
	noID := mcpAs(server, key, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{"version": version, "summary": "id 없이"},
	})
	if !noID.Result.IsError || !strings.Contains(mcpText(t, noID), "reportId") {
		t.Errorf("an update without a report id was not told which argument is missing: %+v", noID.Result)
	}
	noVersion := mcpAs(server, key, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{"reportId": id, "summary": "버전 없이"},
	})
	if !noVersion.Result.IsError || !strings.Contains(mcpText(t, noVersion), "weekly_report_detail") {
		t.Errorf("an update without a version was not told to read first: %+v", noVersion.Result)
	}

	// The same week again is refused, and the refusal says what to do instead.
	again := mcpAs(server, key, map[string]any{
		"name": "weekly_report_create", "arguments": map[string]any{"weekStart": "2026-03-02", "summary": "또"},
	})
	if !again.Result.IsError || !strings.Contains(mcpText(t, again), "weekly_report_update") {
		t.Errorf("a second report for the same week was accepted or not redirected: %+v", again.Result)
	}
}

// guards: callMCPWriteTool, mcpWriteTools
func TestWritingThroughMCPNeedsTheWriteScopeAndOnlyReachesOnesOwnReport(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("쓰기 조직", "MCPWRITE")
	leader := server.createUser("mcpwlead", "TEAM_LEADER", &org)
	member := server.createUser("mcpwmate", "USER", &org)
	memberReport, _ := server.draft(member, "2026-03-02", "팀원 보고")

	reading := createKey(t, server, leader, "읽는 키", []string{"reports:read", "mcp:read"})
	writing := createKey(t, server, leader, "쓰는 키", []string{"reports:read", "mcp:read", "mcp:write"})

	// A key without the scope is not shown the tools, and is refused with the
	// scope named if it calls them anyway.
	listed := func(bearer string) map[string]bool {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+bearer)
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, request)
		var reply mcpReply
		_ = json.Unmarshal(recorder.Body.Bytes(), &reply)
		return mcpToolNames(reply)
	}
	if names := listed(reading); names["weekly_report_create"] || names["weekly_report_update"] {
		t.Error("a read-only key is offered the write tools")
	}
	if names := listed(writing); !names["weekly_report_create"] || !names["weekly_report_update"] {
		t.Error("a key with mcp:write is not offered the write tools")
	}
	refused := mcpAs(server, reading, map[string]any{
		"name": "weekly_report_create", "arguments": map[string]any{"summary": "몰래"},
	})
	if !refused.Result.IsError || !strings.Contains(mcpText(t, refused), "mcp:write") {
		t.Errorf("a read-only key wrote, or was refused without naming the scope: %+v", refused.Result)
	}

	// A leader may read a member's report and may not write it — the same
	// line the editor draws.
	theirs := mcpAs(server, writing, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{
			"reportId": memberReport, "version": 1, "summary": "남의 보고를 고침",
		},
	})
	if !theirs.Result.IsError || !strings.Contains(mcpText(t, theirs), "본인") {
		t.Errorf("a leader rewrote a member's report through MCP: %+v", theirs.Result)
	}

	// And a report the caller cannot even read is refused in the same words
	// as one that does not exist.
	stranger := server.createUser("mcpwouter", "USER", nil)
	strangerKey := createKey(t, server, stranger, "밖의 키", []string{"reports:read", "mcp:read", "mcp:write"})
	unseen := mcpAs(server, strangerKey, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{"reportId": memberReport, "version": 1, "summary": "x"},
	})
	missing := mcpAs(server, strangerKey, map[string]any{
		"name": "weekly_report_update", "arguments": map[string]any{"reportId": memberReport + 100000, "version": 1, "summary": "x"},
	})
	if !unseen.Result.IsError || mcpText(t, unseen) != mcpText(t, missing) {
		t.Errorf("an unreadable report is distinguishable from a missing one: %q vs %q", mcpText(t, unseen), mcpText(t, missing))
	}

	// The REST API stays read only for keys, whatever scope they carry.
	write := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(`{"weekStart":"2026-04-06","summary":"REST"}`))
	write.Header.Set("Content-Type", "application/json")
	write.Header.Set("Authorization", "Bearer "+writing)
	recorder := httptest.NewRecorder()
	server.app.mux.ServeHTTP(recorder, write)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("a key with mcp:write wrote through REST: %d %s", recorder.Code, recorder.Body.String())
	}
	_ = fmt.Sprint()
}
