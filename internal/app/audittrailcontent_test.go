package app

import (
	"net/http"
	"testing"
)

// The audit tests check that a failed write is at least reported, and that a
// record survives the client walking away. Neither reads a record. What an
// operator needs after an incident is the two columns nobody was checking: who
// did it, and from where.

// guards: audit
func TestAnAuditRecordNamesWhoActedAndFromWhere(t *testing.T) {
	server := newTestServer(t)

	created := server.request(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username":    accountName("audited"),
		"displayName": "감사에 남는 계정",
		"role":        "USER",
		"password":    "SomePassword1234",
	}, server.admin)
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("create a user: %d %s", created.Code, created.Body.String())
	}

	var actor *int64
	var address *string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT actor_id, ip_address::text FROM audit_logs WHERE action='user.create' ORDER BY id DESC LIMIT 1`).
		Scan(&actor, &address); err != nil {
		t.Fatal(err)
	}
	if actor == nil {
		t.Error("the record does not say who created the account")
	} else if *actor != server.userIDOf(server.adminName) {
		t.Errorf("the record names actor %d, not the administrator who acted", *actor)
	}
	if address == nil || *address == "" {
		t.Error("the record does not say where the request came from")
	}
}

// guards: audit
//
// Not every audited moment has somebody signed in behind it, and the writer has
// to survive that rather than take the request down with it. Reaching this
// through HTTP would need an audited route that answers before authentication;
// calling it directly is the honest way to pin a defence that exists on purpose.
func TestAnAuditRecordWithNobodySignedInIsStillWritten(t *testing.T) {
	server := newTestServer(t)

	request := server.newRequestFor("/api/v1/version")
	server.app.audit(request, nil, "auth.anonymous", "session", "none", map[string]any{"reason": "시험"})

	var count int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM audit_logs WHERE action='auth.anonymous' AND actor_id IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("an anonymous audit record was not written: %d rows", count)
	}
}

func (s *testServer) newRequestFor(path string) *http.Request {
	s.t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://example.test"+path, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	request.RemoteAddr = "10.0.0.7:54321"
	return request
}
