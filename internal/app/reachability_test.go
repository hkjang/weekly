package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Endpoints the product answers but nothing ever asks.
//
// Three releases in a row found the same shape: a feature built completely,
// tested, documented — and structurally unreachable, because nothing on any
// screen called it. Work item deadlines sat unused until v0.60 asked the
// question a period report was already asking. Dependency links were declared
// zero times until v0.62 moved the declaration to where being blocked is
// written. Report comments had a permission check, a length limit and an audit
// entry, and the only way to leave one was to reject the report.
//
// None of those were found by reading the code. They were found by measuring a
// running deployment and noticing a table with no rows. This does it the cheap
// way instead: every route the server registers should be reachable from
// somewhere, and a new one that nothing calls is a feature nobody can use.
//
// Exceptions are listed with a reason rather than filtered by a pattern,
// because "probably fine" is how the last three got in.
func TestEveryRouteIsReachable(t *testing.T) {
	// Routes with no browser caller, and why that is correct.
	allowed := map[string]string{
		"GET /api/v1/version":                         "외부 호출자와 MCP를 위한 것. 화면은 세션의 build 정보를 쓴다.",
		"GET /api/v1/auth/oidc/callback":              "브라우저 리디렉션 목적지. fetch 로 부르는 것이 아니다.",
		"GET /api/v1/auth/oidc/start":                 "같은 이유. 링크로 이동한다.",
		"POST /mcp":                                   "MCP 클라이언트 전용.",
		"GET /api/v1/report-candidates/{id}/sources":  "후보 목록 응답이 sources 를 이미 담아 화면은 다시 묻지 않는다. 개별 조회는 API 사용자를 위한 것.",
		"GET /api/v1/admin/confluence/users/unmapped": "users/mappings 가 이미 전체 활성 사용자를 LEFT JOIN 으로 담고 매핑 제안까지 준다. 이것은 그 필터판이며 자동화 호출자를 위한 것.",
		"POST /api/v1/reports/{id}/approve":           "팀 화면이 ${id}/${action} 으로 경로를 조립해 approve 라는 조각이 소스에 그대로 나타나지 않는다.",
		"POST /api/v1/reports/{id}/reject":            "위와 같다.",
	}

	routes, err := os.ReadFile("app.go")
	if err != nil {
		t.Skipf("app.go is not readable from here: %v", err)
	}
	frontend, err := readFrontendSources("../../frontend/src")
	if err != nil {
		t.Skipf("frontend sources are not readable from here: %v", err)
	}

	missing := []string{}
	for _, line := range strings.Split(string(routes), "\n") {
		route, ok := registeredRoute(line)
		if !ok {
			continue
		}
		if _, excused := allowed[route]; excused {
			continue
		}
		if !frontendCalls(frontend, route) {
			missing = append(missing, route)
		}
	}
	if len(missing) > 0 {
		t.Errorf("화면에서 부르지 않는 경로 %d건: %s\n"+
			"쓸 수 없는 기능이거나, 이유를 적어 이 테스트의 allowed 에 넣어야 합니다.",
			len(missing), strings.Join(missing, ", "))
	}
}

// registeredRoute pulls "METHOD /path" out of a mux.Handle line.
func registeredRoute(line string) (string, bool) {
	start := strings.Index(line, `"`)
	if !strings.Contains(line, "a.mux.Handle(") || start < 0 {
		return "", false
	}
	rest := line[start+1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", false
	}
	route := rest[:end]
	if !strings.Contains(route, " /") {
		return "", false
	}
	return route, true
}

// frontendCalls looks for the literal parts of a path in the browser sources.
//
// The fixed prefix before the first parameter, and the segment after the last
// one written as a path segment — `/comments`, not `comments`. The looser
// spelling was tried first and made the guard unable to fail: the CSS class
// `"comments"` satisfied it, so removing the only call to the endpoint still
// passed. A watchdog that cannot bark is worse than none, because it is trusted.
//
// A route whose trailing segment is interpolated rather than written out —
// `${id}/${action}` — cannot be found this way and is listed as an exception
// with its reason instead.
func frontendCalls(sources, route string) bool {
	path := route[strings.Index(route, "/"):]
	prefix := strings.TrimRight(strings.Split(path, "{")[0], "/")
	if !strings.Contains(sources, prefix) {
		return false
	}
	if !strings.Contains(path, "}") {
		return true
	}
	suffix := strings.Trim(path[strings.LastIndex(path, "}")+1:], "/")
	if suffix == "" {
		return true
	}
	return strings.Contains(sources, "/"+suffix)
}

func readFrontendSources(root string) (string, error) {
	var builder strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if extension := filepath.Ext(path); extension != ".ts" && extension != ".tsx" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		builder.Write(body)
		return nil
	})
	return builder.String(), err
}

// Fields the server puts in a response that no screen ever reads.
//
// The mirror of the route check above, and it catches a different failure: an
// endpoint that is called, whose answer is partly thrown away. work_items
// carried a deadline for years that nothing displayed. The merge and split
// endpoints answered with how many weekly snapshots actually changed hands —
// the server's own comment calls it the only number that tells the author
// whether the correction did what they meant — and the screen announced the
// count that had been requested instead, so a partial move read as a complete
// one.
//
// Wire formats belonging to somebody else are excluded by file: what Confluence
// sends us, what an OpenAI-compatible endpoint expects, the JSON-RPC envelope
// and the OIDC discovery document are not this product's responses and no
// screen should be reading them.
func TestEveryResponseFieldIsRead(t *testing.T) {
	foreign := map[string]string{
		"confluence_client.go":   "Confluence 서버가 보내는 전문",
		"ai.go":                  "OpenAI 호환 엔드포인트의 요청·응답 형식",
		"mcp.go":                 "JSON-RPC 봉투와 MCP 프로토콜",
		"confluence_analysis.go": "AI 구조화 출력 스키마",
	}
	// Ours, read by something other than a screen, with the reason.
	allowed := map[string]string{
		"authorization_endpoint": "OIDC 디스커버리 문서를 그대로 되비추는 값. 관리자 연결 시험이 서버에서 확인한다.",
		"token_endpoint":         "위와 같다.",
	}

	sources, err := os.ReadDir("../app")
	if err != nil {
		t.Skipf("go sources are not readable from here: %v", err)
	}
	frontend, err := readFrontendSources("../../frontend/src")
	if err != nil {
		t.Skipf("frontend sources are not readable from here: %v", err)
	}

	tag := regexp.MustCompile("`json:\"([A-Za-z0-9_]+)[,\"]")
	unread := []string{}
	for _, entry := range sources {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, isForeign := foreign[name]; isForeign {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join("../app", name))
		if readErr != nil {
			continue
		}
		for _, match := range tag.FindAllStringSubmatch(string(body), -1) {
			field := match[1]
			if _, excused := allowed[field]; excused {
				continue
			}
			if !strings.Contains(frontend, field) {
				unread = append(unread, field+" ("+name+")")
			}
		}
	}
	if len(unread) > 0 {
		t.Errorf("응답에 실려 나가지만 화면이 이름조차 언급하지 않는 필드 %d건: %s\n"+
			"버려지는 답이거나, 이유를 적어 allowed 에 넣어야 합니다.",
			len(unread), strings.Join(unread, ", "))
	}
}
