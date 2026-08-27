package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Reproduced against a stand-in Confluence: a server that answers `size` with
// the grand total rather than the count in this page — which is what a proxy or
// another generation of the API can do — made the sync ask for page after empty
// page. 87 requests, "10,080 pages scanned" reported, 120 pages actually seen,
// and then the 10,000 safety limit failed the whole sync.

// guards: runConfluenceSync
func TestTheSyncStopsWhenAPageComesBackEmpty(t *testing.T) {
	server := newTestServer(t)

	const total = 60
	const batch = 25
	var requests atomic.Int64
	confluence := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/content/search":
			requests.Add(1)
			start := 0
			_, _ = fmt.Sscanf(r.URL.Query().Get("start"), "%d", &start)
			results := []any{}
			for index := start; index < start+batch && index < total; index++ {
				results = append(results, map[string]any{
					"id": fmt.Sprint(700000 + index), "type": "page", "status": "current",
					"title": fmt.Sprintf("페이징 확인 %d", index),
					"space": map[string]any{"key": "PAGE"},
					"history": map[string]any{"createdDate": "2026-08-03T09:00:00.000+0900",
						"createdBy": map[string]any{"username": "u1"}},
					"version": map[string]any{"number": 1, "when": "2026-08-07T18:30:00.000+0900",
						"by": map[string]any{"username": "u1"}},
					"_links": map[string]any{"webui": fmt.Sprintf("/pages/%d", 700000+index)},
				})
			}
			// The counter every version of this API agrees is the total, put
			// where a faithful server puts the page count.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": results, "start": start, "limit": batch, "size": total,
				"_links": map[string]any{"base": confluenceBase(r)},
			})
		case len(r.URL.Path) > len("/rest/api/content/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": r.URL.Path[len("/rest/api/content/"):], "version": map[string]any{"number": 1},
				"body": map[string]any{"storage": map[string]any{"value": "<p>본문 확인</p>"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer confluence.Close()

	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"confluence.enabled": "true", "confluence.base_url": confluence.URL,
			"confluence.auth_mode": "BASIC", "confluence.username": "svc",
			"confluence.password": "secret", "confluence.batch_size": fmt.Sprint(batch),
			"confluence.analyze_body": "false", "confluence.ai_enabled": "false",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("Confluence 를 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}
	cfg, err := server.app.loadConfluenceSettings(server.ctx())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.app.runConfluenceSync(server.ctx(), cfg); err != nil {
		t.Fatalf("동기화가 실패했습니다: %v", err)
	}

	// Three full pages and one empty one is the whole walk. Anything more means
	// the loop is following the server's counter past the end of the results.
	if got := requests.Load(); got > int64(total/batch)+2 {
		t.Errorf("검색을 %d번 불렀습니다. %d건을 %d씩 걷는 데 그만큼 필요하지 않습니다", got, total, batch)
	}
	var scanned int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT pages_scanned FROM confluence_sync_state WHERE system_type='CONFLUENCE'`).Scan(&scanned); err != nil {
		t.Fatal(err)
	}
	if scanned != total {
		t.Errorf("훑었다고 기록한 쪽수가 %d, 실제로 받은 것은 %d 입니다", scanned, total)
	}
}

// confluenceBase is the address the stand-in reports as its own, the way a
// Confluence behind a context path does.
func confluenceBase(r *http.Request) string {
	return "http://" + r.Host
}
