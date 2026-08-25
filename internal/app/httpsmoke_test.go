package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// Editing is the same boundary from the other side — and it has to be asked
	// properly to be asked at all. Without a version this request is refused as
	// 400 VERSION_REQUIRED before ownership is ever consulted, so the assertion
	// below used to hold no matter who was allowed to edit what. Measured: with
	// the version present the answer is 403 본인의 보고서만 수정할 수 있습니다.
	version, _ := decodeData(t, created)["version"].(float64)
	if version == 0 {
		version = 1
	}
	edited := server.request(http.MethodPut, path,
		map[string]any{"summary": "고쳐 버림", "version": int(version), "items": []any{}}, stranger)
	if edited.Code == http.StatusOK {
		t.Errorf("a stranger edited somebody else's report: %s", edited.Body.String())
	}
	if edited.Code != http.StatusNotFound && edited.Code != http.StatusForbidden {
		t.Errorf("editing somebody else's report answered %d, want 403 or 404", edited.Code)
	}
	removed := server.request(http.MethodDelete, fmt.Sprintf("%s?version=%d", path, int(version)), nil, stranger)
	if removed.Code == http.StatusOK {
		t.Errorf("a stranger deleted somebody else's report: %s", removed.Body.String())
	}
	if removed.Code != http.StatusNotFound && removed.Code != http.StatusForbidden {
		t.Errorf("deleting somebody else's report answered %d, want 403 or 404", removed.Code)
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

	// A session with no origin at all is refused for the same reason: the check
	// is on the header, not on its absence being harmless.
	bare := httptest.NewRequest(http.MethodPost, "/api/v1/reports", nil)
	bare.AddCookie(author)
	blank := httptest.NewRecorder()
	server.app.mux.ServeHTTP(blank, bare)
	if blank.Code != http.StatusForbidden {
		t.Errorf("a session write with no origin answered %d, want 403", blank.Code)
	}

	// This test used to carry a comment saying an API key must be exempt from
	// the origin check "otherwise every integration breaks". Checking it showed
	// the comment was wrong in a way worth recording: a personal API key cannot
	// write at all, so widening the CSRF condition could not break an
	// integration — there are none to break. The condition is defence in depth,
	// like the USER branch inside canReviewReport.
	//
	// The rule that does hold, and had nothing watching it, is this one.
	created := server.request(http.MethodPost, "/api/v1/keys",
		map[string]any{"name": "조회 전용 연동", "expiresInDays": 7}, author)
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("create a key: %d %s", created.Code, created.Body.String())
	}
	token, _ := decodeData(t, created)["token"].(string)
	if token == "" {
		t.Fatalf("the key came back without a token: %s", created.Body.String())
	}

	// Positive control: the key reads.
	if read := server.bearer(http.MethodGet, "/api/v1/reports", token); read.Code != http.StatusOK {
		t.Fatalf("the key cannot even read: %d %s", read.Code, read.Body.String())
	}

	// Every write shape is refused, with or without an origin, and always with
	// the code that names the reason rather than one that looks like a bug.
	for _, item := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/reports"},
		{http.MethodPut, "/api/v1/reports/1"},
		{http.MethodDelete, "/api/v1/reports/1"},
		{http.MethodPost, "/api/v1/admin/settings"},
	} {
		write := httptest.NewRequest(item.method, item.path, strings.NewReader(`{}`))
		write.Header.Set("Content-Type", "application/json")
		write.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		server.app.mux.ServeHTTP(recorder, write)
		if recorder.Code == http.StatusOK || recorder.Code == http.StatusCreated || recorder.Code == http.StatusNoContent {
			t.Errorf("%s %s: a read-only key wrote — %s", item.method, item.path, recorder.Body.String())
			continue
		}
		if recorder.Code == http.StatusForbidden && errorCode(recorder) != "API_KEY_SCOPE_DENIED" {
			t.Errorf("%s %s: refused as %q, which does not tell the integrator that keys are read-only",
				item.method, item.path, errorCode(recorder))
		}
	}
}
