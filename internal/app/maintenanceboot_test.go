package app

import (
	"context"
	"testing"
	"time"
)

// Retention that only holds for uptime longer than the sweep interval is not
// retention.
//
// maintenance ran runMaintenance from a thirty-minute ticker and nowhere else,
// so a deployment that restarts more often than that never swept — and a
// rollout, a crash loop, or an operator restarting to apply a setting are all
// exactly that. Measured on a three-year database before this test existed:
// audit rows 1,100 days old and request metrics 400 days old survived a restart
// under a 365-day and a 90-day policy, because the first tick had not arrived
// and the process did not live to see one.
//
// The test starts maintenance and waits a moment, rather than calling
// runMaintenance directly: what broke was not the sweep but when it was run.
//
// guards: maintenance, runMaintenance
func TestMaintenanceSweepsWithoutWaitingForItsFirstTick(t *testing.T) {
	server := newTestServer(t)
	background := server.ctx()

	plant := func() {
		if _, err := server.app.db.Exec(background, `
			INSERT INTO audit_logs(actor_id, action, resource_type, resource_id, created_at)
			SELECT NULL, 'boot.retention', 'probe', days::text, now() - make_interval(days => days)
			FROM (VALUES (300), (400), (1100)) AS ages(days)`); err != nil {
			t.Fatal(err)
		}
		if _, err := server.app.db.Exec(background, `
			INSERT INTO api_request_metrics(bucket, method, route, status, request_count, duration_ms_sum, duration_ms_max)
			SELECT date_trunc('hour', now() - make_interval(days => days)), 'GET', '/probe/' || days, 200, 1, 1, 1
			FROM (VALUES (80), (120), (400)) AS ages(days)`); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (audits, metrics int) {
		if err := server.app.db.QueryRow(background,
			`SELECT (SELECT count(*) FROM audit_logs WHERE action='boot.retention'),
			        (SELECT count(*) FROM api_request_metrics WHERE route LIKE '/probe/%')`).
			Scan(&audits, &metrics); err != nil {
			t.Fatal(err)
		}
		return
	}

	plant()
	if audits, metrics := count(); audits != 3 || metrics != 3 {
		t.Fatalf("심은 행이 %d, %d 입니다", audits, metrics)
	}

	swept, cancel := context.WithCancel(background)
	defer cancel()
	go server.app.maintenance(swept)

	deadline := time.Now().Add(5 * time.Second)
	var audits, metrics int
	for time.Now().Before(deadline) {
		audits, metrics = count()
		if audits <= 1 && metrics <= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 365일 보관에서 300일짜리는 남고 400·1100일짜리는 지워집니다.
	if audits != 1 {
		t.Errorf("감사 기록이 %d건 남았습니다 — 보관 안쪽 300일짜리 하나만 남아야 합니다."+
			" 기동 때 한 번도 쓸지 않으면 30분보다 자주 재기동하는 배포는 영영 정리되지 않습니다", audits)
	}
	// 90일 보관에서 80일짜리만 남습니다.
	if metrics != 1 {
		t.Errorf("요청 지표가 %d건 남았습니다 — 보관 안쪽 80일짜리 하나만 남아야 합니다", metrics)
	}
}
