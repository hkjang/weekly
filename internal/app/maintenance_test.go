package app

import (
	"fmt"
	"net/http"
	"testing"
)

// Everything this deployment throws away on a schedule, and nothing had ever
// run it. Four operator-settable retention policies live in one 30-minute
// ticker, so an administrator who sets 보관일 to 30 has no way to see it take
// effect and no test could reach the code at all. A DELETE that takes too much
// is the kind of mistake nobody reports, because what it removed is exactly
// what nobody is looking at any more.
//
// guards: runMaintenance
func TestTheMaintenanceSweepThrowsAwayOnlyWhatThePolicySays(t *testing.T) {
	server := newTestServer(t)
	server.createUser("maintenance_author", "USER", nil)

	set := func(key, value string) {
		t.Helper()
		saved := server.request(http.MethodPut, "/api/v1/admin/settings",
			map[string]any{"settings": map[string]string{key: value}}, server.admin)
		if saved.Code != http.StatusOK {
			t.Fatalf("set %s: %d %s", key, saved.Code, saved.Body.String())
		}
	}
	count := func(statement string, args ...any) int {
		t.Helper()
		var total int
		if err := server.app.db.QueryRow(server.ctx(), statement, args...).Scan(&total); err != nil {
			t.Fatalf("count: %v", err)
		}
		return total
	}

	set("analytics.retention_days", "30")
	set("audit.retention_days", "30")

	// Two rows on each side of every boundary: one comfortably past it and one
	// comfortably inside. A sweep that takes the second is a sweep that has
	// eaten data somebody still needs.
	tag := fmt.Sprint(harnessRun)
	if _, err := server.app.db.Exec(server.ctx(), `
		INSERT INTO api_request_metrics(bucket, method, route, status, request_count, duration_ms_sum, duration_ms_max)
		VALUES (date_trunc('hour', now() - interval '60 days'), 'GET', $1, 200, 1, 1, 1),
		       (date_trunc('hour', now() - interval '5 days'),  'GET', $2, 200, 1, 1, 1)`,
		"/maintenance-old-"+tag, "/maintenance-new-"+tag); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}
	if _, err := server.app.db.Exec(server.ctx(), `
		INSERT INTO audit_logs(actor_id, action, resource_type, resource_id, detail, created_at)
		VALUES ($1, $2, 'test', '1', '{}', now() - interval '60 days'),
		       ($1, $3, 'test', '2', '{}', now() - interval '5 days')`,
		nil, "maintenance.old."+tag, "maintenance.new."+tag); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if _, err := server.app.db.Exec(server.ctx(), `
		INSERT INTO login_attempts(username, ip_address, created_at)
		VALUES ($1, NULL, now() - interval '3 days'), ($2, NULL, now())`,
		"old-"+tag, "new-"+tag); err != nil {
		t.Fatalf("seed login attempts: %v", err)
	}
	// A session that has expired, and one that has not.
	if _, err := server.app.db.Exec(server.ctx(), `
		INSERT INTO user_sessions(user_id, token_hash, expires_at)
		VALUES ((SELECT id FROM users WHERE username=$1), $2, now() - interval '1 hour')`,
		server.lastCreatedUsername("maintenance_author"), "expired-"+tag); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	liveSessions := count(`SELECT count(*) FROM user_sessions WHERE expires_at > now()`)

	server.app.runMaintenance(server.ctx())

	for _, check := range []struct {
		what      string
		statement string
		arg       any
		want      int
	}{
		{"60일 지난 지표", `SELECT count(*) FROM api_request_metrics WHERE route=$1`, "/maintenance-old-" + tag, 0},
		{"5일 된 지표", `SELECT count(*) FROM api_request_metrics WHERE route=$1`, "/maintenance-new-" + tag, 1},
		{"60일 지난 감사 기록", `SELECT count(*) FROM audit_logs WHERE action=$1`, "maintenance.old." + tag, 0},
		{"5일 된 감사 기록", `SELECT count(*) FROM audit_logs WHERE action=$1`, "maintenance.new." + tag, 1},
		{"3일 지난 로그인 시도", `SELECT count(*) FROM login_attempts WHERE username=$1`, "old-" + tag, 0},
		{"방금 로그인 시도", `SELECT count(*) FROM login_attempts WHERE username=$1`, "new-" + tag, 1},
		{"만료된 세션", `SELECT count(*) FROM user_sessions WHERE token_hash=$1`, "expired-" + tag, 0},
	} {
		if got := count(check.statement, check.arg); got != check.want {
			t.Errorf("%s: 스윕 뒤 %d건, 기대 %d건", check.what, got, check.want)
		}
	}
	if after := count(`SELECT count(*) FROM user_sessions WHERE expires_at > now()`); after != liveSessions {
		t.Errorf("살아 있는 세션이 %d에서 %d로 바뀌었습니다 — 스윕이 로그인한 사람을 쫓아냈습니다", liveSessions, after)
	}

	// Zero means keep everything, and the sweep has to honour that rather than
	// read it as "older than zero days".
	if _, err := server.app.db.Exec(server.ctx(), `
		INSERT INTO audit_logs(actor_id, action, resource_type, resource_id, detail, created_at)
		VALUES (NULL, $1, 'test', '3', '{}', now() - interval '3650 days')`,
		"maintenance.ancient."+tag); err != nil {
		t.Fatalf("seed an ancient record: %v", err)
	}
	set("audit.retention_days", "0")
	server.app.runMaintenance(server.ctx())
	if got := count(`SELECT count(*) FROM audit_logs WHERE action=$1`, "maintenance.ancient."+tag); got != 1 {
		t.Errorf("보관일 0은 전부 보관하라는 뜻인데 10년 된 기록이 지워졌습니다: %d건 남음", got)
	}
}

