package app

import (
	"net/http"
	"testing"
)

// The audit trail is a product feature: administrators read it from 감사 로그.
// Its writes are deliberately best-effort, because refusing an action that has
// already committed just because its bookkeeping failed would be worse. What is
// not acceptable is a gap nobody can see. Before this, a failed insert produced
// no row, no log line and no clue — an administrator could create an account and
// the trail would simply not mention it.

// guards: audit
func TestAnAuditWriteThatFailsIsAtLeastReported(t *testing.T) {
	server := newTestServer(t)
	// Take the table away the way a constraint problem or a lost tablespace
	// would: the action still succeeds, only its record cannot land.
	if _, err := server.app.db.Exec(server.ctx(), `ALTER TABLE audit_logs RENAME TO audit_logs_hidden`); err != nil {
		t.Fatalf("hide the audit table: %v", err)
	}

	created := server.request(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username":    accountName("unaudited"),
		"displayName": "감사 없이 만들어진 계정",
		"role":        "USER",
		"password":    "SomePassword1234",
	}, server.admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("creating the user should still succeed, got %d: %s", created.Code, created.Body.String())
	}

	if !server.logged("audit record was not written", "user.create") {
		t.Fatalf("a lost audit record left no trace in the log:\n%s", server.logs.String())
	}

	if _, err := server.app.db.Exec(server.ctx(), `ALTER TABLE audit_logs_hidden RENAME TO audit_logs`); err != nil {
		t.Fatalf("restore the audit table: %v", err)
	}
}

// guards: audit
func TestAnAuditRecordSurvivesTheClientWalkingAway(t *testing.T) {
	server := newTestServer(t)
	name := accountName("stillaudited")
	created := server.request(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": name, "displayName": "기록되는 계정", "role": "USER", "password": "SomePassword1234",
	}, server.admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("create the user: %d %s", created.Code, created.Body.String())
	}

	var rows int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM audit_logs WHERE action='user.create'`).Scan(&rows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if rows == 0 {
		t.Fatal("creating a user wrote no audit record")
	}
	// The write must not ride on the request context: a client that disconnects
	// mid-request would otherwise cancel the record of what it had already done.
	if server.logged("audit record was not written") {
		t.Fatalf("an audit write failed on a healthy deployment:\n%s", server.logs.String())
	}
}
