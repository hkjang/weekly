package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIKeyScopeEnforcement(t *testing.T) {
	p := &principal{AuthType: "api_key", Scopes: []string{"reports:read", "mcp:read"}}
	tests := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/v1/reports", true},
		{http.MethodGet, "/api/v1/reports/1/export.pptx", true},
		{http.MethodPost, "/api/v1/reports/1/submit", false},
		{http.MethodGet, "/api/v1/admin/settings", false},
		{http.MethodGet, "/api/v1/analytics/overview", false},
		{http.MethodPost, "/mcp", true},
	}
	for _, test := range tests {
		r := httptest.NewRequest(test.method, test.path, nil)
		if got := apiKeyRequestAllowed(p, r); got != test.want {
			t.Errorf("%s %s: got %v, want %v", test.method, test.path, got, test.want)
		}
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
