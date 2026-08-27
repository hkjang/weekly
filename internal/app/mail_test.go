package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// A relay is the one part of this feature the product does not own, so these
// tests bring one: a listener that speaks enough SMTP to accept a message and
// hand back exactly what arrived on the wire. Asserting on a mock's method
// calls would prove the code called itself correctly; this proves what a mail
// server receives.

type fakeRelay struct {
	listener net.Listener
	mu       sync.Mutex
	messages []string
	envelope []string
	refuse   string // when set, the relay rejects every RCPT with this text
	// refuseFor rejects one address and accepts the rest, which is the only way
	// to test that a refused delivery does not hold up the ones behind it: the
	// refusal has to still be there when the next one is tried.
	refuseFor map[string]string
}

func startFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	relay := &fakeRelay{listener: listener}
	go relay.serve()
	t.Cleanup(func() { listener.Close() })
	return relay
}

func (relay *fakeRelay) hostPort() (string, int) {
	address := relay.listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", address.Port
}

func (relay *fakeRelay) serve() {
	for {
		conn, err := relay.listener.Accept()
		if err != nil {
			return
		}
		go relay.handle(conn)
	}
}

func (relay *fakeRelay) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	say := func(line string) { fmt.Fprintf(conn, "%s\r\n", line) }
	say("220 relay.test ESMTP")
	var body strings.Builder
	inData := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if strings.TrimRight(line, "\r\n") == "." {
				inData = false
				relay.mu.Lock()
				relay.messages = append(relay.messages, body.String())
				relay.mu.Unlock()
				body.Reset()
				say("250 2.0.0 Ok")
				continue
			}
			body.WriteString(line)
			continue
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			say("250-relay.test")
			say("250 SIZE 10485760")
		case strings.HasPrefix(command, "RCPT TO"):
			relay.mu.Lock()
			reason := relay.refuse
			for address, text := range relay.refuseFor {
				if strings.Contains(line, address) {
					reason = text
				}
			}
			relay.mu.Unlock()
			if reason != "" {
				say("550 5.1.1 " + reason)
				continue
			}
			relay.mu.Lock()
			relay.envelope = append(relay.envelope, strings.TrimSpace(line))
			relay.mu.Unlock()
			say("250 2.1.5 Ok")
		case strings.HasPrefix(command, "MAIL FROM"):
			relay.mu.Lock()
			relay.envelope = append(relay.envelope, strings.TrimSpace(line))
			relay.mu.Unlock()
			say("250 2.1.0 Ok")
		case command == "DATA":
			inData = true
			say("354 go ahead")
		case command == "QUIT":
			say("221 2.0.0 Bye")
			return
		default:
			say("250 2.0.0 Ok")
		}
	}
}

func (relay *fakeRelay) received() []string {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return append([]string{}, relay.messages...)
}

func (relay *fakeRelay) envelopeLines() []string {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return append([]string{}, relay.envelope...)
}

// decodedBody returns the message body as the reader would eventually see it.
func decodedBody(t *testing.T, message string) string {
	t.Helper()
	_, encoded, found := strings.Cut(message, "\r\n\r\n")
	if !found {
		t.Fatalf("message has no body:\n%s", message)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(encoded, "\r\n", ""), "\n", ""))
	if err != nil {
		t.Fatalf("body is not the base64 the header claims: %v", err)
	}
	return string(decoded)
}

func relaySettings(relay *fakeRelay) mailSettings {
	host, port := relay.hostPort()
	return mailSettings{Enabled: true, Host: host, Port: port, Security: "NONE",
		From: "weekly@internal.test", FromName: "주간보고",
		Timeout: 5 * time.Second, MaxAttempts: 3}
}

