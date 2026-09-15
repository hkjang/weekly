package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A sending service, as far as the receiving side can tell: it answers the
// claim path with whatever the test wants to see arrive.
type fakeSender struct {
	*httptest.Server
	hits    atomic.Int32
	lastURL atomic.Value
	handler func(w http.ResponseWriter, r *http.Request)
}

func newFakeSender(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *fakeSender {
	t.Helper()
	sender := &fakeSender{handler: handler}
	sender.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sender.hits.Add(1)
		sender.lastURL.Store(r.URL.String())
		sender.handler(w, r)
	}))
	t.Cleanup(sender.Close)
	return sender
}

func servesPPTX(body []byte, disposition string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", pptxContentType)
		if disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		_, _ = w.Write(body)
	}
}

func enableHandoff(t *testing.T, server *testServer, origins string) {
	t.Helper()
	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": "https://ai.internal/v1", "ai.model": "weekly-1",
			"import.max_file_mb": "1", "handoff.allowed_origins": origins,
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("설정을 저장하지 못했습니다: %d %s", on.Code, on.Body.String())
	}
}

func receive(server *testServer, cookie *http.Cookie, source, claim string) *httptest.ResponseRecorder {
	return server.request(http.MethodPost, "/api/v1/handoff/receive", map[string]string{"source": source, "claim": claim}, cookie)
}

// guards: receiveHandoff, handoffAllowedOrigins, normalizeOrigin
//
// The allow-list is the whole point. A source outside it is refused before a
// single byte goes out, because a service that fetches whatever origin it is
// handed reads the intranet on behalf of anyone with a session.
func TestAHandoffFromAnOriginOutsideTheListIsRefusedWithoutARequest(t *testing.T) {
	server := newTestServer(t)
	person := server.createUser("handoff_gate", "USER", nil)
	sender := newFakeSender(t, servesPPTX([]byte("PK deck"), ""))

	// Empty list: the default, and the feature does not exist.
	enableHandoff(t, server, "")
	if reply := receive(server, person, sender.URL, "abc123"); reply.Code != http.StatusForbidden || !strings.Contains(reply.Body.String(), "HANDOFF_SOURCE_NOT_ALLOWED") {
		t.Fatalf("빈 허용 목록인데 거절하지 않았습니다: %d %s", reply.Code, reply.Body.String())
	}
	// A list that names somebody else, a scheme that differs, and a value that
	// is not an origin at all.
	enableHandoff(t, server, "https://ptium.intra, https://umm.intra")
	for _, source := range []string{sender.URL, strings.Replace(sender.URL, "http://", "https://", 1)} {
		if reply := receive(server, person, source, "abc123"); reply.Code != http.StatusForbidden {
			t.Fatalf("%s: 목록 밖인데 %d %s", source, reply.Code, reply.Body.String())
		}
	}
	if reply := receive(server, person, sender.URL+"/api/v1/handoff/claims/abc123", "abc123"); reply.Code != http.StatusBadRequest {
		t.Fatalf("경로가 붙은 source 를 오리진으로 받았습니다: %d %s", reply.Code, reply.Body.String())
	}
	if got := sender.hits.Load(); got != 0 {
		t.Fatalf("허용되지 않은 오리진에 요청이 %d번 나갔습니다", got)
	}

	// Named — with a different case and a trailing slash, which are the same
	// origin — the fetch happens, at the claim path the standard names.
	enableHandoff(t, server, "https://ptium.intra,\n"+strings.ToUpper(sender.URL)+"/")
	reply := receive(server, person, sender.URL, "abc123")
	if reply.Code != http.StatusAccepted {
		t.Fatalf("허용된 오리진인데 %d %s", reply.Code, reply.Body.String())
	}
	if got := sender.hits.Load(); got != 1 {
		t.Fatalf("요청이 %d번 나갔습니다, want 1", got)
	}
	if got := sender.lastURL.Load(); got != "/api/v1/handoff/claims/abc123" {
		t.Fatalf("표를 받으러 간 경로가 다릅니다: %v", got)
	}
}

