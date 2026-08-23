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
