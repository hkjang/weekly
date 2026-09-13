package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// 방문 추적 스크립트. An administrator pastes or picks a tracker in 관리자 설정
// and it goes out with the application shell.
//
// The hard part is not the <script> tag. Every page here is served under
// script-src 'self', so a pasted snippet is refused by the browser without a
// word to the person who pasted it. This file produces both halves of the
// answer: the markup, with a per-request nonce on every script tag, and the
// policy that names that nonce and the origins the snippet loads from. The
// policy is never loosened with 'unsafe-inline' — that would stay loose after
// tracking is switched off again.
//
// Momento comes first. It is the in-house collector, the only choice whose
// data stays inside, and it can be reached through this process (/momento/*)
// so that no outside origin has to appear in the policy at all.

const (
	trackingProviderNone    = "none"
	trackingProviderMomento = "momento"
	trackingProviderGA4     = "ga4"
	trackingProviderGTM     = "gtm"
	trackingProviderMatomo  = "matomo"
	trackingProviderCustom  = "custom"

	// trackingMaxSnippetBytes bounds a pasted snippet. A tracker loader is a
	// few hundred bytes; anything past this is not one.
	trackingMaxSnippetBytes = 8 * 1024

	// momentoProxyPrefix is the same-origin path handed to the collector, so
	// the tracker's requests never leave this origin as far as the browser can
	// tell. Both the loader and the events go through it.
	momentoProxyPrefix = "/momento/"

	// cspReportPath is where browsers post what the policy refused. It is
	// unauthenticated because the browser sends it without credentials, and it
	// stores nothing but a bounded list of origins in memory.
	cspReportPath = "/api/v1/tracking/csp-report"

	// maxCSPReportBytes keeps an unauthenticated endpoint from being handed
	// large bodies.
	maxCSPReportBytes = 8 * 1024

	// trackingMaxViolations bounds the recorder. A blocked request repeats on
	// every page view, so what matters is which origins are blocked, not how
	// many times — a short list of distinct origins is enough to fix a snippet.
	trackingMaxViolations = 100
)

// basePagePolicy is the policy every page carried before tracking existed and
// still carries while it is off. It is built from parts below; this constant is
// what a fresh install must keep producing byte for byte.
const basePagePolicy = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// apiPolicy is for answers nobody renders as a page: JSON, downloads, the
// probes. Narrower than the shell's policy on purpose — there is no script to
// allow — with img-src kept because attachments are served inline and a
// browser showing one in its own tab applies this policy to that picture.
const apiPolicy = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'"

type trackingConfig struct {
	Enabled       bool
	Provider      string
	MomentoURL    string
	MomentoSiteID string
	// MomentoDirect loads the tracker from the collector's own origin instead
	// of through /momento/*. Off by default: through the proxy the policy stays
	// 'self' and nothing has to be allowed.
	MomentoDirect bool
	MeasurementID string
	MatomoURL     string
	MatomoSiteID  string
	CustomSnippet string
	AllowedHosts  string
	Placement     string
}

// readTrackingConfig maps stored settings onto the configuration. Anything
// unset reads as the default, and the default is off.
func readTrackingConfig(values map[string]string) trackingConfig {
	get := func(key string) string { return strings.TrimSpace(values["tracking."+key]) }
	config := trackingConfig{
		Enabled:       get("enabled") == "true",
		Provider:      strings.ToLower(get("provider")),
		MomentoURL:    get("momento_url"),
		MomentoSiteID: get("momento_site_id"),
		MomentoDirect: get("momento_endpoint") == "DIRECT",
		MeasurementID: get("measurement_id"),
		MatomoURL:     get("matomo_url"),
		MatomoSiteID:  get("matomo_site_id"),
		CustomSnippet: strings.TrimSpace(values["tracking.custom_snippet"]),
		AllowedHosts:  get("allowed_hosts"),
		Placement:     strings.ToLower(get("placement")),
	}
	if config.Provider == "" {
		config.Provider = trackingProviderNone
	}
	if config.Placement != "body" {
		config.Placement = "head"
	}
	return config
}

