package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 방문 추적 스크립트 — the snippet and the policy it needs, together.
//
// Every page is served under script-src 'self'. A tracking snippet pasted into
// that page is refused by the browser and nothing on the screen says so, which
// is why this feature is mostly about the policy: a nonce per request on every
// script tag, the tracker's origins read out of the snippet, and the refusals
// recorded where an administrator can see them. What must never happen is the
// easy fix — 'unsafe-inline' — because it stays after tracking is off.

var scriptNonce = regexp.MustCompile(`<script[^>]*\snonce="([^"]*)"`)

// guards: withNonce=100, indexFold=100, snippetOrigins=100
func TestEveryScriptTagInASnippetCarriesTheRequestNonce(t *testing.T) {
	snippet := `<SCRIPT async src="https://momento.corp.example/tracker.js"></SCRIPT>
<script>window.__t={endpoint:"https://momento.corp.example/collect/v1/events",pixel:'https://pixel.corp.example/p.gif?id=1'};</script>
<script nonce="keep-me">1</script>`
	out := withNonce(snippet, "n0nce")
	if strings.Count(out, `nonce="n0nce"`) != 2 || !strings.Contains(out, `nonce="keep-me"`) {
		t.Fatalf("nonce was not applied to exactly the tags without one:\n%s", out)
	}
	// Folding by ToLower would move every index after a letter whose byte
	// length changes when folded. These two are the ones that do; a snippet
	// with either used to come out as `<sc nonce="n">ript>` — no longer a
	// script tag, so tracking stopped without a word.
	for _, prefix := range []string{"İİİİ", "\u212A\u212A"} {
		out := withNonce(prefix+`<script>1</script>`, "n")
		if out != prefix+`<script nonce="n">1</script>` {
			t.Fatalf("nonce landed in the wrong place after %q: %s", prefix, out)
		}
		origins := snippetOrigins(prefix + ` "https://a.example/x" 'HTTP://b.example:8443/y' data:x http://a.example/again https://a.example/twice http://`)
		if strings.Join(origins, " ") != "https://a.example http://b.example:8443 http://a.example" {
			t.Fatalf("origins after %q: %v", prefix, origins)
		}
	}
	if withNonce("", "n") != "" || withNonce("<p>", "") != "<p>" || indexFold("abc", "") != 0 {
		t.Fatal("empty inputs should pass through")
	}
	if withNonce("<script", "n") != `<script nonce="n"` {
		t.Fatalf("an unclosed tag still gets the nonce: %s", withNonce("<script", "n"))
	}
	if got := snippetOrigins("nothing here"); len(got) != 0 {
		t.Fatalf("no address, no origin: %v", got)
	}
}

