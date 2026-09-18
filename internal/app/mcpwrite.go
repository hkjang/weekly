package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 주간보고를 MCP 로 쓰기 — 만들고, 고치고, 읽은 대로.
//
// The surface was read only from the day it existed, and the personal key is
// still read only on the REST API. What changes here is narrower than it
// sounds: two tools, each writing the caller's *own* report through the very
// function the editor uses (createReportFor, updateReportFor), so ownership,
// the version check, the status rule, the item reconciliation and the audit
// row are the same at both doors. A model cannot do anything through these
// that the person could not do in the editor — and it cannot do it to anybody
// else's report, whatever it asks for.
//
// The write is opened by a scope of its own, mcp:write, which a key is not
// issued with unless the person ticks it and an SSO token does not carry
// unless the administrator lists it. Listing a tool the caller cannot call is
// worse than not having it, so the two tools appear only for callers who may.

// mcpMayWrite is who sees and may call the write tools: a browser session,
// which can write already, or a bearer that was given the scope.
func mcpMayWrite(p *principal) bool {
	return p.AuthType == "session" || contains(p.Scopes, mcpWriteScope)
}

// mcpItemsArgument reads the items array the way the editor sends it. It is
// decoded through the same struct so a field the editor knows is a field a
// model may send, and nothing else is.
func mcpItemsArgument(arguments map[string]any) ([]reportItem, bool, error) {
	raw, present := arguments["items"]
	if !present || raw == nil {
		return nil, false, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, true, mcpRefuse("items 를 읽을 수 없습니다.")
	}
	var items []reportItem
	if err := json.Unmarshal(encoded, &items); err != nil {
		return nil, true, mcpRefuse("items 는 {title, category, currentResult, nextPlan, issue, managementAsk, progress} 객체의 배열이어야 합니다.")
	}
	return items, true, nil
}

// mcpCreateReport is 이번 주(또는 지정한 주) 보고서를 새로 만들기.
func (a *App) mcpCreateReport(r *http.Request, p *principal, arguments map[string]any) (map[string]any, error) {
	ctx := r.Context()
	week, err := mcpDateArgument(arguments, "weekStart")
	if err != nil {
		return nil, mcpRefusal{message: err.Error()}
	}
	items, _, err := mcpItemsArgument(arguments)
	if err != nil {
		return nil, err
	}
	input := reportCreateInput{Summary: mcpArgumentString(arguments, "summary"), Items: items}
	// Snapped to the grid rather than trusted, the way the week rollup treats
	// its period: a caller naming a Wednesday means the week it is in, and a
	// report filed under a Wednesday would be one the screens never find.
	grid := a.setting(ctx, "workflow.week_start", "MONDAY")
	if week != "" {
		parsed, _ := time.ParseInLocation(dateLayout, week, a.serviceLocation(ctx))
		input.WeekStart = currentWeekStart(parsed, grid).Format(dateLayout)
	} else {
		input.WeekStart = currentWeekStart(time.Now().In(a.serviceLocation(ctx)), grid).Format(dateLayout)
	}
	id, failure := a.createReportFor(r, p, input)
	if failure != nil {
		if failure.Status >= 500 {
			return nil, failure
		}
		message := failure.Message
		// A conflict means the week is already written — whichever check said
		// so — and the useful next step is the other tool.
		if failure.Status == http.StatusConflict {
			message += " weekly_reports_search 로 그 보고서의 id 를 찾아 weekly_report_update 로 고치세요."
		}
		return nil, mcpRefuse("%s", message)
	}
	return map[string]any{
		"id": id, "weekStart": input.WeekStart, "version": 1, "status": "DRAFT",
		"itemCount": len(items),
		"note":      "작성 중(DRAFT) 상태로 만들었습니다. 제출은 사람이 화면에서 합니다. 고치려면 weekly_report_detail 로 version 을 읽고 weekly_report_update 에 넣으세요.",
	}, nil
}

