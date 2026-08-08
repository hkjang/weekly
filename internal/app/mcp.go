package app

import (
	"encoding/json"
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
			"description": "권한 범위 안에서 주차와 상태로 주간보고를 검색합니다.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"weekStart": map[string]any{"type": "string", "format": "date"},
				"status":    map[string]any{"type": "string", "enum": []string{"DRAFT", "SUBMITTED", "REVISION_REQUESTED", "APPROVED", "CLOSED"}},
			}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"reports": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			}, "required": []string{"reports"}},
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
		week := strings.TrimSpace(asString(params.Arguments["weekStart"]))
		if week == "" {
			week = currentWeekStart(time.Now().In(a.serviceLocation(r.Context())), a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format("2006-01-02")
		}
		data, err = a.analyticsOverviewContext(r.Context(), p, week)
	case "weekly_reports_search":
		var reports []reportListItem
		reports, err = a.mcpSearchReports(r, p, strings.TrimSpace(asString(params.Arguments["weekStart"])), strings.TrimSpace(asString(params.Arguments["status"])))
		data = map[string]any{"reports": reports}
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

func (a *App) mcpSearchReports(r *http.Request, p *principal, week, status string) ([]reportListItem, error) {
	query := `SELECT r.id,r.user_id,u.username,u.display_name,r.week_start,r.status,r.summary,r.version,r.submitted_at,r.updated_at FROM weekly_reports r JOIN users u ON u.id=r.user_id WHERE 1=1`
	args := []any{}
	if p.Role == "USER" {
		args = append(args, p.ID)
		query += " AND r.user_id=$1"
	} else if p.Role != "ADMIN" {
		if p.OrganizationID == nil {
			return []reportListItem{}, nil
		}
		args = append(args, *p.OrganizationID)
		query += ` AND u.organization_id IN (WITH RECURSIVE orgs AS (SELECT id FROM organizations WHERE id=$1 UNION ALL SELECT o.id FROM organizations o JOIN orgs x ON o.parent_id=x.id) SELECT id FROM orgs)`
	}
	if week != "" {
		args = append(args, week)
		query += " AND r.week_start=$" + asString(len(args))
	}
	if status != "" {
		args = append(args, status)
		query += " AND r.status=$" + asString(len(args))
	}
	query += " ORDER BY r.week_start DESC LIMIT 100"
	rows, err := a.db.Query(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []reportListItem{}
	for rows.Next() {
		var item reportListItem
		var weekDate time.Time
		if err := rows.Scan(&item.ID, &item.UserID, &item.Username, &item.DisplayName, &weekDate, &item.Status, &item.Summary, &item.Version, &item.SubmittedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.WeekStart = weekDate.Format("2006-01-02")
		result = append(result, item)
	}
	return result, rows.Err()
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