// guards: trackingPolicy=100, readTrackingConfig=100, policySources=100, momentoProxyTarget=100
func TestThePolicyIsExactlyTheOldOneUntilTrackingIsOn(t *testing.T) {
	off := readTrackingConfig(map[string]string{})
	if off.Enabled || off.Provider != trackingProviderNone || off.Placement != "head" || off.active() {
		t.Fatalf("a fresh install is off: %+v", off)
	}
	if policy := trackingPolicy(off, "n"); policy != basePagePolicy {
		t.Fatalf("off, the policy changed:\n%s\n%s", policy, basePagePolicy)
	}
	// Enabled but pointed at nothing is still off: no nonce, no report-uri.
	hollow := readTrackingConfig(map[string]string{"tracking.enabled": "true", "tracking.provider": "momento"})
	if hollow.active() || trackingPolicy(hollow, "n") != basePagePolicy {
		t.Fatal("a provider with nothing to load must not widen the policy")
	}
	if hollow.momentoProxyTarget() != nil {
		t.Fatal("nothing to forward to")
	}

	cases := map[string]struct {
		values          map[string]string
		wantScript      string
		wantConnectOnly bool
		wantOrigin      string
	}{
		"momento through the proxy": {
			values:          map[string]string{"tracking.enabled": "true", "tracking.provider": "MOMENTO", "tracking.momento_url": "https://momento.corp.example/", "tracking.momento_site_id": "SITE_1", "tracking.placement": "BODY"},
			wantScript:      `src="/momento/tracker.js" data-site-id="SITE_1" data-environment="prd" data-contract-version="1" data-endpoint="/momento"`,
			wantConnectOnly: true,
		},
		"momento direct": {
			values:     map[string]string{"tracking.enabled": "true", "tracking.provider": "momento", "tracking.momento_url": "https://momento.corp.example", "tracking.momento_site_id": "SITE_1", "tracking.momento_endpoint": "DIRECT"},
			wantScript: `src="https://momento.corp.example/tracker.js"`,
			wantOrigin: "https://momento.corp.example",
		},
		"ga4": {
			values:     map[string]string{"tracking.enabled": "true", "tracking.provider": "ga4", "tracking.measurement_id": "G-1"},
			wantScript: `gtag/js?id=G-1`,
			wantOrigin: "https://www.googletagmanager.com",
		},
		"gtm": {
			values:     map[string]string{"tracking.enabled": "true", "tracking.provider": "gtm", "tracking.measurement_id": "GTM-X"},
			wantScript: `'GTM-X'`,
			wantOrigin: "https://www.googletagmanager.com",
		},
		"matomo": {
			values:     map[string]string{"tracking.enabled": "true", "tracking.provider": "matomo", "tracking.matomo_url": "https://matomo.corp.example/", "tracking.matomo_site_id": "7"},
			wantScript: `u="https://matomo.corp.example/"`,
			wantOrigin: "https://matomo.corp.example",
		},
		"custom with an allowed host": {
			values:     map[string]string{"tracking.enabled": "true", "tracking.provider": "custom", "tracking.custom_snippet": `<script src="https://t.corp.example/t.js"></script>`, "tracking.allowed_hosts": "https://pixel.corp.example/, https://t.corp.example"},
			wantScript: `src="https://t.corp.example/t.js"`,
			wantOrigin: "https://pixel.corp.example",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			config := readTrackingConfig(tc.values)
			if !config.active() {
				t.Fatalf("not active: %+v", config)
			}
			snippet := config.snippet("N+1=")
			if !strings.Contains(snippet, tc.wantScript) {
				t.Fatalf("snippet lacks %q:\n%s", tc.wantScript, snippet)
			}
			if strings.Count(snippet, "<script") != strings.Count(snippet, `nonce="N+1="`) {
				t.Fatalf("a script tag without the nonce:\n%s", snippet)
			}
			policy := trackingPolicy(config, "N+1=")
			if scripts := strings.SplitN(strings.SplitN(policy, "script-src ", 2)[1], ";", 2)[0]; strings.Contains(scripts, "unsafe-inline") {
				t.Fatalf("script-src was loosened: %s", policy)
			}
			if !strings.Contains(policy, "script-src 'self' 'nonce-N+1='") || !strings.HasSuffix(policy, "; report-uri "+cspReportPath) {
				t.Fatalf("policy lacks the nonce or the report address: %s", policy)
			}
			if tc.wantConnectOnly {
				if !strings.Contains(policy, "connect-src 'self';") || strings.Contains(policy, "momento.corp.example") {
					t.Fatalf("through the proxy no outside origin may appear: %s", policy)
				}
				if config.momentoProxyTarget().String() != "https://momento.corp.example" || config.Placement != "body" {
					t.Fatalf("proxy target %v placement %s", config.momentoProxyTarget(), config.Placement)
				}
				return
			}
			if config.momentoProxyTarget() != nil {
				t.Fatal("only the proxied Momento setup forwards")
			}
			if !strings.Contains(policy, tc.wantOrigin) {
				t.Fatalf("policy lacks %s: %s", tc.wantOrigin, policy)
			}
		})
	}
	// An address that is not http(s) is not a proxy target.
	broken := readTrackingConfig(map[string]string{"tracking.enabled": "true", "tracking.provider": "momento", "tracking.momento_url": "ftp://x", "tracking.momento_site_id": "1"})
	if broken.momentoProxyTarget() != nil {
		t.Fatal("ftp is not a collector")
	}
	// The origins with nothing in them add nothing.
	empty := readTrackingConfig(map[string]string{"tracking.provider": "matomo"})
	if s, c, i := empty.policySources(); len(s)+len(c)+len(i) != 0 {
		t.Fatalf("no address, no sources: %v %v %v", s, c, i)
	}
	if s, _, _ := readTrackingConfig(map[string]string{"tracking.provider": "ga4"}).policySources(); len(s) != 1 {
		t.Fatalf("GA4 needs its loader host regardless: %v", s)
	}
	if s, _, _ := readTrackingConfig(map[string]string{"tracking.provider": "momento", "tracking.momento_url": "nonsense", "tracking.momento_endpoint": "DIRECT"}).policySources(); len(s) != 0 {
		t.Fatalf("an address without a host is no origin: %v", s)
	}
}

