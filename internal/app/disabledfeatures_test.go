package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An offline deployment starts with everything optional turned off: no AI
// gateway, no Confluence, no review workflow. That is the baseline this product
// ships into, and what it answers there is the first thing anybody sees.
//
// The contract says AI-backed endpoints answer 503 when the gateway is off. A
// 500 there reads as "this product is broken"; a 200 with an empty result reads
// as "there was nothing to find". Both are wrong in the same way — they hide a
// setting somebody can change.
//
// guards: parseAIText, suggestDecisions, uploadImportPPTX
func TestWithNoAIGatewayEveryAIPathSaysSoRatherThanFailing(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("off_ai", "USER", nil)

	// Configured and switched off, not merely absent. With nothing configured
	// at all every one of these fails for a second reason, and loosening the
	// enabled check would not show up here — which is the shape of a test that
	// cannot fail.
	if settings := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "false", "ai.endpoint": "http://ai.invalid/v1/chat/completions",
			"ai.model": "some-model", "ai.api_key": "k",
		},
	}, server.admin); settings.Code != http.StatusOK {
		t.Fatalf("configure a disabled gateway: %d %s", settings.Code, settings.Body.String())
	}

	parse := server.request(http.MethodPost, "/api/v1/ai/reports/parse-text",
		map[string]any{"text": "금주: 회선 이설 완료"}, author)
	assertUnavailable(t, "AI 본문 분석", parse)

	// A work item to hang the suggestion off. Without one the route answers 404
	// before it ever reaches the AI check, and the test would prove nothing.
	id := server.submitted(author, "2026-08-24", "결정 제안용")
	var workItemID int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT i.work_item_id FROM report_items i WHERE i.report_id=$1 AND i.work_item_id IS NOT NULL LIMIT 1`, id).
		Scan(&workItemID); err != nil {
		t.Skipf("no work item was created for the report: %v", err)
	}
	suggest := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/decisions/suggest", workItemID), map[string]any{}, author)
	assertUnavailable(t, "결정 제안", suggest)

	upload := server.upload("/api/v1/import/pptx", "files", "주간보고.pptx", []byte("PK\x03\x04not-a-real-deck"), author)
	assertUnavailable(t, "PPTX Import 업로드", upload)
}

func assertUnavailable(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("%s: %d, want 503 — %s", what, w.Code, w.Body.String())
		return
	}
	if code := errorCode(w); code != "AI_UNAVAILABLE" {
		t.Errorf("%s: coded %q, not AI_UNAVAILABLE", what, code)
	}
}

// guards: reviewReport, forceConfluenceSync
func TestFeaturesLeftOffAnswerWithTheirOwnCodeNotAFailure(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("off_flow", "USER", nil)
	id := server.submitted(author, "2026-08-24", "워크플로 꺼짐")

	// The review workflow is off by default, so approving is not a permission
	// problem and not an error — it is a setting.
	approve := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", id), map[string]any{}, server.admin)
	if approve.Code != http.StatusConflict {
		t.Errorf("approving with the workflow off answered %d, want 409 — %s", approve.Code, approve.Body.String())
	}
	if code := errorCode(approve); code != "WORKFLOW_DISABLED" {
		t.Errorf("the refusal is coded %q, not WORKFLOW_DISABLED", code)
	}

	sync := server.request(http.MethodPost, "/api/v1/admin/confluence/sync", map[string]any{}, server.admin)
	if sync.Code != http.StatusConflict {
		t.Errorf("syncing with Confluence off answered %d, want 409 — %s", sync.Code, sync.Body.String())
	}
	if code := errorCode(sync); code != "CONFLUENCE_DISABLED" {
		t.Errorf("the refusal is coded %q, not CONFLUENCE_DISABLED", code)
	}
}

// Reading has to keep working with every option off, or an offline install
// looks broken on the day it is switched on.
//
// guards: listReports, analyticsParticipation
func TestReadingWorksWithEveryOptionOff(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("off_read", "USER", nil)
	server.submitted(author, "2026-08-24", "기본 설치")

	for _, path := range []string{
		"/api/v1/reports",
		"/api/v1/work-items",
		"/api/v1/rollups",
		"/api/v1/search?q=기본",
		"/api/v1/admin/analytics/participation",
		"/api/v1/admin/confluence/sync/status",
		"/api/v1/digest",
	} {
		w := server.request(http.MethodGet, path, nil, server.admin)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s with nothing configured: %d %s", path, w.Code, w.Body.String())
		}
	}
}

// docs/openapi.yaml: "두 확장이 모두 없으면 1단계 정확 일치만 수행하며 기존 동작과
// 같다." Plain PostgreSQL is a supported target, and the trigram and vector
// queries would error there — so every one of them has to sit behind its own
// capability flag. Nothing checked that the gates were on the right doors.
//
// guards: searchReports, searchWorkItems
func TestSearchOnPlainPostgreSQLFallsBackInsteadOfFailing(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("plain_pg", "USER", nil)
	server.submitted(author, "2026-08-24", "확장 없는 설치")

	full := server.app.capabilities
	if !full.Trigram && !full.Vector {
		t.Skip("the test database has neither extension, so there is nothing to switch off")
	}
	// A near miss, not a word that appears nowhere. A query matching nothing
	// leaves the later passes empty either way, and the flags stay false whether
	// the gate held or not — which is a test that cannot fail.
	const nearMiss = "/api/v1/search?q=확장업는"

	if full.Trigram {
		withExtensions := server.request(http.MethodGet, nearMiss, nil, author)
		if withExtensions.Code != http.StatusOK {
			t.Fatalf("report search with extensions: %d %s", withExtensions.Code, withExtensions.Body.String())
		}
		if fuzzy, _ := decodeData(t, withExtensions)["fuzzy"].(bool); !fuzzy {
			t.Fatalf("the near miss did not reach the approximate pass even with pg_trgm present, so switching it off proves nothing: %s",
				withExtensions.Body.String())
		}
	}

	server.app.capabilities = databaseCapabilities{}
	t.Cleanup(func() { server.app.capabilities = full })

	reports := server.request(http.MethodGet, nearMiss, nil, author)
	if reports.Code != http.StatusOK {
		t.Fatalf("report search without extensions: %d %s", reports.Code, reports.Body.String())
	}
	data := decodeData(t, reports)
	if fuzzy, _ := data["fuzzy"].(bool); fuzzy {
		t.Error("the approximate pass ran with pg_trgm reported absent")
	}
	if semantic, _ := data["semantic"].(bool); semantic {
		t.Error("the meaning pass ran with pgvector reported absent")
	}

	if items := server.request(http.MethodGet, "/api/v1/work-items/search?q=확장업는", nil, author); items.Code != http.StatusOK {
		t.Fatalf("work item search without extensions: %d %s", items.Code, items.Body.String())
	}
}
