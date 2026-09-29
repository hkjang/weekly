package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConfluence691ClientSearchAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "service" || password != "secret" {
			t.Fatalf("unexpected Basic Auth: %q %q %v", username, password, ok)
		}
		switch r.URL.Path {
		case "/rest/api/content/search":
			if got := r.URL.Query().Get("expand"); got != "history,version,space" {
				t.Fatalf("expand = %q", got)
			}
			cql := r.URL.Query().Get("cql")
			if !strings.Contains(cql, "type = page") || !strings.Contains(cql, "lastmodified >=") {
				t.Fatalf("CQL = %q", cql)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{map[string]any{
					"id": "123", "type": "page", "status": "current", "title": "AI Gateway OAuth 테스트 결과",
					"space":   map[string]any{"key": "AI"},
					"history": map[string]any{"createdDate": "2026-08-03T09:00:00.000+0900", "createdBy": map[string]any{"username": "hkjang"}},
					"version": map[string]any{"number": 7, "when": "2026-08-07T18:30:00.000+0900", "by": map[string]any{"username": "hkjang"}},
					"_links":  map[string]any{"webui": "/pages/viewpage.action?pageId=123"},
				}},
				"start": 0, "limit": 50, "size": 1, "_links": map[string]any{"base": serverURLFromRequest(r) + "/confluence"},
			})
		case "/rest/api/content/123":
			if r.URL.Query().Get("expand") != "body.storage,version" {
				t.Fatalf("body expand = %q", r.URL.Query().Get("expand"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "123", "version": map[string]any{"number": 7}, "body": map[string]any{"storage": map[string]any{"value": "<p>OAuth PoC 완료</p>"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewConfluenceClient(ConfluenceClientConfig{BaseURL: server.URL, AuthMode: "BASIC", Username: "service", Password: "secret", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.SearchChangedPages(t.Context(), time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 1 || result.Pages[0].ID != "123" || result.Pages[0].CreatorUsername != "hkjang" || result.Pages[0].LastModifierUsername != "hkjang" || result.Pages[0].Version != 7 {
		t.Fatalf("unexpected page result: %#v", result)
	}
	if !strings.Contains(result.Pages[0].WebURL, "/confluence/pages/viewpage.action") {
		t.Fatalf("web URL = %q", result.Pages[0].WebURL)
	}
	body, err := client.GetPageBody(t.Context(), "123")
	if err != nil || body.Version != 7 || !strings.Contains(body.Storage, "OAuth PoC") {
		t.Fatalf("body = %#v, err = %v", body, err)
	}
}

func TestConfluenceClientDoesNotFollowRedirect(t *testing.T) {
	targetReached := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetReached = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	client, err := NewConfluenceClient(ConfluenceClientConfig{BaseURL: server.URL, AuthMode: "NONE", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SearchChangedPages(t.Context(), time.Now().Add(-time.Hour), 0, 1)
	var httpErr *ConfluenceHTTPError
	if err == nil || !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound || targetReached {
		t.Fatalf("redirect handling err=%v targetReached=%v httpErr=%v", err, targetReached, httpErr)
	}
}

func TestConfluenceRuleScoringAndStorageCleaning(t *testing.T) {
	cfg := confluenceSettings{
		IncludeSpaces: map[string]bool{"ai": true}, WorkKeywords: []string{"개발", "테스트"}, PersonalSpacePrefixes: []string{"~"},
		ScoreProjectSpace: 20, ScoreCreator: 20, ScoreModifier: 20, ScoreWorkKeyword: 20,
		ScoreMeeting: -10, ScoreNotice: -20, ScoreLeave: -50, ScorePersonalSpace: -30,
	}
	activity := confluenceActivity{Page: ConfluencePage{Title: "[AI] OAuth 개발 테스트", SpaceKey: "AI"}, ActivityType: "CREATED_AND_MODIFIED"}
	if score := scoreConfluenceActivity(activity, cfg); score != 80 {
		t.Fatalf("work score = %d", score)
	}
	activity.Page.Title = "휴가 계획 공지"
	activity.Page.SpaceKey = "~HKJANG"
	if score := scoreConfluenceActivity(activity, cfg); score >= 20 {
		t.Fatalf("leave/personal score should be excluded, got %d", score)
	}
	cleaned := cleanConfluenceStorage(`<p>OAuth &amp; 인증</p><table><tr><td>구현 완료</td><td>운영 검증</td></tr></table><script>secret-command</script><ac:structured-macro ac:name="toc"/>`, 100)
	if cleaned != "OAuth & 인증\n[ROW] 구현 완료 | 운영 검증" || strings.Contains(cleaned, "secret-command") {
		t.Fatalf("cleaned storage = %q", cleaned)
	}
}

func TestCleanConfluenceStoragePreservesDocumentStructureAndTail(t *testing.T) {
	storage := `<h2>OAuth 전환</h2>
<p>인증 &amp; 권한 작업</p>
<ul><li><strong>OIDC 연동 완료</strong></li><li>권한 검증 완료</li></ul>
<table><thead><tr><th>업무</th><th>금주 실적</th><th>차주 계획</th></tr></thead>
<tbody><tr><td>인증</td><td>개발 완료<br/>시험 완료</td><td>운영 반영</td></tr></tbody></table>
<style>.leak{display:block}</style><script>ignore-this()</script>`
	want := "[H2] OAuth 전환\n인증 & 권한 작업\n• OIDC 연동 완료\n• 권한 검증 완료\n[ROW] 업무 | 금주 실적 | 차주 계획\n[ROW] 인증 | 개발 완료 시험 완료 | 운영 반영"
	if got := cleanConfluenceStorage(storage, 2000); got != want {
		t.Fatalf("structured Confluence text\n got: %q\nwant: %q", got, want)
	}

	long := "[H1] 시작\n" + strings.Repeat("중간 본문 ", 80) + "\n[H2] 다음 계획\n• 운영 반영"
	got := cleanConfluenceStorage(long, 100)
	if runeLength(got) > 100 || !strings.Contains(got, "[TRUNCATED: middle content omitted]") || !strings.Contains(got, "다음 계획") || !strings.Contains(got, "운영 반영") {
		t.Fatalf("head/tail truncation lost structure or exceeded limit: %d %q", runeLength(got), got)
	}
}

func TestConfluenceClassifierRejectsIncompleteCoverage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content := `{"groups":[{"include":true,"normalizedTitle":"OAuth","category":"개발","pageIds":["101"],"confidence":0.9}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()
	activities := []confluenceActivity{{Page: ConfluencePage{ID: "101"}}, {Page: ConfluencePage{ID: "102"}}}
	if _, _, err := callConfluenceClassifier(t.Context(), aiConfiguration{Endpoint: server.URL, Model: "test", Timeout: 3 * time.Second, MaxInput: 50000}, activities); err == nil {
		t.Fatal("classifier response that omits an input page must fall back")
	}
}

func TestConfluenceMappingSuggestionPrioritizesEmailLocalpart(t *testing.T) {
	value, source := confluenceMappingSuggestion("weekly-user", "hkjang@koreacb.com")
	if value != "hkjang" || source != "EMAIL_LOCALPART" {
		t.Fatalf("suggestion = %q %q", value, source)
	}
}

func TestConfluenceClassifierTracksIncludedAndExcludedPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(request["messages"])
		if !strings.Contains(string(encoded), "OAuth 구현 완료") {
			t.Fatalf("classifier did not receive the bounded body preview: %s", encoded)
		}
		content := `{"groups":[{"include":true,"normalizedTitle":"OAuth 인증 적용","category":"개발","pageIds":["101"],"confidence":0.9},{"include":false,"normalizedTitle":"","category":"","pageIds":["102"],"confidence":0.95}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()
	activities := []confluenceActivity{
		{Page: ConfluencePage{ID: "101", Title: "OAuth PoC"}, RuleScore: 60, BodyText: "OAuth 구현 완료"},
		{Page: ConfluencePage{ID: "102", Title: "휴가 계획"}, RuleScore: 20},
	}
	groups, decisions, err := callConfluenceClassifier(t.Context(), aiConfiguration{Endpoint: server.URL, Model: "test", Timeout: 3 * time.Second, MaxInput: 50000}, activities)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Activities) != 1 || groups[0].Activities[0].Page.ID != "101" || !decisions["101"] {
		t.Fatalf("included classification = %#v, decisions = %#v", groups, decisions)
	}
	if included, exists := decisions["102"]; !exists || included {
		t.Fatalf("excluded page decision = %v, exists = %v", included, exists)
	}
}

func TestConfluenceSummarizerUsesAtomicEvidenceBackedFactsAndActivityMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 2 {
			t.Fatalf("messages = %#v", request.Messages)
		}
		for _, expected := range []string{`"activityType":"MODIFIED"`, `"weekStart":"2026-08-03"`, `"version":7`, `"updatedAt":"2026-08-07T18:30:00+09:00"`} {
			if !strings.Contains(request.Messages[1].Content, expected) {
				t.Errorf("summarizer input omitted %s: %s", expected, request.Messages[1].Content)
			}
		}
		content := `{"facts":[{"kind":"CURRENT_RESULT","text":"OAuth 구현 완료","evidencePageIds":["101"],"confidence":0.88},{"kind":"CURRENT_RESULT","text":"권한 검증 완료","evidencePageIds":["101"],"confidence":0.78},{"kind":"NEXT_PLAN","text":"운영 반영","evidencePageIds":["101"],"confidence":0.82}],"confidence":0.9}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()

	updated := time.Date(2026, 8, 7, 18, 30, 0, 0, time.FixedZone("KST", 9*60*60))
	group := confluenceCandidateGroup{Title: "OAuth 인증", Category: "개발", Activities: []confluenceActivity{{
		Page:      ConfluencePage{ID: "101", Title: "OAuth 구현", SpaceKey: "AI", Version: 7, UpdatedAt: updated},
		WeekStart: "2026-08-03", ActivityType: "MODIFIED",
	}}}
	result, err := callConfluenceSummarizer(t.Context(), aiConfiguration{Endpoint: server.URL, Model: "test", Timeout: 3 * time.Second, MaxInput: 50000}, group, map[string]string{"101": "OAuth 구현 완료\n다음: 운영 반영"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CurrentResult != "• OAuth 구현 완료\n• 권한 검증 완료" || result.NextPlan != "운영 반영" || result.Issue != "" || result.Confidence != 0.78 {
		t.Fatalf("summary = %#v", result)
	}
}

func TestConfluenceSummarizerCorrectsUnknownEvidenceOnce(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if calls == 2 && !strings.Contains(request.Messages[0].Content, `evidence page "999"`) {
			t.Fatalf("corrective prompt omitted evidence failure: %s", request.Messages[0].Content)
		}
		pageID := "999"
		if calls == 2 {
			pageID = "101"
		}
		content := `{"facts":[{"kind":"CURRENT_RESULT","text":"구현 완료","evidencePageIds":["` + pageID + `"],"confidence":0.8}],"confidence":0.8}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()

	group := confluenceCandidateGroup{Activities: []confluenceActivity{{Page: ConfluencePage{ID: "101"}}}}
	result, err := callConfluenceSummarizer(t.Context(), aiConfiguration{Endpoint: server.URL, Model: "test", Timeout: 3 * time.Second, MaxInput: 50000}, group, map[string]string{"101": "구현 완료"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result.CurrentResult != "구현 완료" {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}

func TestConfluenceSummarizerRejectsEmptyFactsAfterOneCorrection(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		content := `{"facts":[],"confidence":1}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()

	group := confluenceCandidateGroup{Confidence: 0.45, Activities: []confluenceActivity{{Page: ConfluencePage{ID: "101", Title: "OAuth 구현"}}}}
	_, err := callConfluenceSummarizer(t.Context(), aiConfiguration{Endpoint: server.URL, Model: "test", Timeout: 3 * time.Second, MaxInput: 50000}, group, map[string]string{"101": "OAuth 구현"})
	if err == nil || !strings.Contains(err.Error(), "no evidence-backed facts") {
		t.Fatalf("empty facts must be rejected, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("empty facts should receive exactly one corrective retry, calls = %d", calls)
	}
	fallback := fallbackConfluenceSummary(group)
	if fallback.Confidence != 0.45 {
		t.Fatalf("fallback confidence was inflated: %#v", fallback)
	}
}

func TestConfluenceCandidateLegacyNormalizationAndCanonicalMerge(t *testing.T) {
	legacy := confluenceCandidateView{CurrentResult: "OIDC 연동 완료, 권한 검증 완료.", NextPlan: "운영 배포 준비", UserEdited: false}
	normalizeConfluenceCandidateView(&legacy)
	if legacy.CurrentResult != "• OIDC 연동 완료\n• 권한 검증 완료." {
		t.Fatalf("legacy candidate was not normalized: %#v", legacy)
	}
	edited := confluenceCandidateView{CurrentResult: "사용자, 작성 문장.", UserEdited: true}
	normalizeConfluenceCandidateView(&edited)
	if edited.CurrentResult != "사용자, 작성 문장." {
		t.Fatalf("user-edited candidate was changed: %#v", edited)
	}
	if got := mergeUniqueLines("- 인증 완료", "• 인증 완료."); got != "• 인증 완료" {
		t.Fatalf("list markers were not canonicalized for deduplication: %q", got)
	}
	fallback := fallbackConfluenceSummary(confluenceCandidateGroup{Activities: []confluenceActivity{{Page: ConfluencePage{Title: "OAuth 구현"}}}})
	if fallback.CurrentResult != "• OAuth 구현" {
		t.Fatalf("fallback list marker = %q", fallback.CurrentResult)
	}
}

// guards: adminConfluenceStatus
//
// confluence_sync_errors keeps the newest 500 rows, but the status query reads
// only the newest confluenceRecentErrorLimit of them. A full table therefore
// reads as "this is every diagnosis there is" — a sync that failed forty times
// shows twenty rows and no sign of the rest. The response has to say so, and
// only when it is true: a permanent notice teaches the operator to ignore it.
func TestTheOperatorLearnsTheDiagnosisTableIsCutOff(t *testing.T) {
	server := newTestServer(t)

	read := func() map[string]any {
		t.Helper()
		w := server.request(http.MethodGet, "/api/v1/admin/confluence/sync/status", nil, server.admin)
		if w.Code != http.StatusOK {
			t.Fatalf("read the status: %d %s", w.Code, w.Body.String())
		}
		return decodeData(t, w)
	}

	// A table that fits: the screen shows everything, so no notice.
	for i := 0; i < confluenceRecentErrorLimit; i++ {
		server.app.recordConfluenceNotice(server.ctx(), "SCAN", "연결되지 않은 사용자를 찾았습니다.")
	}
	data := read()
	if rows, _ := data["recentErrors"].([]any); len(rows) != confluenceRecentErrorLimit {
		t.Fatalf("recentErrors=%d, want the full %d", len(rows), confluenceRecentErrorLimit)
	}
	if _, present := data["recentErrorsTruncated"]; present {
		t.Fatalf("recentErrorsTruncated=%v on an untruncated table, want the field absent", data["recentErrorsTruncated"])
	}

	// One row more than the screen carries. The rows returned must not grow —
	// the extra row is read only to learn that it exists.
	server.app.recordConfluenceError(server.ctx(), "12345", "FETCH", 502, errors.New("relay refused the page"))
	data = read()
	if rows, _ := data["recentErrors"].([]any); len(rows) != confluenceRecentErrorLimit {
		t.Fatalf("recentErrors=%d, want still %d", len(rows), confluenceRecentErrorLimit)
	}
	if truncated, _ := data["recentErrorsTruncated"].(bool); !truncated {
		t.Fatalf("recentErrorsTruncated=%v, want true", data["recentErrorsTruncated"])
	}
}

// guards: adminConfluenceStatus
//
// Every number on this card is read as the state of the last sync. Three of its
// reads used to throw their error away, and the value left behind was in each
// case the most reassuring one the screen can show: an empty diagnosis table
// ("진단 없음"), and 0 mapped / 0 unmapped users. A card that cannot reach the
// database has to say that instead of answering with a zero, the way the sync
// state query above it already does.
func TestTheConfluenceCardRefusesToAnswerWithAFailedQuery(t *testing.T) {
	server := newTestServer(t)
	// A real diagnosis is waiting to be read, so an empty table below is the
	// failure speaking and not the truth.
	server.app.recordConfluenceError(server.ctx(), "12345", "FETCH", 502, errors.New("relay refused the page"))

	// Take each table away the way a lost tablespace or a botched migration
	// would. The harness gives this test its own database, so the rename is
	// undone only to keep the rest of the server usable.
	for _, table := range []string{"confluence_sync_errors", "user_external_accounts"} {
		t.Run(table, func(t *testing.T) {
			if _, err := server.app.db.Exec(server.ctx(), `ALTER TABLE `+table+` RENAME TO `+table+`_hidden`); err != nil {
				t.Fatalf("hide %s: %v", table, err)
			}
			defer func() {
				if _, err := server.app.db.Exec(server.ctx(), `ALTER TABLE `+table+`_hidden RENAME TO `+table); err != nil {
					t.Fatalf("restore %s: %v", table, err)
				}
			}()

			w := server.request(http.MethodGet, "/api/v1/admin/confluence/sync/status", nil, server.admin)
			if w.Code != http.StatusInternalServerError || errorCode(w) != "QUERY_FAILED" {
				t.Fatalf("status with %s unreachable = %d %s, want 500 QUERY_FAILED instead of a card that reads as a healthy one", table, w.Code, w.Body.String())
			}
		})
	}

	// With every table back the card answers again, so the refusal above is the
	// missing table talking and not a handler that stopped working.
	w := server.request(http.MethodGet, "/api/v1/admin/confluence/sync/status", nil, server.admin)
	if w.Code != http.StatusOK {
		t.Fatalf("status once the tables are back = %d %s", w.Code, w.Body.String())
	}
	if rows, _ := decodeData(t, w)["recentErrors"].([]any); len(rows) != 1 {
		t.Fatalf("recentErrors=%d, want the 1 diagnosis this test recorded", len(rows))
	}
}

func serverURLFromRequest(r *http.Request) string {
	return "http://" + r.Host
}

// guards: forceConfluenceSync
func TestTheForceSyncRefusesAnUnreadableState(t *testing.T) {
	server := newTestServer(t)
	searched := make(chan struct{}, 1)
	confluence := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/content/search" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "start": 0, "limit": 25, "size": 0})
		select {
		case searched <- struct{}{}:
		default:
		}
	}))
	defer confluence.Close()
	on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"confluence.enabled": "true", "confluence.base_url": confluence.URL,
			"confluence.auth_mode": "BASIC", "confluence.username": "svc",
			"confluence.password": "secret", "confluence.analyze_body": "false",
			"confluence.ai_enabled": "false",
		},
	}, server.admin)
	if on.Code != http.StatusOK {
		t.Fatalf("enable Confluence: %d %s", on.Code, on.Body.String())
	}
	// Observe the real worker past its startup reset, then wait for its sync
	// to finish using the same database lock it holds in production.
	select {
	case <-searched:
	case <-time.After(10 * time.Second):
		t.Fatal("the real Confluence worker did not reach the search endpoint")
	}
	connection, err := server.app.db.Acquire(server.ctx())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(server.ctx(), "SELECT pg_advisory_lock($1)", confluenceAdvisoryLock); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := connection.Exec(server.ctx(), "SELECT pg_advisory_unlock($1)", confluenceAdvisoryLock); err != nil {
			t.Error(err)
		}
	}()
	exec := func(sql string) {
		t.Helper()
		if _, err := server.app.db.Exec(server.ctx(), sql); err != nil {
			t.Fatal(err)
		}
	}
	audits := func() int {
		t.Helper()
		var count int
		if err := server.app.db.QueryRow(server.ctx(),
			"SELECT count(*) FROM audit_logs WHERE action='confluence.sync.request'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	check := func(t *testing.T, wantCode int, wantStatus string, wantQueued bool, wantAudits int) {
		t.Helper()
		before := audits()
		w := server.request(http.MethodPost, "/api/v1/admin/confluence/sync", nil, server.admin)
		if w.Code != wantCode {
			t.Errorf("sync = %d %s, want %d", w.Code, w.Body.String(), wantCode)
		} else if wantCode == http.StatusInternalServerError {
			var body struct {
				Error struct {
					Code    string
					Message string
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "QUERY_FAILED" || body.Error.Message != "동기화 상태를 조회할 수 없습니다." {
				t.Errorf("unsafe or unexpected query failure: %s", w.Body.String())
			}
		} else {
			data := decodeData(t, w)
			if data["status"] != wantStatus || data["queued"] != wantQueued {
				t.Errorf("sync response = %v, want status=%s queued=%v", data, wantStatus, wantQueued)
			}
		}
		if delta := audits() - before; delta != wantAudits {
			t.Errorf("request audit delta = %d, want %d", delta, wantAudits)
		}
	}
	t.Run("unreachable table", func(t *testing.T) {
		exec("ALTER TABLE confluence_sync_state RENAME TO confluence_sync_state_hidden")
		defer exec("ALTER TABLE confluence_sync_state_hidden RENAME TO confluence_sync_state")
		check(t, http.StatusInternalServerError, "", false, 0)
	})
	t.Run("missing row", func(t *testing.T) {
		exec("DELETE FROM confluence_sync_state WHERE system_type='CONFLUENCE'")
		defer exec("INSERT INTO confluence_sync_state(system_type) VALUES ('CONFLUENCE')")
		check(t, http.StatusInternalServerError, "", false, 0)
	})
	t.Run("running", func(t *testing.T) {
		exec("UPDATE confluence_sync_state SET status='RUNNING' WHERE system_type='CONFLUENCE'")
		check(t, http.StatusAccepted, "RUNNING", false, 0)
	})
	t.Run("idle after recovery", func(t *testing.T) {
		exec("UPDATE confluence_sync_state SET status='IDLE' WHERE system_type='CONFLUENCE'")
		check(t, http.StatusAccepted, "QUEUED", true, 1)
	})
}