// guards: validate=100
func TestTrackingCannotBeTurnedOnWithNothingToLoad(t *testing.T) {
	cases := []struct {
		values map[string]string
		want   string
	}{
		{map[string]string{"tracking.provider": "custom"}, ""},
		{map[string]string{"tracking.enabled": "true"}, ""},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "momento"}, "Momento"},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "momento", "tracking.momento_url": "not a url", "tracking.momento_site_id": "1"}, "올바른"},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "momento", "tracking.momento_url": "https://m.example", "tracking.momento_site_id": "1"}, ""},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "ga4"}, "측정 ID"},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "gtm", "tracking.measurement_id": "GTM-1"}, ""},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "matomo"}, "Matomo"},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "matomo", "tracking.matomo_url": "nope", "tracking.matomo_site_id": "1"}, "올바른"},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "matomo", "tracking.matomo_url": "https://m.example", "tracking.matomo_site_id": "1"}, ""},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "custom"}, "추적 코드"},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "custom", "tracking.custom_snippet": "<script></script>"}, ""},
		{map[string]string{"tracking.enabled": "true", "tracking.provider": "piwik"}, "중 하나"},
	}
	for _, tc := range cases {
		err := readTrackingConfig(tc.values).validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%v: unexpected refusal %v", tc.values, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%v: want a refusal mentioning %q, got %v", tc.values, tc.want, err)
		}
	}
}

// guards: record=100, list=100, forget=100, addAllowedHost=100, matchesWildcardOrigin, evictOldest=100
func TestBlockedOriginsAreRememberedOnceEach(t *testing.T) {
	log := newViolationLog()
	moment := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	log.now = func() time.Time { moment = moment.Add(time.Second); return moment }
	for i := 0; i < 5; i++ {
		log.record("https://momento.corp.example/collect/v1/events", "connect-src", "https://weekly.corp.example/")
	}
	log.record("https://www.google-analytics.com/g/collect", "connect-src 'self'", "/")
	log.record("https://pixel.corp.example/p.gif", "", "/")
	log.record("chrome-extension://abc/x.js", "script-src", "/")
	log.record("data:text/plain,x", "img-src", "/")
	log.record("", "script-src", "/")
	config := readTrackingConfig(map[string]string{"tracking.provider": "ga4", "tracking.measurement_id": "G-1", "tracking.allowed_hosts": "https://momento.corp.example"})
	items := log.list(config)
	if len(items) != 3 {
		t.Fatalf("three distinct http origins, got %d: %+v", len(items), items)
	}
	// Most recent first; the repeated one counted, not repeated.
	if items[0].Origin != "https://pixel.corp.example" || items[0].Directive != "connect-src" || items[0].Allowed {
		t.Fatalf("first: %+v", items[0])
	}
	if items[1].Origin != "https://www.google-analytics.com" || !items[1].Allowed {
		t.Fatalf("a wildcard policy entry should mark it allowed: %+v", items[1])
	}
	if items[2].Origin != "https://momento.corp.example" || items[2].Count != 5 || !items[2].Allowed || items[2].FirstSeen.Equal(items[2].LastSeen) {
		t.Fatalf("the repeated origin: %+v", items[2])
	}
	// The same origin under a different directive is a different fix.
	log.record("https://momento.corp.example/tracker.js", "script-src", "/")
	if got := log.list(config); len(got) != 4 {
		t.Fatalf("directive is part of the key: %d", len(got))
	}
	// Bounded: the oldest goes first, and the list never passes the limit.
	for i := 0; i < trackingMaxViolations+10; i++ {
		log.record("https://h"+strings.Repeat("x", i%7)+"-"+string(rune('a'+i%26))+strings.Repeat("y", i/26)+".example/", "img-src", "/")
	}
	items = log.list(config)
	if len(items) != trackingMaxViolations {
		t.Fatalf("bounded at %d, got %d", trackingMaxViolations, len(items))
	}
	for _, item := range items {
		if item.Origin == "https://pixel.corp.example" {
			t.Fatal("the oldest entry should have been evicted")
		}
	}
	// Same second, sorted by origin so the order is stable.
	fixed := newViolationLog()
	fixed.now = func() time.Time { return moment }
	fixed.record("https://b.example/", "img-src", "/")
	fixed.record("https://a.example/", "img-src", "/")
	if got := fixed.list(trackingConfig{}); got[0].Origin != "https://a.example" {
		t.Fatalf("stable order: %+v", got)
	}
	log.forget()
	if len(log.list(config)) != 0 {
		t.Fatal("forget should empty the list")
	}

	if got := addAllowedHost("", "https://a.example/"); got != "https://a.example" {
		t.Fatalf("first host: %q", got)
	}
	if got := addAllowedHost("https://a.example", "HTTPS://A.EXAMPLE"); got != "https://a.example" {
		t.Fatalf("duplicate ignoring case: %q", got)
	}
	if got := addAllowedHost("https://a.example ", "https://b.example"); got != "https://a.example, https://b.example" {
		t.Fatalf("appended: %q", got)
	}
	if got := addAllowedHost("https://a.example", "  "); got != "https://a.example" {
		t.Fatalf("blank adds nothing: %q", got)
	}
	if matchesWildcardOrigin("not a url at all\x7f", map[string]struct{}{"https://*.x": {}}) || matchesWildcardOrigin("https://a.x", map[string]struct{}{"https://a.x": {}}) {
		t.Fatal("no wildcard, no match")
	}
}