// guards: sendMail, buildMailMessage
func TestAReportMailArrivesAsTheRelayCanReadIt(t *testing.T) {
	relay := startFakeRelay(t)
	app := &App{}
	report := &reportView{
		WeekStart: "2026-08-24", DisplayName: "사용자 3", Username: "u3", Status: "CLOSED",
		Summary: "인증 연동을 마쳤습니다.",
		Items: []reportItem{{Category: "개발", Title: "인증 연동", Progress: 80,
			CurrentResult: "토큰 검증까지 끝냈습니다.", NextPlan: "배포 절차서를 씁니다.",
			Issue: "인증서 갱신 일정이 미정입니다.", ManagementAsk: "보안팀 일정을 확정해 주십시오."}},
	}
	subject := reportMailSubject("Weekly", report)
	body := reportMailBody(report, reportStatusLabel(report.Status))

	if err := app.sendMail(context.Background(), relaySettings(relay), "reader@internal.test", subject, body); err != nil {
		t.Fatalf("send: %v", err)
	}
	messages := relay.received()
	if len(messages) != 1 {
		t.Fatalf("the relay received %d messages, want 1", len(messages))
	}

	// Headers are ASCII on the wire: a relay that is not 8-bit clean would
	// otherwise deliver Korean as mojibake, which is indistinguishable from a
	// broken report.
	head, _, _ := strings.Cut(messages[0], "\r\n\r\n")
	for _, line := range strings.Split(head, "\r\n") {
		for _, r := range line {
			if r > 127 {
				t.Fatalf("a header carries a non-ASCII rune %q: %s", r, line)
			}
		}
	}
	if !strings.Contains(head, "Content-Transfer-Encoding: base64") {
		t.Errorf("the message does not say how its body is encoded:\n%s", head)
	}
	// Auto-Submitted keeps a mailbox from answering this with an out-of-office
	// and starting a loop with a server that sends every week.
	if !strings.Contains(head, "Auto-Submitted: auto-generated") {
		t.Errorf("an automatic mail did not say it was automatic:\n%s", head)
	}

	// And the reader sees the report, not an encoding of it.
	text := decodedBody(t, messages[0])
	for _, want := range []string{"2026-08-24 주간보고", "사용자 3", "확정", "인증 연동을 마쳤습니다.",
		"[개발] 인증 연동 (80%)", "토큰 검증까지 끝냈습니다.", "배포 절차서를 씁니다.",
		"인증서 갱신 일정이 미정입니다.", "보안팀 일정을 확정해 주십시오."} {
		if !strings.Contains(text, want) {
			t.Errorf("the mail does not carry %q:\n%s", want, text)
		}
	}

	// The envelope decides where it actually goes; the To header only decides
	// what is printed. They have to name the same person.
	envelope := strings.Join(relay.envelopeLines(), " ")
	if !strings.Contains(envelope, "reader@internal.test") {
		t.Errorf("the envelope does not name the recipient: %s", envelope)
	}
}

// A blank field is not a field. Printing "이슈: -" for every task that has none
// makes the ones that do have an issue harder to find, which is the opposite of
// why the mail exists.

// guards: reportMailBody, writeMailField
func TestTheMailLeavesOutWhatWasNotWritten(t *testing.T) {
	report := &reportView{WeekStart: "2026-08-24", DisplayName: "사용자 3", Username: "u3", Status: "CLOSED",
		Items: []reportItem{{Category: "인프라", Title: "배포 파이프라인", Progress: 55,
			CurrentResult: "스테이징까지 자동화했습니다.", NextPlan: "권한을 신청합니다."}}}
	text := reportMailBody(report, "확정")
	if strings.Contains(text, "이슈") || strings.Contains(text, "요청 사항") {
		t.Errorf("the mail printed headings for fields nobody wrote:\n%s", text)
	}
	if !strings.Contains(text, "스테이징까지 자동화했습니다.") {
		t.Errorf("the mail dropped what was written:\n%s", text)
	}
	// A summary nobody wrote is the same case one level up.
	if strings.Contains(text, "[주간 요약]") {
		t.Errorf("the mail printed an empty summary heading:\n%s", text)
	}
}

// The subject carries a report title and a person's name, and the sender name
// carries what an administrator typed into a settings box. A newline in any of
// them would end the header and let that text add its own — a Bcc, a second
// recipient — to a message this server sends on somebody's behalf.

