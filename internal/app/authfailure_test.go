package app

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Not being signed in and not being able to find out are different facts, and
// the product answered them the same way.
//
// Cutting PostgreSQL while somebody was part-way through a weekly report told
// them "로그인이 필요합니다" — the one action that cannot help — and, because the
// screen treats 401 as a lost session, offered them a fresh login tab. Taking
// it would have thrown away the report they were writing.
//
// So the sentinel exists to say which case it is, and every genuine "you are
// not signed in" path has to carry it.
// guards: authenticate
func TestOnlyGenuineSignOutIsMarkedAsNoSession(t *testing.T) {
	// These two branches answer before touching the database, so they are
	// checked wherever this runs.
	app := &App{}
	bearer := httptest.NewRequest(http.MethodGet, "/api/v1/reports", nil)
	bearer.Header.Set("Authorization", "Bearer not-one-of-ours")
	if _, err := app.authenticate(bearer); !errors.Is(err, errNoSession) {
		t.Errorf("a token in the wrong format is a sign-out, got %v", err)
	}
	if _, err := app.authenticate(httptest.NewRequest(http.MethodGet, "/api/v1/reports", nil)); !errors.Is(err, errNoSession) {
		t.Errorf("no cookie at all is a sign-out, got %v", err)
	}

	dsn := os.Getenv("WEEKLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEEKLY_TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()
	db, err := openDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	live := &App{db: db}

	// A token of the right shape that matches nothing, and a session cookie that
	// matches nothing. Both are pgx.ErrNoRows, and both are genuine sign-outs.
	apiKey := httptest.NewRequest(http.MethodGet, "/api/v1/reports", nil)
	apiKey.Header.Set("Authorization", "Bearer wky_nothing_matches_this")
	if _, err := live.authenticate(apiKey); !errors.Is(err, errNoSession) {
		t.Errorf("an unknown API key is a sign-out, got %v", err)
	}
	stale := httptest.NewRequest(http.MethodGet, "/api/v1/reports", nil)
	stale.AddCookie(&http.Cookie{Name: sessionCookie, Value: "expired-or-revoked"})
	if _, err := live.authenticate(stale); !errors.Is(err, errNoSession) {
		t.Errorf("an expired session is a sign-out, got %v", err)
	}

	// And the case this whole distinction exists for: the store is gone. Telling
	// that user they are signed out sends them to type their password again at
	// the one moment it cannot work.
	db.Close()
	outage := httptest.NewRequest(http.MethodGet, "/api/v1/reports", nil)
	outage.AddCookie(&http.Cookie{Name: sessionCookie, Value: "anything"})
	_, err = live.authenticate(outage)
	if err == nil {
		t.Fatal("a closed pool answered as though the session were valid")
	}
	if errors.Is(err, errNoSession) {
		t.Errorf("a database outage would be reported as a sign-out: %v", err)
	}

	// pgx.ErrNoRows is the one database error that really does mean "no such
	// session", and authenticate converts it deliberately rather than by match.
	if errors.Is(pgx.ErrNoRows, errNoSession) {
		t.Error("pgx.ErrNoRows must be converted deliberately, not matched by accident")
	}
}

// A database that cannot be reached must not be reported as a wrong password.
//
// The user lookup's error fell through to the invalid-credentials branch, so an
// outage told everybody their password had stopped working. Worse, each retry
// was recorded against the throttle, so the people who tried hardest were
// locked out of a service that had never rejected them.
// guards: login
func TestLoginSaysTheStoreIsDownRatherThanBlamingThePassword(t *testing.T) {
	// A pool that parses and constructs but can never connect: pgxpool dials
	// lazily, so this stands in for a database that has gone away.
	config, err := pgxpool.ParseConfig("postgres://nobody:nobody@127.0.0.1:1/nothing?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.ConnectTimeout = 500 * time.Millisecond
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	application := &App{db: pool, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	body := strings.NewReader(`{"username":"someone","password":"whatever"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	started := time.Now()
	application.login(recorder, request)
	elapsed := time.Since(started)

	if recorder.Code == http.StatusUnauthorized {
		t.Fatalf("an unreachable database was reported as bad credentials: %s", recorder.Body.String())
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=503, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "STORE_UNAVAILABLE") {
		t.Errorf("the answer does not name the real problem: %s", recorder.Body.String())
	}
	// Bounded, not hanging. Every database call on this path has a deadline, so
	// the person who pressed 로그인 gets an answer instead of a spinner.
	if elapsed > 30*time.Second {
		t.Errorf("login took %v against a dead database", elapsed)
	}
}