// guards: serveSPA, securityHeaders=100, injectTrackingSnippet=100, momentoProxy=100, receiveCSPReport=100, trackingViolations, clearTrackingViolations, allowTrackingHost
func TestTheShellCarriesTheTrackerAndThePolicyThatAllowsIt(t *testing.T) {
	server := newTestServer(t)

	// Fresh install: the shell is untouched and the policy is the old one.
	page := server.request(http.MethodGet, "/", nil, nil)
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), "<script") || page.Header().Get("Content-Security-Policy") != basePagePolicy {
		t.Fatalf("fresh install changed: %d %q %s", page.Code, page.Body.String(), page.Header().Get("Content-Security-Policy"))
	}
	for _, path := range []string{"/api/v1/version", "/healthz", "/readyz", "/momento/tracker.js"} {
		w := server.request(http.MethodGet, path, nil, nil)
		if got := w.Header().Get("Content-Security-Policy"); got != apiPolicy {
			t.Fatalf("%s should carry the narrow policy, got %s", path, got)
		}
		if path == "/momento/tracker.js" && w.Code != http.StatusNotFound {
			t.Fatalf("the proxy is off until chosen: %d %s", w.Code, w.Body.String())
		}
	}

	// A collector standing in for Momento, remembering what it was asked.
	var proxied []string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = append(proxied, r.Method+" "+r.URL.RequestURI()+" cookie="+r.Header.Get("Cookie")+" host="+r.Host+" xff="+r.Header.Get("X-Forwarded-For"))
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("window.__momento=1"))
	}))
	defer collector.Close()

	// Turning it on with nothing to load is refused with the reason.
	w := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{"tracking.enabled": "true", "tracking.provider": "momento"}}, server.admin)
	if w.Code != http.StatusBadRequest || !strings.Contains(refusal(t, w), "TRACKING_CONFIGURATION_REQUIRED") {
		t.Fatalf("enable without an address: %d %s", w.Code, w.Body.String())
	}
	// And a snippet past the limit is not stored.
	w = server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{"tracking.custom_snippet": strings.Repeat("x", trackingMaxSnippetBytes+1)}}, server.admin)
	if w.Code != http.StatusBadRequest || !strings.Contains(refusal(t, w), "tracking.custom_snippet") {
		t.Fatalf("an 8KB+1 snippet was accepted: %d %s", w.Code, w.Body.String())
	}

	w = server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{
		"tracking.enabled": "true", "tracking.provider": "momento",
		"tracking.momento_url": collector.URL + "/", "tracking.momento_site_id": "SITE_WEEKLY", "tracking.placement": "body",
	}}, server.admin)
	if w.Code != http.StatusOK {
		t.Fatalf("enable Momento: %d %s", w.Code, w.Body.String())
	}

	page = server.request(http.MethodGet, "/", nil, nil)
	body := page.Body.String()
	policy := page.Header().Get("Content-Security-Policy")
	match := scriptNonce.FindStringSubmatch(body)
	if match == nil || len(match[1]) < 20 {
		t.Fatalf("no nonce on the injected tag:\n%s", body)
	}
	if !strings.Contains(policy, "script-src 'self' 'nonce-"+match[1]+"';") || !strings.Contains(policy, "report-uri "+cspReportPath) {
		t.Fatalf("policy does not name the page's nonce: %s", policy)
	}
	if strings.Contains(policy, collector.URL) || !strings.Contains(policy, "connect-src 'self';") {
		t.Fatalf("through the proxy no outside origin may appear: %s", policy)
	}
	if !strings.Contains(body, `data-endpoint="/momento"`) || !strings.Contains(body, `data-site-id="SITE_WEEKLY"`) {
		t.Fatalf("snippet: %s", body)
	}
	// The harness shell has no </body>, so the snippet lands at the end; a
	// real shell gets it before the closing tag it asked for.
	if !strings.HasSuffix(strings.TrimSpace(body), "</script>") {
		t.Fatalf("placement without a marker should append: %s", body)
	}
	shell := []byte("<html><HEAD></HEAD><body><div></div></BODY></html>")
	if got := string(injectTrackingSnippet(shell, "<s/>", "body")); got != "<html><HEAD></HEAD><body><div></div><s/>\n</BODY></html>" {
		t.Fatalf("body placement: %s", got)
	}
	if got := string(injectTrackingSnippet(shell, "<s/>", "head")); got != "<html><HEAD><s/>\n</HEAD><body><div></div></BODY></html>" {
		t.Fatalf("head placement: %s", got)
	}
	// Two requests, two nonces.
	if again := scriptNonce.FindStringSubmatch(server.request(http.MethodGet, "/", nil, nil).Body.String()); again == nil || again[1] == match[1] {
		t.Fatal("the nonce must change per request")
	}
	// Static files and API answers do not carry it.
	if w := server.request(http.MethodGet, "/api/v1/version", nil, nil); strings.Contains(w.Body.String(), "<script") || w.Header().Get("Content-Security-Policy") != apiPolicy {
		t.Fatalf("API answer changed: %s", w.Header().Get("Content-Security-Policy"))
	}

	// The proxy forwards the loader and the events, without our cookie.
	r := httptest.NewRequest(http.MethodGet, "/momento/tracker.js", nil)
	r.AddCookie(server.admin)
	r.RemoteAddr = "10.1.2.3:4444"
	rec := httptest.NewRecorder()
	server.app.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || rec.Body.String() != "window.__momento=1" {
		t.Fatalf("proxy: %d %s", rec.Code, rec.Body.String())
	}
	post := server.request(http.MethodPost, "/momento/collect/v1/events?x=1", map[string]any{"events": []string{}}, nil)
	if post.Code != http.StatusOK {
		t.Fatalf("proxy post: %d %s", post.Code, post.Body.String())
	}
	if len(proxied) != 2 || !strings.HasPrefix(proxied[0], "GET /tracker.js cookie= ") || !strings.Contains(proxied[0], "xff=10.1.2.3") || !strings.HasPrefix(proxied[1], "POST /collect/v1/events?x=1 cookie= ") {
		t.Fatalf("what the collector saw: %q", proxied)
	}
	if !strings.Contains(proxied[0], "host="+strings.TrimPrefix(collector.URL, "http://")) {
		t.Fatalf("the collector should see its own host: %q", proxied[0])
	}
	// A collector that is down is a 502 with a reason, not a hang.
	collector.Close()
	if w := server.request(http.MethodGet, "/momento/tracker.js", nil, nil); w.Code != http.StatusBadGateway || !strings.Contains(refusal(t, w), "MOMENTO_UNAVAILABLE") {
		t.Fatalf("dead collector: %d %s", w.Code, w.Body.String())
	}

	// What the browser refused is recorded, once per origin, and shown.
	report := `{"csp-report":{"blocked-uri":"https://pixel.corp.example/p.gif?id=1","effective-directive":"img-src","document-uri":"https://weekly.corp.example/"}}`
	for i := 0; i < 3; i++ {
		if w := server.request(http.MethodPost, cspReportPath, json.RawMessage(report), nil); w.Code != http.StatusNoContent {
			t.Fatalf("report: %d", w.Code)
		}
	}
	// Malformed and empty reports are swallowed the same way.
	for _, raw := range []string{"not json", ""} {
		r := httptest.NewRequest(http.MethodPost, cspReportPath, strings.NewReader(raw))
		rec := httptest.NewRecorder()
		server.app.Handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%q: %d", raw, rec.Code)
		}
	}
	if w := server.request(http.MethodPost, cspReportPath, json.RawMessage(`{"csp-report":{"blocked-uri":"https://old.example/x","violated-directive":"script-src 'self'"}}`), nil); w.Code != http.StatusNoContent {
		t.Fatalf("legacy field: %d", w.Code)
	}
	var listed struct {
		Data struct {
			Items []trackingViolation `json:"items"`
		} `json:"data"`
	}
	w = server.request(http.MethodGet, "/api/v1/admin/tracking/violations", nil, server.admin)
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if len(listed.Data.Items) != 2 || listed.Data.Items[1].Origin != "https://pixel.corp.example" || listed.Data.Items[1].Count != 3 || listed.Data.Items[1].Allowed || listed.Data.Items[0].Directive != "script-src" {
		t.Fatalf("listed: %+v", listed.Data.Items)
	}
	// One click allows it, and the list then says so.
	if w := server.request(http.MethodPost, "/api/v1/admin/tracking/allow", map[string]string{"origin": "ftp://nope"}, server.admin); w.Code != http.StatusBadRequest {
		t.Fatalf("a non-http origin: %d", w.Code)
	}
	if w := server.request(http.MethodPost, "/api/v1/admin/tracking/allow", map[string]string{"origin": "https://pixel.corp.example/p.gif"}, server.admin); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"allowedHosts":"https://pixel.corp.example"`) {
		t.Fatalf("allow: %d %s", w.Code, w.Body.String())
	}
	w = server.request(http.MethodGet, "/api/v1/admin/tracking/violations", nil, server.admin)
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if !listed.Data.Items[1].Allowed {
		t.Fatalf("after allowing: %+v", listed.Data.Items)
	}
	if policy := server.request(http.MethodGet, "/", nil, nil).Header().Get("Content-Security-Policy"); !strings.Contains(policy, "img-src 'self' data: https://pixel.corp.example") {
		t.Fatalf("the allowed host should be in the page policy: %s", policy)
	}
	// The list is too long to add to: refused, not truncated.
	if w := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{"tracking.allowed_hosts": "https://" + strings.Repeat("h", 3980) + ".example"}}, server.admin); w.Code != http.StatusOK {
		t.Fatalf("fill the list: %d %s", w.Code, w.Body.String())
	}
	if w := server.request(http.MethodPost, "/api/v1/admin/tracking/allow", map[string]string{"origin": "https://one-more.example"}, server.admin); w.Code != http.StatusBadRequest {
		t.Fatalf("a full list: %d %s", w.Code, w.Body.String())
	}
	if w := server.request(http.MethodDelete, "/api/v1/admin/tracking/violations", nil, server.admin); w.Code != http.StatusOK {
		t.Fatalf("clear: %d", w.Code)
	}
	w = server.request(http.MethodGet, "/api/v1/admin/tracking/violations", nil, server.admin)
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Data.Items) != 0 {
		t.Fatalf("cleared: %+v", listed.Data.Items)
	}
	// Only an administrator sees or changes any of it.
	member := server.createUser("tracking_member", "USER", nil)
	for _, call := range []struct{ method, path string }{{http.MethodGet, "/api/v1/admin/tracking/violations"}, {http.MethodDelete, "/api/v1/admin/tracking/violations"}, {http.MethodPost, "/api/v1/admin/tracking/allow"}} {
		if w := server.request(call.method, call.path, map[string]string{"origin": "https://x.example"}, member); w.Code != http.StatusForbidden {
			t.Fatalf("%s %s by a member: %d", call.method, call.path, w.Code)
		}
	}

	// Off again: the policy is the old one, byte for byte, and the proxy is closed.
	if w := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{"tracking.enabled": "false"}}, server.admin); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	page = server.request(http.MethodGet, "/", nil, nil)
	if strings.Contains(page.Body.String(), "<script") || page.Header().Get("Content-Security-Policy") != basePagePolicy {
		t.Fatalf("off, the page changed: %s / %s", page.Body.String(), page.Header().Get("Content-Security-Policy"))
	}
	if w := server.request(http.MethodGet, "/momento/tracker.js", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("proxy after disabling: %d", w.Code)
	}
}