// guards: headerSafe, buildMailMessage
func TestNothingTypedIntoAReportCanAddItsOwnHeaders(t *testing.T) {
	// The recipient is the one field that is not wrapped in an encoded-word, so
	// it is the one the guard actually protects. An earlier version of this test
	// injected only through the subject and the sender name, both of which are
	// base64 whatever they contain — it passed with the guard deleted, which is
	// the same as not having tested it.
	message := string(buildMailMessage("weekly@internal.test", "주간보고\r\nBcc: quiet@elsewhere.test",
		"reader@internal.test\r\nBcc: quiet@elsewhere.test", "주간보고\r\nBcc: quiet@elsewhere.test", "본문"))
	head, _, _ := strings.Cut(message, "\r\n\r\n")
	// Not "does the text 'Bcc:' appear" — neutralised, it appears inside the To
	// line as ordinary characters, which is exactly what should happen. What
	// matters is which fields the message has, so compare the field names.
	written := map[string]bool{}
	for _, line := range strings.Split(head, "\r\n") {
		name, _, found := strings.Cut(line, ":")
		if !found {
			t.Errorf("a header line has no field name, so a value broke out of one: %q", line)
			continue
		}
		written[strings.ToLower(strings.TrimSpace(name))] = true
	}
	for _, want := range []string{"from", "to", "subject", "mime-version", "content-type", "content-transfer-encoding", "auto-submitted"} {
		if !written[want] {
			t.Errorf("the message lost its %s header:\n%s", want, head)
		}
		delete(written, want)
	}
	for extra := range written {
		t.Errorf("injected text became a %q header:\n%s", extra, head)
	}
}

// An address is where the mail goes, and every other shape net/mail accepts —
// a display name, a list — would put somebody else's text into a header.