// The import source cleanup removes the uploaded PPTX from the state volume
// once the job is finished and the retention has passed. It selected with
// ($1 || ' days'), which PostgreSQL reads as a text parameter and pgx refuses
// to encode an int into, and the failure went into a bare `return` — so Import
// 원본 보관일 has never removed a file on any deployment.
//
// guards: cleanupImportSources
func TestTheImportSourceCleanupActuallySelects(t *testing.T) {
	server := newTestServer(t)
	server.enableAIWithoutAGateway(t)
	author := server.createUser("import_retention", "USER", nil)

	created := server.upload("/api/v1/import/pptx", "files", "retention.pptx", []byte("not a pptx"), author)
	if created.Code != http.StatusAccepted {
		t.Fatalf("upload: %d %s", created.Code, created.Body.String())
	}
	var jobID, fileID int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT f.import_job_id, f.id FROM import_files f ORDER BY f.id DESC LIMIT 1`).Scan(&jobID, &fileID); err != nil {
		t.Fatalf("read the uploaded file: %v", err)
	}
	// Finished long ago, with a stored path — exactly what the policy is for.
	// The path the upload actually wrote stays as it is — safeImportPath only
	// removes files under the import directory, and rewriting it here would
	// test the guard rather than the policy.
	if _, err := server.app.db.Exec(server.ctx(), `
		UPDATE import_files SET status='CONFIRMED', confirmed_at = now() - interval '400 days'
		WHERE id=$1`, fileID); err != nil {
		t.Fatalf("age the file: %v", err)
	}
	var storedPath string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT coalesce(stored_path,'') FROM import_files WHERE id=$1`, fileID).Scan(&storedPath); err != nil {
		t.Fatalf("read the stored path: %v", err)
	}
	if storedPath == "" {
		t.Skip("이 업로드는 원본을 저장하지 않았습니다")
	}
	// And one finished yesterday, which the same sweep must leave alone.
	var recentID int64
	if err := server.app.db.QueryRow(server.ctx(), `
		INSERT INTO import_files(import_job_id, original_filename, stored_path, file_hash, size_bytes, status, confirmed_at)
		VALUES ($1, 'recent.pptx', $2, $3, 10, 'CONFIRMED', now() - interval '1 day') RETURNING id`,
		jobID, storedPath, fmt.Sprintf("hash-recent-%d", harnessRun)).Scan(&recentID); err != nil {
		t.Fatalf("insert a recent file: %v", err)
	}

	server.app.runMaintenance(server.ctx())

	var oldPath, recentPath *string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT stored_path FROM import_files WHERE id=$1`, fileID).Scan(&oldPath); err != nil {
		t.Fatalf("re-read the aged file: %v", err)
	}
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT stored_path FROM import_files WHERE id=$1`, recentID).Scan(&recentPath); err != nil {
		t.Fatalf("re-read the recent file: %v", err)
	}
	if oldPath != nil {
		t.Errorf("400일 지난 Import 원본이 그대로입니다: %q", *oldPath)
	}
	if recentPath == nil {
		t.Error("어제 끝난 Import 원본까지 지웠습니다")
	}
}
