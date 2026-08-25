package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

// A harness that serves requests through the same mux the binary does.
//
// 196 handlers had no coverage at all, and every permission rule, error code and
// status this product returns lives in one of them. Checking those by starting a
// container and driving it by hand is what happened instead — which means it
// happened when somebody remembered to, and only for the path they were
// thinking about.
type testServer struct {
	t   *testing.T
	app *App
	// admin is the bootstrap administrator's session cookie.
	admin     *http.Cookie
	adminName string
	// logs holds everything the app wrote while the test ran. Some behaviour is
	// only visible here: work the product does on its own, and failures it
	// deliberately survives rather than reporting to the caller.
	logs *bytes.Buffer
}

// logged reports whether any line written so far contains every fragment.
func (s *testServer) logged(fragments ...string) bool {
	text := s.logs.String()
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			return false
		}
	}
	return true
}

// harnessRun makes every account this file creates unique to one `go test`, so
// a suite that leaves a row behind still runs the next time. Cleanup below is
// best effort; this is what makes the tests not depend on it.
var harnessRun = time.Now().UnixNano() % 1e9

var harnessAccounts atomic.Int64

// accountName turns a readable stem into a name no earlier run can collide with.
func accountName(stem string) string {
	return fmt.Sprintf("%s_%d_%d", stem, harnessRun, harnessAccounts.Add(1))
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	dsn := os.Getenv("WEEKLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEEKLY_TEST_POSTGRES_DSN is not configured")
	}
	// Each server gets its own database. Sharing one made the suite depend on
	// its own cleanup: the bootstrap administrator is only created when no
	// administrator exists, so a single leftover row turned every later run into
	// a wall of INVALID_CREDENTIALS.
	dsn = createScratchDatabase(t, dsn)

	// The three directories are derived from stateDirectory at package init, so
	// each has to be pointed somewhere writable on its own.
	workspace := t.TempDir()
	state, attachments, imports := stateDirectory, stateDirectoryAttachments, importDirectory
	stateDirectory = workspace
	stateDirectoryAttachments = workspace + "/attachments"
	importDirectory = workspace + "/imports"
	t.Cleanup(func() {
		stateDirectory, stateDirectoryAttachments, importDirectory = state, attachments, imports
	})

	t.Setenv("WEEKLY_POSTGRES_DSN", dsn)
	adminName := accountName("harnessadmin")
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN", adminName)
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD", "HarnessAdmin1234")
	t.Setenv("WEEKLY_ALLOW_SECRET_RESET", "true")

	ctx, cancel := context.WithCancel(context.Background())
	logs := &bytes.Buffer{}
	app, err := New(ctx, Options{
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
		Web:    fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}},
		Build:  BuildInfo{Version: "test"},
	})
	if err != nil {
		cancel()
		t.Fatalf("build the app: %v", err)
	}
	t.Cleanup(func() { cancel(); app.Close() })

	server := &testServer{t: t, app: app, logs: logs}
	server.adminName = adminName
	server.admin = server.signIn(adminName, "HarnessAdmin1234")
	return server
}

// request issues one request through the mux. Origin is set to the request host
// so session calls pass the CSRF check; tests that need it absent say so.
func (s *testServer) request(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	s.t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	r := httptest.NewRequest(method, path, payload)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Origin", "http://"+r.Host)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.app.mux.ServeHTTP(w, r)
	return w
}

// createOrganization adds an organisation and returns its id.
func (s *testServer) createOrganization(name, code string) int64 {
	return s.createChildOrganization(name, code, nil)
}

// createChildOrganization hangs an organisation under another, which is how a
// real deployment is shaped and how the visibility predicate's recursive lookup
// gets more than one level to walk.
func (s *testServer) createChildOrganization(name, code string, parent *int64) int64 {
	s.t.Helper()
	unique := fmt.Sprintf("%s%d", code, harnessAccounts.Add(1))
	body := map[string]any{"name": name, "code": unique}
	if parent != nil {
		body["parentId"] = *parent
	}
	w := s.request(http.MethodPost, "/api/v1/admin/organizations", body, s.admin)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		s.t.Fatalf("create organisation %s: %d %s", unique, w.Code, w.Body.String())
	}
	id, _ := decodeData(s.t, w)["id"].(float64)
	if id == 0 {
		s.t.Fatalf("organisation %s has no id: %s", unique, w.Body.String())
	}
	return int64(id)
}

