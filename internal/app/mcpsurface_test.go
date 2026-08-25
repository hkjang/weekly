package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// MCP is the door an agent comes through, and it reaches the same data the
// screens do. Until now only argument parsing was tested: the role rules, the
// scope of what a tool returns, and which tools a caller is even offered were
// verified by hand once and then guarded by nothing.

type mcpTool struct {
	Name string `json:"name"`
}

type mcpReply struct {
	Result struct {
		Tools   []mcpTool `json:"tools"`
		IsError bool      `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func mcpCall(t *testing.T, server *testServer, cookie *http.Cookie, method string, params any) mcpReply {
	t.Helper()
	w := server.request(http.MethodPost, "/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	}, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
	}
	var reply mcpReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return reply
}

func mcpToolNames(reply mcpReply) map[string]bool {
	names := map[string]bool{}
	for _, tool := range reply.Result.Tools {
		names[tool.Name] = true
	}
	return names
}

// guards: mcp
func TestMCPOffersOnlyTheToolsTheCallerCanUse(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("MCP 조직", "MCPORG")
	leader := server.createUser("mcplead", "TEAM_LEADER", &organisation)

	admin := mcpToolNames(mcpCall(t, server, server.admin, "tools/list", map[string]any{}))
	if !admin["weekly_endpoint_analysis"] {
		t.Fatal("an administrator is not offered the endpoint analysis tool")
	}

	// Listing a tool the caller cannot use is worse than refusing it: an agent
	// reads the list as what it may do, and spends a turn finding out otherwise.
	offered := mcpToolNames(mcpCall(t, server, leader, "tools/list", map[string]any{}))
	if offered["weekly_endpoint_analysis"] {
		t.Fatal("a team leader is offered a tool only an administrator may call")
	}
	if len(offered) == 0 {
		t.Fatal("a team leader is offered no tools at all")
	}
	for name := range offered {
		if !admin[name] {
			t.Fatalf("a team leader is offered %q, which an administrator is not", name)
		}
	}
}

// guards: mcp
func TestMCPRefusesAnAdministratorToolAndSaysWhy(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("MCP 조직", "MCPORG")
	leader := server.createUser("mcplead", "TEAM_LEADER", &organisation)

	reply := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_endpoint_analysis", "arguments": map[string]any{},
	})
	if !reply.Result.IsError {
		t.Fatalf("a team leader read the service's API metrics: %+v", reply.Result)
	}
	if len(reply.Result.Content) == 0 || reply.Result.Content[0].Text == "" {
		t.Fatal("the refusal carries no message, so the caller cannot tell what went wrong")
	}
}

// guards: mcp
func TestMCPAnswersWithinTheCallersOwnOrganisation(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("내 조직", "MCPMINE")
	theirs := server.createOrganization("남의 조직", "MCPTHEIRS")
	leader := server.createUser("mcplead", "TEAM_LEADER", &mine)
	server.createUser("mcpmate", "USER", &mine)
	server.createUser("mcpouter", "USER", &theirs)
	server.createUser("mcpouter2", "USER", &theirs)

	read := func(cookie *http.Cookie) float64 {
		reply := mcpCall(t, server, cookie, "tools/call", map[string]any{
			"name": "weekly_submission_overview", "arguments": map[string]any{},
		})
		if reply.Result.IsError || len(reply.Result.Content) == 0 {
			t.Fatalf("submission overview failed: %+v", reply)
		}
		var payload struct {
			TotalUsers float64 `json:"totalUsers"`
		}
		if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &payload); err != nil {
			t.Fatalf("decode %s: %v", reply.Result.Content[0].Text, err)
		}
		return payload.TotalUsers
	}

	everybody := read(server.admin)
	mineOnly := read(leader)
	if mineOnly >= everybody {
		t.Fatalf("a team leader counted %v people and the whole service has %v", mineOnly, everybody)
	}
	if mineOnly < 2 {
		t.Fatalf("the leader's own organisation has two accounts, the tool counted %v", mineOnly)
	}
}

// guards: mcpSearchReports
func TestMCPSearchReturnsOnlyTheCallersOwnOrganisation(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("검색 내 조직", "MCPSMINE")
	theirs := server.createOrganization("검색 남의 조직", "MCPSTHEIRS")
	leader := server.createUser("mcpsearchlead", "TEAM_LEADER", &mine)
	mate := server.createUser("mcpsearchmate", "USER", &mine)
	stranger := server.createUser("mcpsearchouter", "USER", &theirs)

	server.draft(mate, "2026-03-02", "같은 조직 사람이 쓴 보고입니다")
	server.draft(stranger, "2026-03-02", "다른 조직 사람이 쓴 보고입니다")

	// A search that quietly reaches past the caller's organisation is the same
	// leak as the screens would be, through a door nothing was watching.
	reply := mcpCall(t, server, leader, "tools/call", map[string]any{
		"name": "weekly_reports_search", "arguments": map[string]any{"week": "2026-03-02", "limit": 100},
	})
	if reply.Result.IsError || len(reply.Result.Content) == 0 {
		t.Fatalf("search failed: %+v", reply)
	}
	body := reply.Result.Content[0].Text
	var payload struct {
		Reports []struct {
			Summary string `json:"summary"`
		} `json:"reports"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(payload.Reports) == 0 {
		t.Fatal("the leader's own organisation wrote a report and the search found nothing")
	}
	for _, report := range payload.Reports {
		if report.Summary == "다른 조직 사람이 쓴 보고입니다" {
			t.Fatalf("the search reached into another organisation: %s", body)
		}
	}
}