// guards: receiveHandoff
//
// The fetch is bounded the way the standard says: no redirect, PPTX only, a
// byte limit that cuts the stream rather than reading it all, and the
// sender's 404 passed on without guessing why.
func TestAHandoffFetchDoesNotFollowRedirectsOrReadPastTheLimit(t *testing.T) {
	server := newTestServer(t)
	person := server.createUser("handoff_bounds", "USER", nil)

	elsewhere := newFakeSender(t, servesPPTX([]byte("PK elsewhere"), ""))
	mode := "redirect"
	sender := newFakeSender(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "redirect":
			http.Redirect(w, r, elsewhere.URL+"/api/v1/handoff/claims/x", http.StatusFound)
		case "gone":
			w.WriteHeader(http.StatusNotFound)
		case "markdown":
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = w.Write([]byte("# not a deck"))
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
		case "declared-large":
			w.Header().Set("Content-Type", pptxContentType)
			w.Header().Set("Content-Length", "9000000")
			_, _ = w.Write(make([]byte, 9000000))
		case "streamed-large":
			w.Header().Set("Content-Type", pptxContentType)
			chunk := make([]byte, 64<<10)
			for i := 0; i < 128; i++ { // 8MB against a 1MB limit
				if _, err := w.Write(chunk); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	})
	enableHandoff(t, server, sender.URL)

	cases := []struct {
		mode string
		code int
		text string
	}{
		{"redirect", http.StatusBadGateway, "HANDOFF_REDIRECTED"},
		{"gone", http.StatusNotFound, "HANDOFF_CLAIM_REJECTED"},
		{"markdown", http.StatusUnsupportedMediaType, "HANDOFF_UNSUPPORTED_FORMAT"},
		{"broken", http.StatusBadGateway, "HANDOFF_SOURCE_FAILED"},
		{"declared-large", http.StatusRequestEntityTooLarge, "HANDOFF_TOO_LARGE"},
		{"streamed-large", http.StatusRequestEntityTooLarge, "HANDOFF_TOO_LARGE"},
	}
	for _, c := range cases {
		mode = c.mode
		reply := receive(server, person, sender.URL, "claim-"+c.mode)
		if reply.Code != c.code || !strings.Contains(reply.Body.String(), c.text) {
			t.Errorf("%s: got %d %s, want %d %s", c.mode, reply.Code, reply.Body.String(), c.code, c.text)
		}
	}
	if got := elsewhere.hits.Load(); got != 0 {
		t.Errorf("리다이렉트를 따라갔습니다: %d번", got)
	}
	var files int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM import_files`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Errorf("거절된 넘겨받기가 파일 기록 %d개를 남겼습니다", files)
	}
}

// guards: receiveHandoff, stageImportBody, handoffFilename
//
// What arrives is an import job like any other, and it remembers where it came
// from. The claim is written nowhere.
func TestAReceivedHandoffBecomesAnImportJobThatRemembersItsSource(t *testing.T) {
	server := newTestServer(t)
	person := server.createUser("handoff_source", "USER", nil)
	sender := newFakeSender(t, servesPPTX([]byte("PK deck bytes"), "attachment; filename*=UTF-8''2026%EB%85%84%203%EB%B6%84%EA%B8%B0.pptx"))
	enableHandoff(t, server, sender.URL)

	reply := receive(server, person, sender.URL, "secret-claim-value")
	if reply.Code != http.StatusAccepted {
		t.Fatalf("넘겨받기 실패: %d %s", reply.Code, reply.Body.String())
	}
	if body := reply.Body.String(); !strings.Contains(body, "2026년 3분기.pptx") || strings.Contains(body, "secret-claim-value") {
		t.Fatalf("응답이 파일 이름을 말하지 않거나 표를 되돌려 줍니다: %s", body)
	}
	var source, name string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT coalesce(handoff_source,''), original_filename FROM import_files ORDER BY id DESC LIMIT 1`).Scan(&source, &name); err != nil {
		t.Fatal(err)
	}
	if source != strings.ToLower(sender.URL) || name != "2026년 3분기.pptx" {
		t.Fatalf("출처·이름이 남지 않았습니다: source=%q name=%q", source, name)
	}
	var audited int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM audit_logs WHERE action='handoff.receive' AND detail->>'source'=$1 AND detail::text NOT LIKE '%secret-claim-value%'`, strings.ToLower(sender.URL)).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Fatalf("감사 로그에 출처가 남지 않았거나 표가 적혔습니다: %d", audited)
	}
	if server.logged("secret-claim-value") {
		t.Fatal("표가 서버 로그에 남았습니다")
	}
	// And the screen sees it on the job.
	var jobID int64
	if err := server.app.db.QueryRow(server.ctx(), `SELECT import_job_id FROM import_files ORDER BY id DESC LIMIT 1`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	job := server.request(http.MethodGet, "/api/v1/import/"+itoa(jobID), nil, person)
	if !strings.Contains(job.Body.String(), `"handoffSource":"`+strings.ToLower(sender.URL)+`"`) {
		t.Fatalf("작업 상세에 출처가 없습니다: %s", job.Body.String())
	}
	// A direct upload carries no source.
	upload := server.upload("/api/v1/import/pptx", "files", "직접올림.pptx", []byte("PK other deck"), person)
	if upload.Code != http.StatusAccepted {
		t.Fatalf("직접 업로드 실패: %d %s", upload.Code, upload.Body.String())
	}
	if err := server.app.db.QueryRow(server.ctx(), `SELECT coalesce(handoff_source,'') FROM import_files WHERE original_filename='직접올림.pptx'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "" {
		t.Fatalf("직접 올린 파일에 출처가 붙었습니다: %q", source)
	}

	for input, want := range map[string]string{
		"":                                   "handoff.pptx",
		"attachment; filename=\"deck.pptx\"": "deck.pptx",
		"attachment; filename=\"../../etc/x.pptx\"": "x.pptx",
		"attachment; filename=\"notes.md\"":         "notes.md.pptx",
		"attachment; filename=\"DECK.PPTX\"":        "DECK.PPTX",
		"garbage":                                   "handoff.pptx",
	} {
		if got := handoffFilename(input); got != want {
			t.Errorf("handoffFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

// guards: handoffEntry
//
// The browser entry point sends the claim into the fragment, where the SPA
// keeps it through the login screen and no request line ever carries it.
func TestTheHandoffEntryPointForwardsToTheImportScreen(t *testing.T) {
	server := newTestServer(t)
	reply := server.request(http.MethodGet, "/handoff?source=https://ptium.intra&claim=abc", nil, nil)
	if reply.Code != http.StatusFound {
		t.Fatalf("got %d, want 302", reply.Code)
	}
	if location := reply.Header().Get("Location"); location != "/#/import?claim=abc&source=https%3A%2F%2Fptium.intra" {
		t.Fatalf("Location = %q", location)
	}
	if reply.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", reply.Header().Get("Cache-Control"))
	}
}

// guards: validOriginList, normalizeOrigin
func TestTheAllowedOriginsSettingAcceptsOnlyBareOrigins(t *testing.T) {
	for value, want := range map[string]bool{
		"":                     true,
		"https://ptium.intra":  true,
		"https://ptium.intra/": true,
		"https://ptium.intra, http://umm.intra:8080\nhttps://muni.intra;": true,
		"HTTPS://Ptium.Intra":                true,
		"https://ptium.intra/handoff":        false,
		"https://ptium.intra?x=1":            false,
		"https://ptium.intra#x":              false,
		"https://user:pw@ptium.intra":        false,
		"ftp://ptium.intra":                  false,
		"ptium.intra":                        false,
		"https://":                           false,
		"https://ptium.intra, not an origin": false,
	} {
		if got := validOriginList(value); got != want {
			t.Errorf("validOriginList(%q) = %v, want %v", value, got, want)
		}
	}
	if origin, ok := normalizeOrigin(" HTTPS://Ptium.Intra:8443/ "); !ok || origin != "https://ptium.intra:8443" {
		t.Errorf("normalizeOrigin = %q %v", origin, ok)
	}
}