// mcpUpdateReport is 보고서 고치기 — 요약만, 항목만, 또는 둘 다.
//
// The editor's PUT replaces the whole report, and a model that sends two
// items would wipe the other eight without knowing it. So an argument left
// out means "leave it as it is": the current summary and items are read first
// and only what was sent changes. items replaces the list unless mode is
// append, and an item carrying the id weekly_report_detail gave keeps its
// identity — and with it the link to the tracked work item.
func (a *App) mcpUpdateReport(r *http.Request, p *principal, arguments map[string]any) (map[string]any, error) {
	ctx := r.Context()
	id := int64(mcpArgumentInt(arguments, "reportId", 0, 0, 1<<53))
	version := mcpArgumentInt(arguments, "version", 0, 0, 1<<31)
	if id <= 0 {
		return nil, mcpRefuse("reportId 는 weekly_reports_search 나 weekly_report_detail 이 돌려준 보고서 id 여야 합니다.")
	}
	if version <= 0 {
		return nil, mcpRefuse("version 이 필요합니다. 먼저 weekly_report_detail 로 읽어 그 version 을 그대로 넣으세요. 다른 곳에서 먼저 저장되면 거부되어 남의 수정을 덮어쓰지 않습니다.")
	}
	mode := strings.ToUpper(mcpArgumentString(arguments, "mode"))
	if mode == "" {
		mode = "REPLACE"
	}
	if mode != "REPLACE" && mode != "APPEND" {
		return nil, mcpRefuse("mode 는 REPLACE(보낸 items 로 전부 갈아 끼움) 또는 APPEND(기존 items 뒤에 더함)여야 합니다.")
	}
	items, itemsSent, err := mcpItemsArgument(arguments)
	if err != nil {
		return nil, err
	}
	_, summarySent := arguments["summary"]

	// What is there now — read only if the caller may. The same rule as the
	// detail tool, and a report the caller may not read is one they may not
	// write either.
	if !a.canViewReport(ctx, p, id) {
		return nil, errMCPReportUnreachable
	}
	current, loadErr := a.loadReport(ctx, id)
	if loadErr != nil {
		return nil, loadErr
	}
	if current.UserID != p.ID {
		return nil, mcpRefuse("본인의 보고서만 수정할 수 있습니다.")
	}
	input := reportUpdateInput{Version: version, Summary: current.Summary, Items: current.Items}
	if summarySent {
		input.Summary = mcpArgumentString(arguments, "summary")
	}
	if itemsSent {
		if mode == "APPEND" {
			input.Items = append(append([]reportItem{}, current.Items...), items...)
		} else {
			input.Items = items
		}
	}
	if !summarySent && !itemsSent {
		return nil, mcpRefuse("바꿀 것이 없습니다. summary 나 items 가운데 하나는 보내세요.")
	}
	result, failure := a.updateReportFor(r, p, id, input)
	if failure != nil {
		if failure.Status >= 500 {
			return nil, failure
		}
		return nil, mcpRefuse("%s", failure.Message)
	}
	data := map[string]any{
		"id": result.ID, "version": result.Version, "status": result.Status,
		"itemCount": len(input.Items),
	}
	if result.Status == "DRAFT" && current.Status != "DRAFT" {
		data["note"] = fmt.Sprintf("제출된 보고서(%s)를 고쳤으므로 작성 중(DRAFT)으로 돌아갔습니다. 다시 제출은 사람이 화면에서 합니다.", current.Status)
	}
	return data, nil
}

