package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This table is the policy a personal API key runs under, so it is written out
// rather than described: deny by default, read only, and only these paths.
//
// `reason` is checked as well as the verdict. Every refusal used to carry the
// same sentence, so a caller refused because no scope covers /api/v1/work-items
// could not tell that from a missing scope, and would try adding each of the
// three in turn to no effect.
func TestAPIKeyScopeEnforcement(t *testing.T) {
	p := &principal{AuthType: "api_key", Scopes: []string{"reports:read", "mcp:read"}}
	tests := []struct {
		method, path string
		want         bool
		reasonHas    string
	}{
		{http.MethodGet, "/api/v1/reports", true, ""},
		{http.MethodGet, "/api/v1/reports/1/export.pptx", true, ""},
		{http.MethodGet, "/api/v1/me", true, ""},
		// Writes are closed to keys whatever scopes they carry.
		{http.MethodPost, "/api/v1/reports/1/clone", false, "조회 전용"},
		{http.MethodPost, "/api/v1/reports/1/submit", false, "조회 전용"},
		{http.MethodDelete, "/api/v1/reports/1", false, "조회 전용"},
		// A scope this key does not hold: naming it is the actionable answer.
		{http.MethodGet, "/api/v1/analytics/overview", false, "analytics:read"},
		// No scope opens these at all, and the message has to say so rather
		// than imply a scope would fix it.
		{http.MethodGet, "/api/v1/admin/settings", false, "이 경로를 사용할 수 없습니다"},
		{http.MethodGet, "/api/v1/work-items", false, "이 경로를 사용할 수 없습니다"},
		{http.MethodGet, "/api/v1/handover", false, "이 경로를 사용할 수 없습니다"},
		{http.MethodGet, "/api/v1/team/members", false, "이 경로를 사용할 수 없습니다"},
		{http.MethodGet, "/api/v1/digest", false, "이 경로를 사용할 수 없습니다"},
		{http.MethodPost, "/mcp", true, ""},
	}
	for _, test := range tests {
		r := httptest.NewRequest(test.method, test.path, nil)
		got, reason := apiKeyRequestAllowed(p, r)
		if got != test.want {
			t.Errorf("%s %s: got %v, want %v", test.method, test.path, got, test.want)
			continue
		}
		if test.reasonHas != "" && !strings.Contains(reason, test.reasonHas) {
			t.Errorf("%s %s: reason %q does not say %q", test.method, test.path, reason, test.reasonHas)
		}
		if test.want && reason != "" {
			t.Errorf("%s %s: allowed but carries a reason %q", test.method, test.path, reason)
		}
	}
	// The scope that is missing is named, and the one that is held is not.
	_, reason := apiKeyRequestAllowed(p, httptest.NewRequest(http.MethodGet, "/api/v1/analytics/overview", nil))
	if strings.Contains(reason, "reports:read") {
		t.Errorf("the refusal names a scope the key already holds: %q", reason)
	}
}

func TestMCPOriginValidation(t *testing.T) {
	tests := []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"https://weekly.internal", true},
		{"http://weekly.internal", true},
		{"https://attacker.example", false},
		{"file:///tmp/test", false},
	}
	for _, test := range tests {
		r := httptest.NewRequest(http.MethodPost, "https://weekly.internal/mcp", nil)
		if test.origin != "" {
			r.Header.Set("Origin", test.origin)
		}
		if got := validMCPOrigin(r); got != test.want {
			t.Errorf("origin %q: got %v, want %v", test.origin, got, test.want)
		}
	}
}

// The endpoint analysis is what an administrator opens to ask whether the
// service is healthy. A call to an API path this build does not serve matches
// the catch-all that also serves the application shell, so both were recorded
// as "/". Measured on a deployment: GET / came back as 55 requests at 78%
// refused — twelve page loads and forty-three calls to a path that is not
// there. One row, two unrelated facts, and a percentage true of neither.

// guards: requestMetrics, isAPIPath
func TestAnUnknownAPIPathIsNotFiledUnderTheSiteRoot(t *testing.T) {
	server := newTestServer(t)
	// Through the whole chain, not the mux. The harness calls the mux directly,
	// so the metrics middleware — and the route name it decides — is not on the
	// path any other test takes.
	handler := server.app.Handler()
	send := func(path string, cookie *http.Cookie) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Origin", "http://"+r.Host)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}

	// The shell, which is the route genuinely called "/".
	if code := send("/", nil); code != http.StatusOK {
		t.Fatalf("the application shell did not load: %d", code)
	}
	// An API path this build does not serve.
	if code := send("/api/v1/there-is-no-such-thing", nil); code != http.StatusNotFound {
		t.Fatalf("an unknown API path answered %d, want 404", code)
	}
	// And a 404 from a route that does exist, which must keep its own pattern
	// rather than being swept in with the unknown ones.
	if code := send("/api/v1/reports/99999999", server.admin); code != http.StatusNotFound {
		t.Fatalf("a missing report answered %d, want 404", code)
	}

	counts := map[string]int64{}
	rows, err := server.app.db.Query(server.ctx(),
		`SELECT route, sum(request_count) FROM api_request_metrics GROUP BY route`)
	if err != nil {
		t.Fatalf("read the metrics: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var route string
		var count int64
		if err := rows.Scan(&route, &count); err != nil {
			t.Fatal(err)
		}
		counts[route] = count
	}

	if counts[unknownAPIRoute] == 0 {
		t.Errorf("the unknown API path was not recorded as one: %v", counts)
	}
	// The shell's own row must not carry it. Mixed together, the refusal rate
	// on "/" describes neither the site nor the caller.
	for route, count := range counts {
		if route == "/" && count != 1 {
			t.Errorf("the site root carries %d requests, want only the page load: %v", count, counts)
		}
	}
	// A 404 from a real route keeps its pattern, so an administrator can still
	// see which endpoint is being asked for things that are not there.
	if counts["GET /api/v1/reports/{id}"] == 0 {
		t.Errorf("a 404 from a route that exists lost its own name: %v", counts)
	}
}
