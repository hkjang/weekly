package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Where a request came from is read once, by remoteHost, and then handed to
// three `inet` columns: audit_logs.ip_address, user_sessions.ip_address and
// login_attempts.ip_address. A value that is not an address is therefore worse
// than no value at all — every one of those writes fails, and each of them fails
// quietly:
//
//   - the audit record is dropped (logged, not returned),
//   - the login_attempts row the rate limiter counts is dropped, so the limit
//     never arrives and password guessing is unlimited,
//   - the session row is not written, so a correct password answers
//     SESSION_ERROR.
//
// Both halves of remoteHost could produce such a value. These tests drive the
// product's own handler over a real IPv6 listener, so net/http fills RemoteAddr
// the way it does for a customer on a dual-stack network — the binary listens on
// ":8080", which on Linux accepts IPv6 as well.

// ipv6Server serves the product's handler on the IPv6 loopback address.
func ipv6Server(t *testing.T, server *testServer) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("the IPv6 loopback address is not available here: %v", err)
	}
	unstarted := httptest.NewUnstartedServer(server.app.Handler())
	_ = unstarted.Listener.Close()
	unstarted.Listener = listener
	unstarted.Start()
	t.Cleanup(unstarted.Close)
	return unstarted
}

// postLogin posts credentials to a real listener and returns status and body.
func postLogin(t *testing.T, base, username, password string, header http.Header) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, base+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, values := range header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(payload)
}

// attemptRows reports how many recent failures the rate limiter can count for a
// name, and the address recorded on the newest of them.
func (s *testServer) attemptRows(username string) (int, string) {
	s.t.Helper()
	var count int
	var address *string
	if err := s.app.db.QueryRow(s.ctx(), `SELECT count(*),
		max(host(ip_address)) FROM login_attempts WHERE lower(username)=lower($1)`, username).
		Scan(&count, &address); err != nil {
		s.t.Fatal(err)
	}
	if address == nil {
		return count, ""
	}
	return count, *address
}

// guards: remoteHost
func TestAWrongPasswordFromAnIPv6AddressIsStillCountedAgainstTheLimit(t *testing.T) {
	server := newTestServer(t)
	base := ipv6Server(t, server).URL

	name := server.adminName
	status, body := postLogin(t, base, name, "DefinitelyNotThePassword", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("a wrong password answered %d %s", status, body)
	}

	count, address := server.attemptRows(name)
	if count != 1 {
		t.Errorf("the rate limiter counts %d failures from ::1, so the limit never arrives there", count)
	}
	if address != "::1" {
		t.Errorf("the recorded address is %q, not the caller's ::1", address)
	}
}

// guards: remoteHost
func TestTheRightPasswordFromAnIPv6AddressSignsIn(t *testing.T) {
	server := newTestServer(t)
	base := ipv6Server(t, server).URL

	status, body := postLogin(t, base, server.adminName, "HarnessAdmin1234", nil)
	if status != http.StatusOK {
		t.Fatalf("a correct password from ::1 answered %d %s", status, body)
	}

	var address *string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT host(ip_address) FROM user_sessions ORDER BY id DESC LIMIT 1`).Scan(&address); err != nil {
		t.Fatal(err)
	}
	if address == nil || *address != "::1" {
		got := "<null>"
		if address != nil {
			got = *address
		}
		t.Errorf("the session records %s as the address it was issued to, not ::1", got)
	}
}

// guards: remoteHost
//
// The deployment guide tells an operator to have the proxy overwrite
// X-Forwarded-For, and OPERATIONS.md says why. Until it does — or on the day a
// caller reaches :8080 without passing one — a header anybody can type decided
// what went into an `inet` column, and a value that is not an address took the
// login_attempts row down with it. Erasing your own address must not erase the
// count of your own failures.
func TestAForgedForwardedForDoesNotEraseTheFailureCount(t *testing.T) {
	server := newTestServer(t)
	base := ipv6Server(t, server).URL

	header := http.Header{}
	header.Set("X-Forwarded-For", "그런 주소 없음")
	status, body := postLogin(t, base, server.adminName, "DefinitelyNotThePassword", header)
	if status != http.StatusUnauthorized {
		t.Fatalf("a wrong password answered %d %s", status, body)
	}

	count, address := server.attemptRows(server.adminName)
	if count != 1 {
		t.Errorf("a forged X-Forwarded-For left %d failures on record, so the limit never arrives", count)
	}
	if address != "::1" {
		t.Errorf("the recorded address is %q; the header was not an address, so the caller's ::1 should stand", address)
	}
}
