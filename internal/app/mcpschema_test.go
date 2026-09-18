package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// 도구가 자기 스키마대로 답하는가.
//
// An MCP client hands outputSchema to the model as the shape of what a tool
// returns, and some clients validate structuredContent against it. Measured
// by calling every tool and comparing: four tools were sending fields their
// own schema never mentioned — itemsTotal, contributorsTotal, note, version,
// today — which are precisely the fields that say a list was cut, that a
// match was approximate, or that a save needs a version. A field the schema
// hides is a field the model has to discover by accident.
//
// So: every key in structuredContent is declared, every required key is
// present, and declared types hold. Run against every tool the listing offers,
// with the same default arguments the listing sweep uses.
//
// guards: mcpTools
func TestEveryToolAnswersWithinItsOwnSchema(t *testing.T) {
	server := newTestServer(t)
	org := server.createOrganization("스키마 조직", "MCPSCHEMA")
	leader := server.createUser("mcpschemalead", "TEAM_LEADER", &org)
	reportID, version := server.draft(leader, "2026-03-02", "스키마 점검 보고")
	fillInclusionTestReport(t, server, leader, reportID, version, "스키마 점검 보고", "스키마 점검 업무")
	// The caller below is the administrator, and the update tool writes only
	// the caller's own report.
	ownID, ownVersion := server.draft(server.admin, "2026-03-09", "관리자 자신의 보고")

	w := server.request(http.MethodPost, "/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	}, server.admin)
	var listing struct {
		Result struct {
			Tools []struct {
				Name         string         `json:"name"`
				OutputSchema map[string]any `json:"outputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil || len(listing.Result.Tools) == 0 {
		t.Fatalf("tools/list: %s", w.Body.String())
	}

	// The two tools with a required argument, and the write tools, which need
	// a report to act on. Everything else answers with no arguments.
	arguments := func(name string) map[string]any {
		switch name {
		case "weekly_report_detail":
			return map[string]any{"reportId": reportID}
		case "weekly_reports_text_search":
			return map[string]any{"q": "스키마 점검"}
		case "weekly_report_create":
			return map[string]any{"weekStart": "2026-02-02", "summary": "스키마 생성", "items": []map[string]any{{"title": "생성 업무", "progress": 10}}}
		case "weekly_report_update":
			return map[string]any{"reportId": ownID, "version": ownVersion, "summary": "스키마 수정"}
		}
		return map[string]any{}
	}

	for _, tool := range listing.Result.Tools {
		reply := server.request(http.MethodPost, "/mcp", map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool.Name, "arguments": arguments(tool.Name)},
		}, server.admin)
		var out struct {
			Result struct {
				IsError           bool `json:"isError"`
				Content           []struct{ Text string }
				StructuredContent map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(reply.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: decode %s: %v", tool.Name, reply.Body.String(), err)
		}
		if out.Result.IsError {
			text := ""
			if len(out.Result.Content) > 0 {
				text = out.Result.Content[0].Text
			}
			t.Errorf("%s refused its default call: %s", tool.Name, text)
			continue
		}
		properties, _ := tool.OutputSchema["properties"].(map[string]any)
		required, _ := tool.OutputSchema["required"].([]any)
		for _, key := range required {
			if _, present := out.Result.StructuredContent[key.(string)]; !present {
				t.Errorf("%s: required field %q is missing from the answer", tool.Name, key)
			}
		}
		undeclared := []string{}
		for key := range out.Result.StructuredContent {
			if _, declared := properties[key]; !declared {
				undeclared = append(undeclared, key)
			}
		}
		sort.Strings(undeclared)
		if len(undeclared) > 0 {
			t.Errorf("%s answers with fields its schema never declares: %s", tool.Name, strings.Join(undeclared, ", "))
		}
		for key, raw := range properties {
			spec, _ := raw.(map[string]any)
			value, present := out.Result.StructuredContent[key]
			if !present || value == nil {
				continue
			}
			if want := schemaTypeNames(spec["type"]); len(want) > 0 && !want[jsonTypeOf(value)] {
				t.Errorf("%s: %s is declared %v and answered as %s", tool.Name, key, keys(want), jsonTypeOf(value))
			}
		}
	}
}

func schemaTypeNames(raw any) map[string]bool {
	names := map[string]bool{}
	switch value := raw.(type) {
	case string:
		names[value] = true
	case []any:
		for _, item := range value {
			if name, ok := item.(string); ok {
				names[name] = true
			}
		}
	}
	// integer and number are both JSON numbers.
	if names["integer"] {
		names["number"] = true
	}
	return names
}

func jsonTypeOf(value any) string {
	switch value.(type) {
	case float64:
		return "number"
	case string:
		return "string"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", value)
}

func keys(set map[string]bool) []string {
	out := []string{}
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
