package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// guards: login, authenticate
func TestHarnessCanSignInAndRead(t *testing.T) {
	server := newTestServer(t)
	me := server.request(http.MethodGet, "/api/v1/me", nil, server.admin)
	if me.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/me = %d %s", me.Code, me.Body.String())
	}
	user, _ := decodeData(t, me)["user"].(map[string]any)
	if user["username"] != server.adminName {
		t.Errorf("signed in as %v", user["username"])
	}
}

// Every rule below was true when read; none had ever been exercised through the
// route that enforces it.
// guards: getReport, updateReport, deleteReport, createReport
func TestOnePersonsReportIsNotAnothersToRead(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("harness_author", "USER", nil)
	stranger := server.createUser("harness_stranger", "USER", nil)

	created := server.request(http.MethodPost, "/api/v1/reports",
		map[string]any{"weekStart": "2026-08-24", "summary": "남이 보면 안 되는 요약"}, author)
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("create a report: %d %s", created.Code, created.Body.String())
	}
	id, ok := decodeData(t, created)["id"].(float64)
	if !ok {
		t.Fatalf("the created report has no id: %s", created.Body.String())
	}
	path := fmt.Sprintf("/api/v1/reports/%d", int64(id))

	if own := server.request(http.MethodGet, path, nil, author); own.Code != http.StatusOK {
		t.Fatalf("the author cannot read their own report: %d %s", own.Code, own.Body.String())
	}
	other := server.request(http.MethodGet, path, nil, stranger)
	if other.Code == http.StatusOK {
		t.Errorf("a stranger read somebody else's report: %s", other.Body.String())
	}
	if other.Code != http.StatusNotFound && other.Code != http.StatusForbidden {
		t.Errorf("reading somebody else's report answered %d, want 403 or 404", other.Code)
	}
	// Editing is the same boundary from the other side.
	edited := server.request(http.MethodPut, path, map[string]any{"summary": "고쳐 버림"}, stranger)
	if edited.Code == http.StatusOK {
		t.Errorf("a stranger edited somebody else's report: %s", edited.Body.String())
	}
	if removed := server.request(http.MethodDelete, path, nil, stranger); removed.Code == http.StatusOK {
		t.Errorf("a stranger deleted somebody else's report: %s", removed.Body.String())
	}
}

// guards: requireRole, requireAuth
func TestRoleAndSessionGatesAnswerBeforeTheHandlerDoes(t *testing.T) {
	server := newTestServer(t)
	plain := server.createUser("harness_plain", "USER", nil)

	// requireRole is an allow-list, not a hierarchy: a USER is not a lesser ADMIN.
	forbidden := server.request(http.MethodGet, "/api/v1/admin/users", nil, plain)
	if forbidden.Code != http.StatusForbidden {
		t.Errorf("a USER reached the administrator list: %d %s", forbidden.Code, forbidden.Body.String())
	}
	// No cookie at all is a sign-out, and says so rather than 403.
	anonymous := server.request(http.MethodGet, "/api/v1/reports", nil, nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated read answered %d, want 401", anonymous.Code)
	}
	if code := errorCode(anonymous); code == "" {
		t.Errorf("the 401 carries no error code: %s", anonymous.Body.String())
	}
}

// CSRF here is Origin-based, so a request from another site must be refused even
// though it carries a valid session cookie.
// guards: csrf=100
func TestAWriteFromAnotherOriginIsRefused(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("harness_origin", "USER", nil)

	foreign := httptest.NewRequest(http.MethodPost, "/api/v1/reports", nil)
	foreign.Header.Set("Origin", "http://attacker.example")
	foreign.AddCookie(author)
	w := httptest.NewRecorder()
	server.app.mux.ServeHTTP(w, foreign)
	if w.Code != http.StatusForbidden {
		t.Errorf("a write from another origin answered %d, want 403 — %s", w.Code, w.Body.String())
	}
	if code := errorCode(w); code != "CSRF_REJECTED" {
		t.Errorf("the refusal is coded %q, not CSRF_REJECTED", code)
	}

	// An API key is not a browser and carries no origin, so the check must not
	// apply to it — otherwise every integration breaks.
	bare := httptest.NewRequest(http.MethodPost, "/api/v1/reports", nil)
	bare.AddCookie(author)
	blank := httptest.NewRecorder()
	server.app.mux.ServeHTTP(blank, bare)
	if blank.Code != http.StatusForbidden {
		t.Errorf("a session write with no origin answered %d, want 403", blank.Code)
	}
}
