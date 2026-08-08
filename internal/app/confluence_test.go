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
	cleaned := cleanConfluenceStorage(`<p>OAuth &amp; 인증</p><script>secret-command</script><ac:structured-macro ac:name="toc"/>`, 100)
	if cleaned != "OAuth & 인증" || strings.Contains(cleaned, "secret-command") {
		t.Fatalf("cleaned storage = %q", cleaned)
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
		content := `{"groups":[{"include":true,"normalizedTitle":"OAuth 인증 적용","category":"개발","pageIds":["101"],"confidence":0.9},{"include":false,"normalizedTitle":"","category":"","pageIds":["102"],"confidence":0.95}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()
	activities := []confluenceActivity{
		{Page: ConfluencePage{ID: "101", Title: "OAuth PoC"}, RuleScore: 60},
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

func serverURLFromRequest(r *http.Request) string {
	return "http://" + r.Host
}