// trackingConfig reads the tracking settings in one query. A failure reads as
// "no tracking": a settings outage must never keep the shell from loading, and
// a page without a tracker is the same page a fresh install serves.
func (a *App) trackingConfig(ctx context.Context) trackingConfig {
	ctx, cancel := context.WithTimeout(ctx, settingWait)
	defer cancel()
	rows, err := a.db.Query(ctx, `SELECT key,value FROM app_settings WHERE key LIKE 'tracking.%' AND secret=false`)
	if err != nil {
		return trackingConfig{}
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return trackingConfig{}
		}
		values[key] = value
	}
	if rows.Err() != nil {
		return trackingConfig{}
	}
	return readTrackingConfig(values)
}

// active reports whether the shell should carry the snippet at all.
func (c trackingConfig) active() bool {
	if !c.Enabled || c.Provider == trackingProviderNone {
		return false
	}
	return strings.TrimSpace(c.snippet("")) != ""
}

// validate says what is missing for the chosen provider, in the words the
// settings screen shows. Nothing is required while tracking is off.
func (c trackingConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	switch c.Provider {
	case trackingProviderNone:
		return nil
	case trackingProviderMomento:
		if c.MomentoURL == "" || c.MomentoSiteID == "" {
			return fmt.Errorf("Momento 수집기 주소와 사이트 ID를 입력한 뒤 방문 추적을 켜세요.")
		}
		if !validOptionalURL(c.MomentoURL) {
			return fmt.Errorf("Momento 수집기 주소가 올바른 http(s) 주소가 아닙니다.")
		}
	case trackingProviderGA4, trackingProviderGTM:
		if c.MeasurementID == "" {
			return fmt.Errorf("측정 ID를 입력한 뒤 방문 추적을 켜세요.")
		}
	case trackingProviderMatomo:
		if c.MatomoURL == "" || c.MatomoSiteID == "" {
			return fmt.Errorf("Matomo 주소와 사이트 ID를 입력한 뒤 방문 추적을 켜세요.")
		}
		if !validOptionalURL(c.MatomoURL) {
			return fmt.Errorf("Matomo 주소가 올바른 http(s) 주소가 아닙니다.")
		}
	case trackingProviderCustom:
		if c.CustomSnippet == "" {
			return fmt.Errorf("추적 코드를 붙여 넣은 뒤 방문 추적을 켜세요.")
		}
	default:
		return fmt.Errorf("추적 도구는 none, momento, ga4, gtm, matomo, custom 중 하나여야 합니다.")
	}
	return nil
}

// momentoProxyTarget is the collector this process forwards /momento/* to, or
// nil when nothing should be forwarded — tracking off, another provider, or an
// installation that chose to load the tracker directly.
func (c trackingConfig) momentoProxyTarget() *url.URL {
	if !c.Enabled || c.Provider != trackingProviderMomento || c.MomentoDirect {
		return nil
	}
	target, err := url.Parse(strings.TrimRight(c.MomentoURL, "/"))
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil
	}
	return target
}

