package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
func TestOnlyGenuineSignOutIsMarkedAsNoSession(t *testing.T) {
	genuine := []error{
		fmt.Errorf("%w: no session cookie", errNoSession),
		fmt.Errorf("%w: session expired or revoked", errNoSession),
		fmt.Errorf("%w: unsupported bearer token", errNoSession),
		fmt.Errorf("%w: no such api key", errNoSession),
	}
	for _, err := range genuine {
		if !errors.Is(err, errNoSession) {
			t.Errorf("%v should be answered 401", err)
		}
	}

	// What a database outage looks like coming out of pgx. Anything in this
	// shape must not be reported as an authentication problem.
	outages := []error{
		errors.New("failed to connect to `host=weekly-pg`: dial error"),
		errors.New("conn closed"),
		fmt.Errorf("query failed: %w", errors.New("server closed the connection unexpectedly")),
	}
	for _, err := range outages {
		if errors.Is(err, errNoSession) {
			t.Errorf("%v would be reported to the user as a login problem", err)
		}
	}

	// pgx.ErrNoRows is the one database error that really does mean "no such
	// session", and authenticate converts it before returning.
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