// guards: validMailAddress
func TestOnlyAPlainAddressIsAccepted(t *testing.T) {
	for _, good := range []string{"a@b.test", "first.last@sub.example.co.kr", "u3+weekly@internal.test"} {
		if !validMailAddress(good) {
			t.Errorf("%q was refused", good)
		}
	}
	for _, bad := range []string{
		"", "no-at-sign", "a@b.test, other@b.test", "이름 <a@b.test>",
		"a@b.test\nBcc: x@y.test", "a@b.test\r\nBcc: x@y.test", "a@b@c.test", "a @b.test",
	} {
		if validMailAddress(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// Go's SMTP client will not hand a password to an unencrypted connection, so a
// username with security NONE cannot work however the relay is configured.
// Saying which two settings disagree beats a refusal from somebody else's
// server at send time.

// guards: unusable
func TestAPasswordIsNotOfferedToAPlaintextRelay(t *testing.T) {
	base := mailSettings{Enabled: true, Host: "relay.internal", Port: 25, From: "weekly@internal.test"}
	if reason := base.unusable(); reason != "" {
		t.Fatalf("a plain relay with no account was refused: %s", reason)
	}
	withAccount := base
	withAccount.Username = "weekly"
	if reason := withAccount.unusable(); reason == "" {
		t.Error("a password over a plaintext connection was allowed")
	}
	for _, security := range []string{"STARTTLS", "TLS"} {
		encrypted := withAccount
		encrypted.Security = security
		if reason := encrypted.unusable(); reason != "" {
			t.Errorf("%s with an account was refused: %s", security, reason)
		}
	}
	// And each missing piece is named rather than reported as one failure.
	for name, settings := range map[string]mailSettings{
		"꺼짐":        {Host: "relay.internal", From: "weekly@internal.test"},
		"호스트 없음":    {Enabled: true, From: "weekly@internal.test"},
		"보내는 주소 없음": {Enabled: true, Host: "relay.internal"},
	} {
		if settings.unusable() == "" {
			t.Errorf("%s 인데도 사용 가능하다고 답했습니다", name)
		}
	}
}

// configureRelay points this server's mail settings at a listener under the
// test's control, through the same admin endpoint an operator would use.
func (s *testServer) configureRelay(relay *fakeRelay) {
	s.t.Helper()
	host, port := relay.hostPort()
	w := s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{
		"mail.enabled": "true", "mail.host": host, "mail.port": fmt.Sprint(port),
		"mail.security": "NONE", "mail.from": "weekly@internal.test", "mail.from_name": "주간보고",
	}}, s.admin)
	if w.Code != http.StatusOK {
		s.t.Fatalf("configure the relay: %d %s", w.Code, w.Body.String())
	}
}

// awaitDelivery waits for the newest delivery to reach a state and returns it.
//
// The worker is already running in a test server, and submitting wakes it. An
// earlier version of these tests called sendNextQueuedMail themselves and read
// its answer, which raced the worker for the same row: the mail went out, the
// explicit call found an empty queue, and the failure read "nothing was
// queued" — the feature working, reported as the feature broken. Waiting for
// the state is the only assertion that means the same thing either way.
func (s *testServer) awaitDelivery(want string) (status, reason string, attempts int) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := s.app.db.QueryRow(s.ctx(),
			`SELECT status, coalesce(error_message,''), attempts FROM report_mail_deliveries ORDER BY id DESC LIMIT 1`).
			Scan(&status, &reason, &attempts)
		if err == nil && (want == "" || status == want) {
			return status, reason, attempts
		}
		if time.Now().After(deadline) {
			if err != nil {
				s.t.Fatalf("no delivery was recorded within the deadline: %v", err)
			}
			return status, reason, attempts
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitDeliveryReason waits for the newest delivery to carry a failure reason.
//
// attempts is incremented when the row is claimed and the reason is written
// after the relay has refused, so a test that waits on the counter reads the
// row in the gap between the two and sees an empty reason.
func (s *testServer) awaitDeliveryReason() (status, reason string, attempts int) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := s.app.db.QueryRow(s.ctx(),
			`SELECT status, coalesce(error_message,''), attempts FROM report_mail_deliveries ORDER BY id DESC LIMIT 1`).
			Scan(&status, &reason, &attempts)
		if err == nil && reason != "" {
			return status, reason, attempts
		}
		if time.Now().After(deadline) {
			return status, reason, attempts
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitRelay waits until the relay has received at least count messages.
func (relay *fakeRelay) awaitRelay(t *testing.T, count int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		messages := relay.received()
		if len(messages) >= count || time.Now().After(deadline) {
			return messages
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The whole point of the feature, end to end: a report that is handed in
// reaches the address its writer chose. Everything else here guards a piece of
// that; this one guards that the pieces are connected.

// guards: queueReportMail, sendNextQueuedMail
func TestSubmittingAReportSendsItToTheAddressTheWriterChose(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	writer := server.createUser("mail_writer", "USER", nil)

	saved := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "writer@internal.test", "onSubmit": true}, writer)
	if saved.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", saved.Code, saved.Body.String())
	}

	server.submitted(writer, "2026-08-24", "이번 주 요약")

	// Queued by the submission and carried by the worker the submission woke.
	// Nothing here asks it to run: whether that wiring is connected is the
	// thing being tested.
	messages := relay.awaitRelay(t, 1)
	if len(messages) != 1 {
		t.Fatalf("the relay received %d messages, want 1", len(messages))
	}
	if body := decodedBody(t, messages[0]); !strings.Contains(body, "2026-08-24 주간보고") {
		t.Errorf("the mail is not this week's report:\n%s", body)
	}
	if envelope := strings.Join(relay.envelopeLines(), " "); !strings.Contains(envelope, "writer@internal.test") {
		t.Errorf("the mail did not go to the address the writer chose: %s", envelope)
	}

	if status, _, _ := server.awaitDelivery("SENT"); status != "SENT" {
		t.Errorf("the delivery is %s, want SENT — the writer would be told it had not arrived", status)
	}
}

// Turning it off has to mean nothing is sent, not that it is sent and hidden.
// Only the refusal side of this was ever obvious; the queue is what would carry
// a report to somebody who asked not to receive it.

// guards: queueReportMail
func TestNothingIsSentForAWriterWhoDidNotAskForIt(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)

	silent := server.createUser("mail_silent", "USER", nil)
	server.submitted(silent, "2026-08-24", "설정하지 않은 사람")

	offAgain := server.createUser("mail_off", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "off@internal.test", "onSubmit": false}, offAgain); w.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	server.submitted(offAgain, "2026-08-24", "꺼 둔 사람")

	// Long enough for the worker the submissions woke to have done something if
	// it were going to. There is nothing to wait for here, which is the point.
	time.Sleep(500 * time.Millisecond)
	var queued int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM report_mail_deliveries`).Scan(&queued); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if queued != 0 {
		t.Errorf("%d deliveries were queued for writers who did not turn sending on", queued)
	}
	if messages := relay.received(); len(messages) != 0 {
		t.Errorf("the relay received %d messages, want 0", len(messages))
	}
}

// A relay that refuses is the normal failure in these networks, and the writer
// has to be able to see which of the two it was: not configured, or refused.

// guards: sendNextQueuedMail
func TestARefusedDeliveryKeepsTheRelaysOwnReason(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	relay.refuse = "recipient rejected by policy"
	server.configureRelay(relay)
	writer := server.createUser("mail_refused", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "writer@internal.test", "onSubmit": true}, writer); w.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	server.submitted(writer, "2026-08-24", "거부되는 주")

	// The worker tries once, is refused, and writes down why. The count rises
	// before the send is attempted, so waiting on it would read the row in the
	// moment between the two — wait for the reason, which is written last.
	status, reason, attempts := server.awaitDeliveryReason()
	if attempts < 1 {
		t.Errorf("attempts=%d, want at least 1 — a try that is not counted retries forever", attempts)
	}
	if status != "QUEUED" {
		t.Errorf("status=%s, want QUEUED — one refusal is not a permanent failure", status)
	}
	if !strings.Contains(reason, "recipient rejected by policy") {
		t.Errorf("the relay's own reason was lost: %q", reason)
	}

	// And the writer can read that reason on their own screen rather than
	// asking an operator to open a log.
	w := server.request(http.MethodGet, "/api/v1/me/mail", nil, writer)
	if w.Code != http.StatusOK {
		t.Fatalf("read the preference: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "recipient rejected by policy") {
		t.Errorf("the screen does not carry why it has not arrived: %s", w.Body.String())
	}
}

// A relay that is unreachable for a couple of minutes is an ordinary event — a
// restart, a certificate reload, a network blip. With the attempts thirty
// seconds apart it spent a report's entire budget: measured on a deployment,
// five attempts and a permanent failure inside two and a half minutes.

// guards: retryDelay
func TestTheRetryBudgetOutlivesAnOrdinaryOutage(t *testing.T) {
	var total time.Duration
	for attempt := 1; attempt <= 5; attempt++ {
		total += retryDelay(attempt)
	}
	if total < time.Hour {
		t.Errorf("five attempts span %s — a relay restart would spend the whole budget", total)
	}
	// And each wait is longer than the last, so an outage that is still there
	// is asked about less often rather than more.
	for attempt := 2; attempt <= 4; attempt++ {
		if retryDelay(attempt) <= retryDelay(attempt-1) {
			t.Errorf("attempt %d waits %s, not longer than the %s before it",
				attempt, retryDelay(attempt), retryDelay(attempt-1))
		}
	}
	// Capped, or a long-running deployment would schedule a retry days out and
	// the row would look abandoned.
	if retryDelay(20) > 2*time.Hour {
		t.Errorf("the wait grows without bound: %s", retryDelay(20))
	}
	// The first wait still has to be short: most failures are momentary.
	if retryDelay(1) > 5*time.Minute {
		t.Errorf("the first retry waits %s, long enough to look broken", retryDelay(1))
	}
}

// One address the relay refuses used to hold the queue: the claim took the
// oldest queued row every tick, failed on it, and stopped. Everybody behind it
// waited for that row to burn through its attempts.

// guards: sendNextQueuedMail
func TestOneRefusedAddressDoesNotHoldUpEverybodyElse(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	// Only this address is refused, and it stays refused. Turning the refusal
	// off before queueing the second delivery would let the worker succeed on
	// the first one and prove nothing about reaching past it.
	relay.refuseFor = map[string]string{"blocked@internal.test": "recipient rejected by policy"}
	server.configureRelay(relay)

	refused := server.createUser("mail_block", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "blocked@internal.test", "onSubmit": true}, refused); w.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	server.submitted(refused, "2026-08-17", "막히는 사람")
	// The first delivery has to have been tried and deferred before the second
	// is queued, or this proves nothing about reaching past it.
	server.awaitDeliveryReason()

	fine := server.createUser("mail_after", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "after@internal.test", "onSubmit": true}, fine); w.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	server.submitted(fine, "2026-08-24", "뒤에 선 사람")

	messages := relay.awaitRelay(t, 1)
	if len(messages) == 0 {
		t.Fatal("the second writer's mail never went out — the refused one is still holding the queue")
	}
	if envelope := strings.Join(relay.envelopeLines(), " "); !strings.Contains(envelope, "after@internal.test") {
		t.Errorf("the delivered mail was not the second writer's: %s", envelope)
	}

	// And the refused one is waiting, not lost: it carries a time to try again.
	var status string
	var next *time.Time
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT status, next_attempt_at FROM report_mail_deliveries WHERE address='blocked@internal.test'`).
		Scan(&status, &next); err != nil {
		t.Fatalf("read the deferred delivery: %v", err)
	}
	if status != "QUEUED" {
		t.Errorf("the refused delivery is %s, want QUEUED", status)
	}
	if next == nil || !next.After(time.Now()) {
		t.Errorf("the refused delivery is due again immediately, so the queue will spin on it: %v", next)
	}
}