// upload posts one file as multipart/form-data through the mux.
func (s *testServer) upload(path, field, filename string, body []byte, cookie *http.Cookie) *httptest.ResponseRecorder {
	return s.uploadMany(path, field, map[string][]byte{filename: body}, cookie)
}

// uploadMany posts several files in one request, in sorted name order so a
// failure names the same file every run.
func (s *testServer) uploadMany(path, field string, files map[string][]byte, cookie *http.Cookie) *httptest.ResponseRecorder {
	s.t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for _, name := range names {
		part, err := writer.CreateFormFile(field, name)
		if err != nil {
			s.t.Fatal(err)
		}
		if _, err := part.Write(files[name]); err != nil {
			s.t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		s.t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, &buffer)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Origin", "http://"+r.Host)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.app.mux.ServeHTTP(w, r)
	return w
}

// attemptLogin posts credentials and returns the response without asserting.
func (s *testServer) attemptLogin(username, password string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password}, nil)
}

// bearer issues a request authenticated by a personal API key rather than a
// session, which is the other way into every read endpoint.
func (s *testServer) bearer(method, path, token string) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.app.mux.ServeHTTP(w, r)
	return w
}

// lastCreatedUsername and passwordFor recover the decorated name and password
// createUser generated, so a test can sign the same person in again.
func (s *testServer) lastCreatedUsername(stem string) string {
	s.t.Helper()
	var username string
	if err := s.app.db.QueryRow(s.ctx(),
		`SELECT username FROM users WHERE username LIKE $1 ORDER BY id DESC LIMIT 1`, stem+"_%").Scan(&username); err != nil {
		s.t.Fatal(err)
	}
	return username
}

func (s *testServer) passwordFor(stem string) string {
	return s.lastCreatedUsername(stem) + "Password1234"
}

func (s *testServer) ctx() context.Context { return context.Background() }

func (s *testServer) signIn(username, password string) *http.Cookie {
	s.t.Helper()
	w := s.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password}, nil)
	if w.Code != http.StatusOK {
		s.t.Fatalf("sign in as %s: %d %s", username, w.Code, w.Body.String())
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookie {
			return cookie
		}
	}
	s.t.Fatalf("sign in as %s returned no session cookie", username)
	return nil
}

// createUser adds a user through the administrator API and signs them in. The
// stem is decorated so the account cannot collide with an earlier run.
func (s *testServer) createUser(stem, role string, organizationID *int64) *http.Cookie {
	s.t.Helper()
	username := accountName(stem)
	password := username + "Password1234"
	body := map[string]any{"username": username, "displayName": username, "role": role, "password": password}
	if organizationID != nil {
		body["organizationId"] = *organizationID
	}
	w := s.request(http.MethodPost, "/api/v1/admin/users", body, s.admin)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		s.t.Fatalf("create %s: %d %s", username, w.Code, w.Body.String())
	}
	return s.signIn(username, password)
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return envelope.Data
}

func errorCode(w *httptest.ResponseRecorder) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	return envelope.Error.Code
}

// createScratchDatabase makes an empty database beside the configured one and
// drops it when the test ends.
func createScratchDatabase(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse the test DSN: %v", err)
	}
	name := fmt.Sprintf("weekly_h_%d_%d", harnessRun, harnessAccounts.Add(1))

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to create %s: %v", name, err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create %s: %v", name, err)
	}
	_ = admin.Close(ctx)

	t.Cleanup(func() {
		dropper, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer dropper.Close(context.Background())
		_, _ = dropper.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	scratch := *parsed
	scratch.Path = "/" + name
	return scratch.String()
}

var _ = fmt.Sprint
