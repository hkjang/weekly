package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const mcpProtocolVersion = "2025-11-25"

var supportedMCPVersions = map[string]bool{
	"2025-03-26": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (a *App) mcpGet(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", "POST")
	writeError(w, http.StatusMethodNotAllowed, "MCP_POST_REQUIRED", "이 MCP 서버는 Streamable HTTP POST 요청을 사용합니다.")
}

func (a *App) mcp(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	if !validMCPOrigin(r) {
		writeError(w, http.StatusForbidden, "MCP_ORIGIN_REJECTED", "MCP 요청 출처를 확인할 수 없습니다.")
		return
	}
	if version := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")); version != "" && !supportedMCPVersions[version] {
		writeError(w, http.StatusBadRequest, "MCP_VERSION_UNSUPPORTED", "지원하지 않는 MCP 프로토콜 버전입니다.")
		return
	}
	if p.AuthType == "api_key" && !contains(p.Scopes, "mcp:read") {
		writeError(w, 403, "MCP_SCOPE_REQUIRED", "mcp:read 범위가 필요합니다.")
		return
	}
	var request jsonRPCRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		a.writeRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &jsonRPCError{Code: -32600, Message: "Invalid Request"}})
		return
	}
	if len(request.ID) == 0 {
		// JSON-RPC notifications never receive a JSON-RPC response.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var response jsonRPCResponse
	response.JSONRPC = "2.0"
	response.ID = request.ID
	switch request.Method {
	case "initialize":
		negotiated := mcpProtocolVersion
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(request.Params, &params) == nil && supportedMCPVersions[params.ProtocolVersion] {
			negotiated = params.ProtocolVersion
		}
		response.Result = map[string]any{"protocolVersion": negotiated, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "Weekly Analytics MCP", "title": "Weekly 보고·서비스 분석", "version": a.build.Version}, "instructions": "주간보고 제출 현황, 보고서 검색, 서비스 API 상태를 읽기 전용으로 분석합니다."}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": a.mcpTools(p)}
	case "tools/call":
		response = a.callMCPTool(r, p, request)
	default:
		response.Error = &jsonRPCError{Code: -32601, Message: "Method not found"}
	}
	a.writeRPC(w, response)
}