// The end of the queue. With the retries now spread over about three hours,
// nobody will reach this state by accident again — which is exactly why it
// needs a test: a delivery that is given up on has to say so and carry the last
// reason, or it sits as 대기 중 forever and the writer waits for a mail that is
// never coming.

// guards: sendNextQueuedMail, markMailFailed
func TestADeliveryThatRunsOutOfTriesSaysSoAndKeepsTheLastReason(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	relay.refuse = "mailbox unavailable"
	server.configureRelay(relay)
	// One try, so the budget is spent on the first refusal rather than three
	// hours from now.
	if w := server.request(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"settings": map[string]string{"mail.max_attempts": "1"}}, server.admin); w.Code != http.StatusOK {
		t.Fatalf("set the attempt budget: %d %s", w.Code, w.Body.String())
	}

	writer := server.createUser("mail_giveup", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "nowhere@internal.test", "onSubmit": true}, writer); w.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	server.submitted(writer, "2026-08-24", "받는 곳이 없는 주")

	status, reason, attempts := server.awaitDelivery("FAILED")
	if status != "FAILED" {
		t.Fatalf("status=%s after the budget ran out, want FAILED — the writer would wait forever", status)
	}
	if attempts != 1 {
		t.Errorf("attempts=%d, want 1 — the budget was not respected", attempts)
	}
	if !strings.Contains(reason, "mailbox unavailable") {
		t.Errorf("the last thing the relay said was lost: %q", reason)
	}

	// And it is not picked up again: a row that has been given up on must not
	// keep the worker busy.
	before := len(relay.received())
	server.app.sendNextQueuedMail(server.ctx())
	if len(relay.received()) != before {
		t.Error("a delivery that was given up on was tried again")
	}

	// The writer reads the verdict, not just the silence.
	w := server.request(http.MethodGet, "/api/v1/me/mail", nil, writer)
	if !strings.Contains(w.Body.String(), "FAILED") || !strings.Contains(w.Body.String(), "mailbox unavailable") {
		t.Errorf("the screen does not say it was given up on: %s", w.Body.String())
	}
}