// mcpWriteTools is the listing half, shown only to callers who may write.
func mcpWriteTools(p *principal) []map[string]any {
	if !mcpMayWrite(p) {
		return nil
	}
	item := map[string]any{"type": "object", "properties": map[string]any{
		"id":            map[string]any{"type": "integer", "description": "기존 항목을 유지·수정하려면 weekly_report_detail 이 준 id. 새 항목은 생략"},
		"title":         map[string]any{"type": "string", "maxLength": 240, "description": "업무명(필수)"},
		"category":      map[string]any{"type": "string", "maxLength": 80, "description": "구분. 예: 개발, 운영, 기획"},
		"currentResult": map[string]any{"type": "string", "description": "금주 실적"},
		"nextPlan":      map[string]any{"type": "string", "description": "차주 계획"},
		"issue":         map[string]any{"type": "string", "description": "이슈. 없으면 빈 문자열"},
		"managementAsk": map[string]any{"type": "string", "description": "상위 조직에 요청할 것"},
		"progress":      map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
	}, "required": []string{"title"}}
	writes := map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false}
	return []map[string]any{
		{
			"name": "weekly_report_create", "title": "주간보고 새로 만들기",
			"description": "호출한 계정 본인의 주간보고를 작성 중(DRAFT) 상태로 만듭니다. weekStart 를 생략하면 이번 주, 주 중간 날짜를 주면 그 날이 든 주로 맞춥니다. " +
				"같은 주에 이미 보고서가 있으면 거부하고 weekly_report_update 를 안내합니다. 제출은 하지 않습니다 — 그것은 사람이 화면에서 합니다.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"weekStart": map[string]any{"type": "string", "format": "date", "description": "YYYY-MM-DD. 생략하면 이번 주"},
				"summary":   map[string]any{"type": "string", "description": "주간 요약"},
				"items":     map[string]any{"type": "array", "items": item, "maxItems": 100},
			}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"id": map[string]any{"type": "integer"}, "weekStart": map[string]any{"type": "string", "format": "date"},
				"version": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"},
				"itemCount": map[string]any{"type": "integer"}, "note": map[string]any{"type": "string"},
			}, "required": []string{"id", "weekStart", "version", "status"}},
			"annotations": writes,
		},
		{
			"name": "weekly_report_update", "title": "주간보고 고치기",
			"description": "본인 보고서의 요약과 업무 항목을 고칩니다. 먼저 weekly_report_detail 로 읽어 version 을 그대로 넣으세요 — 다른 곳에서 먼저 저장되면 거부됩니다. " +
				"보내지 않은 것은 그대로 둡니다: summary 만 보내면 항목은 남고, items 만 보내면 요약은 남습니다. " +
				"items 는 기본(REPLACE)으로 목록 전체를 갈아 끼우므로 남길 항목은 detail 이 준 id 와 함께 다시 보내고, 뒤에 더하기만 하려면 mode 를 APPEND 로 하세요. " +
				"제출·확정된 보고서를 고치면 작성 중으로 돌아갑니다.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"reportId": map[string]any{"type": "integer"},
				"version":  map[string]any{"type": "integer", "description": "weekly_report_detail 이 준 version"},
				"summary":  map[string]any{"type": "string"},
				"items":    map[string]any{"type": "array", "items": item, "maxItems": 100},
				"mode":     map[string]any{"type": "string", "enum": []string{"REPLACE", "APPEND"}, "description": "생략하면 REPLACE"},
			}, "required": []string{"reportId", "version"}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"id": map[string]any{"type": "integer"}, "version": map[string]any{"type": "integer"},
				"status": map[string]any{"type": "string"}, "itemCount": map[string]any{"type": "integer"},
				"note": map[string]any{"type": "string"},
			}, "required": []string{"id", "version", "status", "itemCount"}},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false},
		},
	}
}

// callMCPWriteTool answers the two write tools; handled says whether the name
// was one of them.
func (a *App) callMCPWriteTool(r *http.Request, p *principal, name string, arguments map[string]any) (any, error, bool) {
	switch name {
	case "weekly_report_create", "weekly_report_update":
	default:
		return nil, nil, false
	}
	if !mcpMayWrite(p) {
		return nil, mcpRefuse("이 연결로는 보고서를 쓸 수 없습니다. mcp:write 범위가 있는 개인 키를 발급하거나, 관리자가 SSO 토큰 범위에 mcp:write 를 더해야 합니다."), true
	}
	if name == "weekly_report_create" {
		data, err := a.mcpCreateReport(r, p, arguments)
		return data, err, true
	}
	data, err := a.mcpUpdateReport(r, p, arguments)
	return data, err, true
}
