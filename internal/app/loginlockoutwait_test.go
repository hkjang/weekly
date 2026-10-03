package app

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Two counters refuse a login, and only one of them was ever described.
//
// loginThrottleFor read `min(created_at)` for the account alone, so a caller
// stopped by auth.max_login_attempts_per_ip was told how long *their own*
// failures had left to run — and somebody else's typos had filled the address
// counter, so for them that is no time at all. The refusal then carried
// `Retry-After: 1` and "1분 후에 다시 시도하세요" while the address stayed blocked
// for the whole of auth.lockout_minutes, which sends the reader straight back
// into the same wall, every minute, for a quarter of an hour. The record left
// behind said `failures: 0`, so an operator reading the trail could not see
// which counter had refused it either.
//
// These drive the product's own login handler through Handler(), so every
// request in one test arrives from the same client address — which is the shape
// the per-address counter exists for: one office NAT, one reverse proxy.

// setAddressLoginLimit turns on the per-address counter an operator would enable
// on a network that gives out distinct client addresses.
func (s *testServer) setAddressLoginLimit(attempts, lockoutMinutes int) {
	s.t.Helper()
	w := s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"auth.max_login_attempts_per_ip": strconv.Itoa(attempts),
			"auth.lockout_minutes":           strconv.Itoa(lockoutMinutes),
		},
	}, s.admin)
	if w.Code != http.StatusOK {
		s.t.Fatalf("turn on the per-address login limit: %d %s", w.Code, w.Body.String())
	}
}

// fillAddressCounter spends the address counter on names that are not the
// caller's, so the account counter is nowhere near its own limit.
func (s *testServer) fillAddressCounter(attempts int) {
	s.t.Helper()
	for i := range attempts {
		w := s.request(http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": accountName("someoneelse"), "password": "NotThePassword1"}, nil)
		if w.Code != http.StatusUnauthorized {
			s.t.Fatalf("guess %d answered %d %s", i+1, w.Code, w.Body.String())
		}
	}
}

// guards: loginThrottleFor
func TestAnAddressLockoutSaysHowLongItActuallyLasts(t *testing.T) {
	server := newTestServer(t)
	server.setAddressLoginLimit(3, 15)
	server.fillAddressCounter(3)

	// The right password, from an account with no failures of its own: the
	// address counter is the only thing that can refuse this.
	w := server.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": server.adminName, "password": "HarnessAdmin1234"}, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the address limit answered %d %s", w.Code, w.Body.String())
	}

	seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After was %q: %v", w.Header().Get("Retry-After"), err)
	}
	if seconds < 14*60 {
		t.Errorf("Retry-After says %ds, but the address stays blocked for 15 minutes: %s",
			seconds, refusal(t, w))
	}
	if message := refusal(t, w); !strings.Contains(message, "15분") {
		t.Errorf("the refusal reads %q; it has to name the wait the caller really has", message)
	}
}

// guards: loginThrottleFor
func TestTheBlockedLoginRecordNamesTheCounterThatRefusedIt(t *testing.T) {
	server := newTestServer(t)
	server.setAddressLoginLimit(3, 15)
	server.fillAddressCounter(3)

	if w := server.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": server.adminName, "password": "HarnessAdmin1234"}, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the address limit answered %d %s", w.Code, w.Body.String())
	}

	var account, address *int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT (detail->>'failures')::int, (detail->>'addressFailures')::int
			FROM audit_logs WHERE action='auth.login_blocked' ORDER BY id DESC LIMIT 1`).
		Scan(&account, &address); err != nil {
		t.Fatal(err)
	}
	if account == nil || *account != 0 {
		t.Errorf("the record reports %v failures for the account; this account has none", account)
	}
	if address == nil || *address != 3 {
		t.Errorf("the record reports %v failures for the address, so the trail does not say which counter refused the login", address)
	}
}