// snippet renders the markup to inject. The nonce goes on every script tag so
// the policy can stay strict.
func (c trackingConfig) snippet(nonce string) string {
	switch c.Provider {
	case trackingProviderMomento:
		site := html.EscapeString(c.MomentoSiteID)
		base := strings.TrimRight(c.MomentoURL, "/")
		if site == "" || base == "" {
			return ""
		}
		if c.MomentoDirect {
			return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-site-id="%s" data-environment="prd" data-contract-version="1"></script>`,
				html.EscapeString(base), site), nonce)
		}
		// Through the proxy both the loader and the events stay on this
		// origin: data-endpoint tells the tracker where to post.
		endpoint := strings.TrimSuffix(momentoProxyPrefix, "/")
		return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-site-id="%s" data-environment="prd" data-contract-version="1" data-endpoint="%s"></script>`,
			endpoint, site, endpoint), nonce)
	case trackingProviderGA4:
		id := html.EscapeString(c.MeasurementID)
		if id == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script async src="https://www.googletagmanager.com/gtag/js?id=%s"></script>
<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','%s');</script>`, id, id), nonce)
	case trackingProviderGTM:
		id := html.EscapeString(c.MeasurementID)
		if id == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>(function(w,d,s,l,i){w[l]=w[l]||[];w[l].push({'gtm.start':new Date().getTime(),event:'gtm.js'});var f=d.getElementsByTagName(s)[0],j=d.createElement(s),dl=l!='dataLayer'?'&l='+l:'';j.async=true;j.src='https://www.googletagmanager.com/gtm.js?id='+i+dl;f.parentNode.insertBefore(j,f);})(window,document,'script','dataLayer','%s');</script>`, id), nonce)
	case trackingProviderMatomo:
		base := strings.TrimRight(c.MatomoURL, "/")
		site := html.EscapeString(c.MatomoSiteID)
		if base == "" || site == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>var _paq=window._paq=window._paq||[];_paq.push(['trackPageView']);_paq.push(['enableLinkTracking']);(function(){var u="%s/";_paq.push(['setTrackerUrl',u+'matomo.php']);_paq.push(['setSiteId','%s']);var d=document,g=d.createElement('script'),s=d.getElementsByTagName('script')[0];g.async=true;g.src=u+'matomo.js';s.parentNode.insertBefore(g,s);})();</script>`, html.EscapeString(base), site), nonce)
	case trackingProviderCustom:
		return withNonce(c.CustomSnippet, nonce)
	}
	return ""
}

// policySources lists the origins the snippet needs beyond 'self'. Through the
// Momento proxy that list is empty, which is the point of the proxy.
func (c trackingConfig) policySources() (scripts, connects, images []string) {
	add := func(origin string) {
		scripts = append(scripts, origin)
		connects = append(connects, origin)
		images = append(images, origin)
	}
	switch c.Provider {
	case trackingProviderMomento:
		if c.MomentoDirect {
			if origin := originOf(c.MomentoURL); origin != "" {
				add(origin)
			}
		}
	case trackingProviderGA4, trackingProviderGTM:
		scripts = append(scripts, "https://www.googletagmanager.com")
		connects = append(connects, "https://www.google-analytics.com", "https://analytics.google.com", "https://*.google-analytics.com")
		images = append(images, "https://www.google-analytics.com", "https://www.googletagmanager.com")
	case trackingProviderMatomo:
		if origin := originOf(c.MatomoURL); origin != "" {
			add(origin)
		}
	case trackingProviderCustom:
		// A pasted snippet names the addresses it loads and reports to, so
		// those are allowed without anybody translating a console error into
		// a host name first.
		for _, origin := range snippetOrigins(c.CustomSnippet) {
			add(origin)
		}
	}
	for _, host := range splitHosts(c.AllowedHosts) {
		add(host)
	}
	return scripts, connects, images
}

func splitHosts(list string) []string {
	var hosts []string
	for _, host := range strings.FieldsFunc(list, func(letter rune) bool {
		return letter == ',' || letter == ' ' || letter == '\n' || letter == '\r' || letter == '\t'
	}) {
		if trimmed := strings.TrimSuffix(strings.TrimSpace(host), "/"); trimmed != "" {
			hosts = append(hosts, trimmed)
		}
	}
	return hosts
}

// trackingPolicy is the shell's policy for one request: the strict base, plus
// only what the configured tracker needs, plus the report address while
// tracking is on so a refusal can be shown to the administrator.
func trackingPolicy(config trackingConfig, nonce string) string {
	scripts := []string{"'self'"}
	connects := []string{"'self'"}
	images := []string{"'self'", "data:"}
	active := config.active()
	if active {
		extraScripts, extraConnects, extraImages := config.policySources()
		scripts = append(scripts, "'nonce-"+nonce+"'")
		scripts = append(scripts, extraScripts...)
		connects = append(connects, extraConnects...)
		images = append(images, extraImages...)
	}
	policy := "default-src 'self'; img-src " + strings.Join(images, " ") +
		"; style-src 'self' 'unsafe-inline'; script-src " + strings.Join(scripts, " ") +
		"; connect-src " + strings.Join(connects, " ") +
		"; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	if active {
		policy += "; report-uri " + cspReportPath
	}
	return policy
}