func (a *App) writeRPC(w http.ResponseWriter, response jsonRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (a *App) mcpTools(p *principal) []map[string]any {
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	tools := []map[string]any{
		{
			"name":        "weekly_submission_overview",
			"title":       "주차별 제출 현황 분석",
			"description": "선택한 주차의 사용자 수, 제출률, 상태별 건수, 이슈 및 평균 진척도를 분석합니다.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"weekStart": map[string]any{"type": "string", "format": "date", "description": "YYYY-MM-DD 주차 시작일. 생략하면 현재 주차"},
			}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"weekStart": map[string]any{"type": "string"}, "totalUsers": map[string]any{"type": "integer"},
				"submittedUsers": map[string]any{"type": "integer"}, "submissionRate": map[string]any{"type": "number"},
				"statusCounts": map[string]any{"type": "object"}, "openIssues": map[string]any{"type": "integer"},
				"averageProgress": map[string]any{"type": "number"},
			}, "required": []string{"weekStart", "totalUsers", "submittedUsers", "submissionRate", "statusCounts", "openIssues", "averageProgress"}},
			"annotations": readOnly,
		},
		{
			"name": "weekly_reports_search", "title": "주간보고 검색",
			"description": "권한 범위 안에서 주차와 상태로 주간보고를 검색합니다. " +
				"한 번에 최대 100건을 반환하고 조건에 맞는 전체 건수를 total로 함께 알려 줍니다. " +
				"total이 반환 건수보다 크면 offset을 옮겨 나머지를 가져오세요.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"weekStart": map[string]any{"type": "string", "format": "date"},
				"status":    map[string]any{"type": "string", "enum": []string{"DRAFT", "SUBMITTED", "REVISION_REQUESTED", "APPROVED", "CLOSED"}},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 100, "description": "한 번에 가져올 건수"},
				"offset":    map[string]any{"type": "integer", "minimum": 0, "default": 0, "description": "건너뛸 건수"},
			}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"reports": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"total":   map[string]any{"type": "integer", "description": "조건에 맞는 전체 건수"},
				"limit":   map[string]any{"type": "integer"},
				"offset":  map[string]any{"type": "integer"},
				"note":    map[string]any{"type": "string", "description": "일부만 반환했을 때 그 사실을 적는다"},
			}, "required": []string{"reports", "total", "limit", "offset"}},
			"annotations": readOnly,
		},
		{
			"name": "period_report_rollup", "title": "월간·분기·반기·연간 보고 집계",
			"description": "주간보고를 기간 단위로 취합해 중복을 제거한 업무 목록과 완료율, 정체·이슈 지속 업무 같은 경영 인사이트를 반환합니다.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"kind":   map[string]any{"type": "string", "enum": []string{periodMonth, periodQuarter, periodHalf, periodYear}, "description": "집계 단위. 생략하면 MONTH"},
				"period": map[string]any{"type": "string", "description": "2026-08, 2026-Q3, 2026-H2, 2026 형식. 생략하면 현재 기간"},
				"scope":  map[string]any{"type": "string", "enum": []string{scopeSelf, scopeTeam}, "description": "SELF는 본인, TEAM은 소속 조직. 생략하면 SELF"},
			}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"period": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"}, "insights": map[string]any{"type": "object"},
				"highlights": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"items":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			}, "required": []string{"period", "label", "summary", "insights", "highlights", "items"}},
			"annotations": readOnly,
		},
	}
	if p.Role == "ADMIN" {
		tools = append(tools, map[string]any{"name": "weekly_endpoint_analysis", "title": "Weekly API 운영 분석", "description": "최근 24시간 API별 호출 수, 평균/최대 응답시간과 오류율을 분석합니다.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}, "outputSchema": map[string]any{"type": "object", "properties": map[string]any{"endpoints": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, "required": []string{"endpoints"}}, "annotations": readOnly})
	}
	return tools
}

func (a *App) callMCPTool(r *http.Request, p *principal, request jsonRPCRequest) jsonRPCResponse {
	response := jsonRPCResponse{JSONRPC: "2.0", ID: request.ID}
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		response.Error = &jsonRPCError{Code: -32602, Message: "Invalid params"}
		return response
	}
	var data any
	var err error
	switch params.Name {
	case "weekly_submission_overview":
		week := mcpArgumentString(params.Arguments, "weekStart")
		if week == "" {
			week = currentWeekStart(time.Now().In(a.serviceLocation(r.Context())), a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format("2006-01-02")
		}
		data, err = a.analyticsOverviewContext(r.Context(), p, week)
	case "weekly_reports_search":
		var page mcpReportPage
		page, err = a.mcpSearchReports(r, p,
			mcpArgumentString(params.Arguments, "weekStart"),
			mcpArgumentString(params.Arguments, "status"),
			mcpArgumentInt(params.Arguments, "limit", mcpReportPageMaximum, 1, mcpReportPageMaximum),
			mcpArgumentInt(params.Arguments, "offset", 0, 0, 1_000_000))
		data = map[string]any{"reports": page.Reports, "total": page.Total, "limit": page.Limit, "offset": page.Offset}
		// Spelled out as well as counted. The caller here is a language model
		// whose entire view of the data is this payload; a JSON field it might
		// not compare against len(reports) is a weaker signal than a sentence
		// saying the list is partial.
		if page.Offset+len(page.Reports) < page.Total {
			data.(map[string]any)["note"] = fmt.Sprintf(
				"조건에 맞는 %d건 중 %d번째부터 %d건만 반환했습니다. 나머지는 offset=%d으로 이어서 가져오세요. 이 목록을 전체로 보고 요약하지 마세요.",
				page.Total, page.Offset+1, len(page.Reports), page.Offset+len(page.Reports))
		}
	case "period_report_rollup":
		kind := strings.ToUpper(mcpArgumentString(params.Arguments, "kind"))
		if kind == "" {
			kind = periodMonth
		}
		scope := strings.ToUpper(mcpArgumentString(params.Arguments, "scope"))
		if scope == "" {
			scope = scopeSelf
		}
		if scope != scopeSelf && scope != scopeTeam {
			return a.mcpToolError(response, "조회 범위는 SELF 또는 TEAM이어야 합니다.")
		}
		if scope == scopeTeam && p.Role == "USER" {
			return a.mcpToolError(response, "조직 단위 집계는 팀장 이상만 조회할 수 있습니다.")
		}
		period, periodErr := resolvePeriod(kind, mcpArgumentString(params.Arguments, "period"), time.Now().In(a.serviceLocation(r.Context())))
		if periodErr != nil {
			return a.mcpToolError(response, "조회 기간이 올바르지 않습니다. 예: 2026-08, 2026-Q3, 2026-H2, 2026")
		}
		data, err = a.loadRollup(r.Context(), p, period, scope)
	case "weekly_endpoint_analysis":
		if p.Role != "ADMIN" {
			return a.mcpToolError(response, "관리자 권한이 필요합니다.")
		}
		var endpoints []map[string]any
		endpoints, err = a.endpointAnalytics(r.Context())
		data = map[string]any{"endpoints": endpoints}
	default:
		response.Error = &jsonRPCError{Code: -32602, Message: "Unknown tool"}
		return response
	}
	if err != nil {
		// The caller gets one sentence; whoever has to fix it needs the cause.
		// Swallowing this entirely meant an MCP tool failure looked identical
		// whether the database was down or the query was malformed.
		a.logger.Error("mcp tool", "tool", params.Name, "error", err, "trace", traceIDFromContext(r.Context()))
		return a.mcpToolError(response, "분석 중 오류가 발생했습니다.")
	}
	encoded, _ := json.MarshalIndent(data, "", "  ")
	response.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": string(encoded)}}, "structuredContent": data}
	return response
}

func (a *App) mcpToolError(response jsonRPCResponse, message string) jsonRPCResponse {
	response.Result = map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": message}}}
	return response
}

// mcpReportPageMaximum is the most reports one tool call returns. The cap is
// not the problem; returning it without the total was. An agent handed a
// hundred rows and no count reasons about them as though they were everything,
// and unlike a person it has no screen to scroll to find out otherwise.
const mcpReportPageMaximum = 100

type mcpReportPage struct {
	Reports []reportListItem
	Total   int
	Limit   int
	Offset  int
}

// mcpArgumentString reads an optional tool argument as text.
//
// asString goes through fmt.Sprint, which turns a missing argument into the
// four characters "<nil>" rather than an empty string. Every optional argument
// here is guarded with `if value == ""`, so a caller omitting one did not get
// the default — it got the literal "<nil>" passed down. weekly_reports_search
// called with no arguments at all sent "<nil>" to PostgreSQL as a date and came
// back as "분석 중 오류가 발생했습니다", which is what an agent saw whenever it
// asked the obvious first question: what reports are there?
func mcpArgumentString(arguments map[string]any, name string) string {
	value, present := arguments[name]
	if !present || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

// mcpArgumentInt reads a whole number out of tool arguments, which arrive as
// JSON and so may be float64, a string, or missing.
func mcpArgumentInt(arguments map[string]any, name string, fallback, low, high int) int {
	raw, present := arguments[name]
	if !present || raw == nil {
		return fallback
	}
	parsed := fallback
	switch value := raw.(type) {
	case float64:
		parsed = int(value)
	case int:
		parsed = value
	default:
		if _, err := fmt.Sscanf(strings.TrimSpace(asString(raw)), "%d", &parsed); err != nil {
			return fallback
		}
	}
	return min(max(parsed, low), high)
}

func (a *App) mcpSearchReports(r *http.Request, p *principal, week, status string, limit, offset int) (mcpReportPage, error) {
	page := mcpReportPage{Reports: []reportListItem{}, Limit: limit, Offset: offset}
	where := ""
	args := []any{}
	if p.Role == "USER" {
		args = append(args, p.ID)
		where += " AND r.user_id=$1"
	} else if p.Role != "ADMIN" {
		if p.OrganizationID == nil {
			return page, nil
		}
		args = append(args, *p.OrganizationID)
		where += ` AND u.organization_id IN (WITH RECURSIVE orgs AS (SELECT id FROM organizations WHERE id=$1 UNION ALL SELECT o.id FROM organizations o JOIN orgs x ON o.parent_id=x.id) SELECT id FROM orgs)`
	}
	if week != "" {
		args = append(args, week)
		where += " AND r.week_start=$" + asString(len(args))
	}
	if status != "" {
		args = append(args, status)
		where += " AND r.status=$" + asString(len(args))
	}
	if err := a.db.QueryRow(r.Context(),
		`SELECT count(*) FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE 1=1`+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	query := `SELECT r.id,r.user_id,u.username,u.display_name,r.week_start,r.status,r.source_type,r.summary,r.version,r.submitted_at,r.updated_at FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE 1=1` + where
	args = append(args, limit, offset)
	query += " ORDER BY r.week_start DESC,r.id LIMIT $" + asString(len(args)-1) + " OFFSET $" + asString(len(args))
	rows, err := a.db.Query(r.Context(), query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item reportListItem
		var weekDate time.Time
		if err := rows.Scan(&item.ID, &item.UserID, &item.Username, &item.DisplayName, &weekDate, &item.Status, &item.SourceType, &item.Summary, &item.Version, &item.SubmittedAt, &item.UpdatedAt); err != nil {
			return page, err
		}
		item.WeekStart = weekDate.Format("2006-01-02")
		page.Reports = append(page.Reports, item)
	}
	return page, rows.Err()
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validMCPOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host) && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
