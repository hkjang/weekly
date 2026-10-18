package app

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A queued mail has to read the report before it can send it, and that read can
// fail for two reasons that are not alike. The report may be gone — nothing is
// coming back, so the delivery is over. Or the store was unreachable for a
// moment: a connection dropped, a statement timed out, a failover. The send
// path already knows the difference between those and an address the relay
// refuses outright, and waits the second kind out over about three hours.
//
// The read did not. Any error at all marked the row FAILED on the first claim,
// so a blip during the worker's thirty-second tick spent a whole week's report:
// the writer's own card showed one FAILED line, there is no way to put it back
// in the queue, and that week's mail never went.

// queuedMail is the newest delivery row as the worker left it.
type queuedMail struct {
	id       int64
	status   string
	reason   string
	attempts int
	next     time.Time
}

// awaitMailAttempt waits until the newest delivery has been claimed and carries
// a verdict, and returns it.
//
// awaitDelivery("QUEUED") cannot be used here: QUEUED is also the state the row
// is in before anything has touched it, so a test waiting on it would pass
// against a worker that never ran. Waiting for the reason as well as the
// counter keeps this out of the gap between the claim, which increments
// attempts, and the write that records what happened.
func (s *testServer) awaitMailAttempt(count int) queuedMail {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var row queuedMail
	for {
		err := s.app.db.QueryRow(s.ctx(),
			`SELECT id, status, coalesce(error_message,''), attempts, next_attempt_at
			 FROM report_mail_deliveries ORDER BY id DESC LIMIT 1`).
			Scan(&row.id, &row.status, &row.reason, &row.attempts, &row.next)
		if err == nil && row.attempts >= count && row.reason != "" {
			return row
		}
		if time.Now().After(deadline) {
			if err != nil {
				s.t.Fatalf("no delivery was recorded within the deadline: %v", err)
			}
			return row
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// breakReportLoading makes loading a report fail the way an unreachable store
// makes it fail: the report row itself is still readable and the query after it
// cannot run, so the error that comes back is a transport error and not "no
// such report".
//
// report_comments is read by loadReport and written by nothing the mail worker
// touches, so taking it away reaches the second query without disturbing the
// submission, the queue or the writer's settings card. The returned function
// puts it back; it is also registered as cleanup so a failing assertion cannot
// leave the schema short of a table.
func (s *testServer) breakReportLoading() func() {
	s.t.Helper()
	if _, err := s.app.db.Exec(s.ctx(), `ALTER TABLE report_comments RENAME TO report_comments_blocked`); err != nil {
		s.t.Fatalf("take report_comments away: %v", err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if _, err := s.app.db.Exec(s.ctx(), `ALTER TABLE report_comments_blocked RENAME TO report_comments`); err != nil {
			s.t.Fatalf("put report_comments back: %v", err)
		}
	}
	s.t.Cleanup(restore)
	return restore
}

// queueOneMail leaves exactly one QUEUED delivery for a new writer without the
// worker having had a usable relay to carry it with.
//
// The order matters. Sending is still off while the report is handed in, so
// unusable() stops the worker the submission wakes before it can claim the row,
// and the test gets to decide what the claim will find.
func (s *testServer) queueOneMail(stem, address, week string) *http.Cookie {
	s.t.Helper()
	writer := s.createUser(stem, "USER", nil)
	if w := s.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": address, "onSubmit": true}, writer); w.Code != http.StatusOK {
		s.t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	s.submitted(writer, week, "읽히지 않는 주의 요약")
	return writer
}

// guards: sendNextQueuedMail, deferMailRetry
func TestAReportThatCouldNotBeReadForAMomentKeepsItsRetryBudget(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	writer := server.queueOneMail("mail_reload", "reload@internal.test", "2026-08-24")

	// The store goes away between the submission and the claim, which is where
	// a failover or a dropped connection lands in practice.
	restore := server.breakReportLoading()
	server.configureRelay(relay)
	server.app.sendNextQueuedMail(server.ctx())

	row := server.awaitMailAttempt(1)
	if row.status != "QUEUED" {
		t.Fatalf("status=%s attempts=%d reason=%q — one unreadable moment spent the whole week's report, and there is no way to queue it again",
			row.status, row.attempts, row.reason)
	}
	if row.attempts != 1 {
		t.Errorf("attempts=%d after one claim, want 1", row.attempts)
	}
	if !strings.Contains(row.reason, "보고서를 읽을 수 없습니다") {
		t.Errorf("the row does not say why it has not arrived yet: %q", row.reason)
	}
	if !row.next.After(time.Now()) {
		t.Errorf("the row is due again immediately, so the queue will spin on it: %v", row.next)
	}
	// Nothing reached the relay: the mail was never built, let alone sent.
	if sent := relay.received(); len(sent) != 0 {
		t.Errorf("the relay received %d messages for a report that could not be read", len(sent))
	}
	// And the writer's own card says waiting rather than failed.
	card := server.request(http.MethodGet, "/api/v1/me/mail", nil, writer)
	if body := card.Body.String(); strings.Contains(body, "FAILED") || !strings.Contains(body, "QUEUED") {
		t.Errorf("the card tells the writer the mail is not coming: %s", body)
	}

	// The outage ends. The row is still there to be carried, and the next time
	// it is due it goes out for real.
	restore()
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE report_mail_deliveries SET next_attempt_at=now() WHERE id=$1`, row.id); err != nil {
		t.Fatalf("make the delivery due again: %v", err)
	}
	server.app.sendNextQueuedMail(server.ctx())

	messages := relay.awaitRelay(t, 1)
	if len(messages) != 1 {
		t.Fatalf("the relay received %d messages after the store came back, want 1", len(messages))
	}
	if envelope := strings.Join(relay.envelopeLines(), " "); !strings.Contains(envelope, "reload@internal.test") {
		t.Errorf("the mail did not go to the address the writer chose: %s", envelope)
	}
	if status, _, _ := server.awaitDelivery("SENT"); status != "SENT" {
		t.Errorf("the delivery is %s after the relay took it, want SENT", status)
	}
}

// The other half of the budget: a store that never comes back must not leave
// the row queued forever. The read failure is spent the same way a refused
// address is, and when it runs out the delivery is given up on with the reason
// on it.

// guards: sendNextQueuedMail, deferMailRetry, markMailFailed
func TestAReportThatStaysUnreadableRunsOutOfTriesAndStops(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	writer := server.queueOneMail("mail_reload_out", "gone@internal.test", "2026-08-17")

	server.breakReportLoading()
	server.configureRelay(relay)
	// One try, so the budget is spent on the first claim rather than three
	// hours from now.
	if w := server.request(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"settings": map[string]string{"mail.max_attempts": "1"}}, server.admin); w.Code != http.StatusOK {
		t.Fatalf("set the attempt budget: %d %s", w.Code, w.Body.String())
	}
	server.app.sendNextQueuedMail(server.ctx())

	row := server.awaitMailAttempt(1)
	if row.status != "FAILED" {
		t.Fatalf("status=%s attempts=%d — the budget ran out and the row is still waiting, so the worker will keep claiming it",
			row.status, row.attempts)
	}
	if row.attempts != 1 {
		t.Errorf("attempts=%d, want 1 — the budget was not respected", row.attempts)
	}
	if !strings.Contains(row.reason, "보고서를 읽을 수 없습니다") {
		t.Errorf("the verdict does not carry the reason: %q", row.reason)
	}
	// It is not picked up again.
	server.app.sendNextQueuedMail(server.ctx())
	after := server.awaitMailAttempt(1)
	if after.attempts != 1 {
		t.Errorf("attempts=%d after a second sweep — a delivery that was given up on is still being tried", after.attempts)
	}
	card := server.request(http.MethodGet, "/api/v1/me/mail", nil, writer)
	if body := card.Body.String(); !strings.Contains(body, "FAILED") || !strings.Contains(body, "보고서를 읽을 수 없습니다") {
		t.Errorf("the card does not say it was given up on: %s", body)
	}
}

// And the case that must stay immediate. A report that is no longer there will
// not be there in an hour either, so spending three hours of retries on it
// would only delay a verdict the writer could already act on.

// guards: sendNextQueuedMail, markMailFailed
func TestAMailForAReportThatIsGoneIsGivenUpOnAtOnce(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.queueOneMail("mail_reload_none", "vanished@internal.test", "2026-08-10")

	// The delivery outliving its report cannot happen through the API — the row
	// is deleted with the report it belongs to — so the reference is loosened
	// to reach the branch that handles it anyway.
	var reportID int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT report_id FROM report_mail_deliveries ORDER BY id DESC LIMIT 1`).Scan(&reportID); err != nil {
		t.Fatalf("read the queued delivery: %v", err)
	}
	if _, err := server.app.db.Exec(server.ctx(),
		`ALTER TABLE report_mail_deliveries DROP CONSTRAINT report_mail_deliveries_report_id_fkey`); err != nil {
		t.Fatalf("loosen the report reference: %v", err)
	}
	if _, err := server.app.db.Exec(server.ctx(), `DELETE FROM weekly_reports WHERE id=$1`, reportID); err != nil {
		t.Fatalf("take the report away: %v", err)
	}

	// The budget is the default five, so a row that is deferred here would be
	// left QUEUED and the writer would be told to keep waiting.
	server.configureRelay(relay)
	server.app.sendNextQueuedMail(server.ctx())

	row := server.awaitMailAttempt(1)
	if row.status != "FAILED" {
		t.Fatalf("status=%s attempts=%d reason=%q — a mail for a report that no longer exists is waiting for it to come back",
			row.status, row.attempts, row.reason)
	}
	if !strings.Contains(row.reason, "보고서를 읽을 수 없습니다") {
		t.Errorf("the verdict does not carry the reason: %q", row.reason)
	}
	if sent := relay.received(); len(sent) != 0 {
		t.Errorf("the relay received %d messages for a report that does not exist", len(sent))
	}
	// The card is not read here: it names each delivery by the week of the
	// report it belongs to, so a delivery whose report is gone is not on it at
	// all. That is the state this test had to build by hand to reach the branch.
}