// newNonce is one request's script nonce: 128 random bits, base64.
func newNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// Without randomness there is no nonce worth the name, and a policy
		// naming an empty one allows nothing — which is the safe way to fail.
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// injectTrackingSnippet places the markup just before the closing tag it
// belongs to, or at the end when the shell has no such tag.
func injectTrackingSnippet(page []byte, snippet, placement string) []byte {
	marker := "</head>"
	if placement == "body" {
		marker = "</body>"
	}
	text := string(page)
	index := lastIndexFold(text, marker)
	if index < 0 {
		return []byte(text + "\n" + snippet + "\n")
	}
	return []byte(text[:index] + snippet + "\n" + text[index:])
}

// indexFold finds sub in s ignoring ASCII case, and returns an index into s.
//
// strings.ToLower is the obvious way and the wrong one: it changes byte lengths
// for some runes — U+212A KELVIN SIGN is three bytes and folds to a one-byte
// 'k', U+0130 'İ' is two and folds to three — so an index taken from the folded
// copy lands somewhere else in the original. One such letter in a snippet and
// the nonce is written into the middle of the tag name, which breaks the tag
// without saying so. Every needle here is ASCII, and folding only ASCII keeps
// every byte where it is.
func indexFold(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

func lastIndexFold(s, sub string) int {
	for i := len(s) - len(sub); i >= 0; i-- {
		if equalFoldASCII(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if foldASCII(a[i]) != foldASCII(b[i]) {
			return false
		}
	}
	return true
}

func foldASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// withNonce adds the nonce to every script tag that does not already carry
// one, which is what lets a pasted snippet run under a strict policy unchanged.
func withNonce(snippet, nonce string) string {
	if nonce == "" || snippet == "" {
		return snippet
	}
	var builder strings.Builder
	remaining := snippet
	for {
		index := indexFold(remaining, "<script")
		if index < 0 {
			builder.WriteString(remaining)
			return builder.String()
		}
		end := index + len("<script")
		builder.WriteString(remaining[:end])
		tag := remaining[end:]
		if closing := strings.IndexByte(tag, '>'); closing >= 0 {
			tag = tag[:closing]
		}
		if indexFold(tag, "nonce=") < 0 {
			builder.WriteString(` nonce="` + html.EscapeString(nonce) + `"`)
		}
		remaining = remaining[end:]
	}
}

// snippetOrigins lists every http(s) origin written into a snippet: the script
// it loads, the endpoint it posts to, the pixel it requests. A tracker almost
// always writes its own address into its loader, so reading them here is what
// keeps a pasted snippet working without the administrator reading a policy
// error first.
func snippetOrigins(snippet string) []string {
	var origins []string
	seen := map[string]struct{}{}
	for index := 0; index < len(snippet); {
		start := indexFold(snippet[index:], "http")
		if start < 0 {
			break
		}
		start += index
		end := start
		for end < len(snippet) && !isURLBoundary(snippet[end]) {
			end++
		}
		index = end
		origin := originOf(snippet[start:end])
		if origin == "" {
			continue
		}
		if _, duplicate := seen[origin]; duplicate {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins
}

// isURLBoundary reports the characters that cannot appear in an address written
// inside HTML or JavaScript, which is where each address ends.
func isURLBoundary(letter byte) bool {
	switch letter {
	case '"', '\'', '`', '<', '>', ' ', '\t', '\n', '\r', ')', ',', ';', '\\', '+':
		return true
	}
	return false
}

// originOf reduces an address to scheme://host[:port], or "" when it is not an
// http(s) address — a data: URL or a browser extension cannot be allowed and
// there is nothing to do with them.
func originOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(parsed.Host)
}

// trackingViolation is one origin the policy refused, kept with the directive
// that refused it so the screen can say what to allow.
type trackingViolation struct {
	Origin    string    `json:"origin"`
	Directive string    `json:"directive"`
	Page      string    `json:"page"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Allowed marks an origin the current settings already permit, so a fixed
	// snippet stops nagging without anybody clearing the list.
	Allowed bool `json:"allowed"`
}

// violationLog collects what browsers report. Deliberately in memory: the
// reports are a live troubleshooting aid for the person pasting a snippet, not
// an audit record, and keeping them out of the database means a browser can
// report freely without growing storage.
type violationLog struct {
	mutex sync.Mutex
	items map[string]*trackingViolation
	now   func() time.Time
}

func newViolationLog() *violationLog {
	return &violationLog{items: map[string]*trackingViolation{}, now: time.Now}
}

// record notes one blocked request. Reports that name no http origin are
// dropped: allowing them is neither possible nor useful.
func (v *violationLog) record(blockedURI, directive, page string) {
	origin := originOf(blockedURI)
	if origin == "" {
		return
	}
	directive = strings.ToLower(strings.TrimSpace(directive))
	if index := strings.IndexByte(directive, ' '); index > 0 {
		directive = directive[:index]
	}
	if directive == "" {
		directive = "connect-src"
	}
	v.mutex.Lock()
	defer v.mutex.Unlock()
	key := directive + " " + origin
	moment := v.now()
	if existing, found := v.items[key]; found {
		existing.Count++
		existing.LastSeen = moment
		existing.Page = page
		return
	}
	if len(v.items) >= trackingMaxViolations {
		v.evictOldest()
	}
	v.items[key] = &trackingViolation{Origin: origin, Directive: directive, Page: page, Count: 1, FirstSeen: moment, LastSeen: moment}
}

func (v *violationLog) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for key, item := range v.items {
		if oldestKey == "" || item.LastSeen.Before(oldest) {
			oldestKey, oldest = key, item.LastSeen
		}
	}
	delete(v.items, oldestKey)
}

// list returns the blocked origins, most recent first, marking the ones the
// configuration already allows.
func (v *violationLog) list(config trackingConfig) []trackingViolation {
	allowed := map[string]struct{}{}
	scripts, connects, images := config.policySources()
	for _, group := range [][]string{scripts, connects, images} {
		for _, origin := range group {
			allowed[strings.ToLower(strings.TrimSuffix(origin, "/"))] = struct{}{}
		}
	}
	v.mutex.Lock()
	defer v.mutex.Unlock()
	items := make([]trackingViolation, 0, len(v.items))
	for _, item := range v.items {
		copied := *item
		_, known := allowed[copied.Origin]
		copied.Allowed = known || matchesWildcardOrigin(copied.Origin, allowed)
		items = append(items, copied)
	}
	sort.Slice(items, func(first, second int) bool {
		if items[first].LastSeen.Equal(items[second].LastSeen) {
			return items[first].Origin < items[second].Origin
		}
		return items[first].LastSeen.After(items[second].LastSeen)
	})
	return items
}

// forget drops the recorded reports, which is how an administrator checks
// whether a change actually fixed the snippet.
func (v *violationLog) forget() {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	v.items = map[string]*trackingViolation{}
}

// matchesWildcardOrigin covers policy entries such as https://*.google-analytics.com.
func matchesWildcardOrigin(origin string, allowed map[string]struct{}) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	for pattern := range allowed {
		star := strings.Index(pattern, "*.")
		if star < 0 {
			continue
		}
		if strings.HasPrefix(origin, pattern[:star]) && strings.HasSuffix(parsed.Host, pattern[star+1:]) {
			return true
		}
	}
	return false
}

// addAllowedHost appends an origin to the comma separated allow list, leaving
// the existing entries and their order alone.
func addAllowedHost(existing, origin string) string {
	origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
	if origin == "" {
		return existing
	}
	for _, host := range splitHosts(existing) {
		if strings.EqualFold(host, origin) {
			return existing
		}
	}
	if strings.TrimSpace(existing) == "" {
		return origin
	}
	return strings.TrimSpace(existing) + ", " + origin
}

// receiveCSPReport records what a browser refused to load. Always 204: a
// misbehaving page must never see an error from us, and there is nothing a
// browser could do with one.
func (a *App) receiveCSPReport(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCSPReportBytes))
	if err != nil || len(body) == 0 {
		return
	}
	var report struct {
		Report struct {
			BlockedURI         string `json:"blocked-uri"`
			ViolatedDirective  string `json:"violated-directive"`
			EffectiveDirective string `json:"effective-directive"`
			DocumentURI        string `json:"document-uri"`
		} `json:"csp-report"`
	}
	if json.Unmarshal(body, &report) != nil {
		return
	}
	directive := report.Report.EffectiveDirective
	if directive == "" {
		directive = report.Report.ViolatedDirective
	}
	a.violations.record(report.Report.BlockedURI, directive, report.Report.DocumentURI)
}

// trackingViolations shows the administrator which addresses the policy is
// blocking, so a snippet can be fixed without reading the browser console.
func (a *App) trackingViolations(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, map[string]any{"items": a.violations.list(a.trackingConfig(r.Context()))})
}

func (a *App) clearTrackingViolations(w http.ResponseWriter, r *http.Request) {
	a.violations.forget()
	writeData(w, http.StatusOK, map[string]any{"cleared": true})
}

// allowTrackingHost adds one blocked origin to the allow list. It is the
// one-click fix for the reports listed above.
func (a *App) allowTrackingHost(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Origin string `json:"origin"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	origin := originOf(input.Origin)
	if origin == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ORIGIN", "허용할 주소는 http(s) 출처여야 합니다.")
		return
	}
	p := currentPrincipal(r.Context())
	hosts := addAllowedHost(a.setting(r.Context(), "tracking.allowed_hosts", ""), origin)
	if !settingDefinitions["tracking.allowed_hosts"].Validate(hosts) {
		writeError(w, http.StatusBadRequest, "INVALID_SETTING", "허용 목록이 너무 깁니다. 관리자 설정에서 정리한 뒤 다시 더하세요.")
		return
	}
	_, err := a.db.Exec(r.Context(), `INSERT INTO app_settings(key,value,secret,updated_by,updated_at) VALUES($1,$2,false,$3,now())
		ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,secret=false,updated_by=EXCLUDED.updated_by,updated_at=now()`, "tracking.allowed_hosts", hosts, p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "설정을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "settings.update", "settings", "global", map[string]any{"keys": []string{"tracking.allowed_hosts"}, "origin": origin})
	writeData(w, http.StatusOK, map[string]any{"allowedHosts": hosts})
}

// momentoProxy forwards /momento/* to the collector. The browser sees one
// origin; the collector sees the visitor's address in X-Forwarded-For. Nothing
// is forwarded unless Momento is the configured tracker and the proxy is the
// chosen path, so on every other installation this is a 404.
func (a *App) momentoProxy(w http.ResponseWriter, r *http.Request) {
	target := a.trackingConfig(r.Context()).momentoProxyTarget()
	if target == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Momento 프록시가 켜져 있지 않습니다.")
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(momentoProxyPrefix, "/"))
			request.Out.URL.RawPath = ""
			request.Out.Host = target.Host
			request.SetXForwarded()
			// A session cookie is for this service, not the collector.
			request.Out.Header.Del("Cookie")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			a.logger.Warn("momento proxy", "error", err, "trace", traceIDFromContext(r.Context()))
			writeError(w, http.StatusBadGateway, "MOMENTO_UNAVAILABLE", "Momento 수집기에 연결할 수 없습니다.")
		},
	}
	proxy.ServeHTTP(w, r)
}
