package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sending a finished weekly report to an address its writer chose.
//
// The relay is the one part of this feature that is not in the product's hands.
// So nothing here blocks a submission: the delivery is queued in the same
// transaction that files the report, and a worker carries it from there. A
// relay that is down delays a mail; it must never lose a week's work.

// mailSettings is the relay as an operator configured it.
type mailSettings struct {
	Enabled     bool
	Host        string
	Port        int
	Security    string // NONE, STARTTLS, TLS
	Username    string
	Password    string
	From        string
	FromName    string
	Timeout     time.Duration
	MaxAttempts int
	// PasswordUnreadable says the stored password is ciphertext this key cannot
	// open. It is a configuration state, not a transport failure, so it belongs
	// with the other things unusable() names.
	PasswordUnreadable bool
}

func (a *App) loadMailSettings(ctx context.Context) (mailSettings, error) {
	password, err := a.secretSetting(ctx, "mail.password")
	unreadable := false
	if err != nil {
		// A password that cannot be decrypted is not the same as no password:
		// sending without it would authenticate as nobody and be refused by the
		// relay with a message nobody could act on.
		//
		// It is also not a failure to read the settings. Returning one made the
		// worker log an ERROR every thirty seconds for a state that cannot fix
		// itself — the same every-thirty-seconds line this file goes out of its
		// way not to write when sending is merely off. It is a configuration
		// state, so it is carried as one and unusable() says it.
		if !errors.Is(err, errSecretUnreadable) {
			return mailSettings{}, fmt.Errorf("read mail password: %w", err)
		}
		unreadable, password = true, ""
	}
	return mailSettings{
		PasswordUnreadable: unreadable,
		Enabled:            a.settingBool(ctx, "mail.enabled", false),
		Host:               strings.TrimSpace(a.setting(ctx, "mail.host", "")),
		Port:               a.settingInt(ctx, "mail.port", 25),
		Security:           strings.ToUpper(strings.TrimSpace(a.setting(ctx, "mail.security", "NONE"))),
		Username:           strings.TrimSpace(a.setting(ctx, "mail.username", "")),
		Password:           password,
		From:               strings.TrimSpace(a.setting(ctx, "mail.from", "")),
		FromName:           strings.TrimSpace(a.setting(ctx, "mail.from_name", "Weekly")),
		Timeout:            time.Duration(a.settingInt(ctx, "mail.timeout_seconds", 20)) * time.Second,
		MaxAttempts:        a.settingInt(ctx, "mail.max_attempts", 5),
	}, nil
}

// unusable names what an operator has to fix, or "" when the relay can be used.
//
// Each of these fails at send time with a message from somebody else's server,
// so they are checked here where the wording can name the setting.
func (settings mailSettings) unusable() string {
	if !settings.Enabled {
		return "메일 발송이 꺼져 있습니다. 관리자 설정에서 켜십시오."
	}
	if settings.PasswordUnreadable {
		return "저장된 SMTP 비밀번호를 현재 암호화 키로 읽을 수 없습니다. 관리자 설정에서 비밀번호를 다시 입력하십시오."
	}
	if settings.Host == "" {
		return "SMTP 호스트가 비어 있습니다."
	}
	if settings.From == "" {
		return "보내는 주소가 비어 있습니다. 릴레이가 받아 주는 주소여야 합니다."
	}
	if !validMailAddress(settings.From) {
		return "보내는 주소 형식이 올바르지 않습니다."
	}
	if settings.Username != "" && !settings.encrypted() {
		// Go's SMTP client refuses to hand a password to an unencrypted
		// connection, so this combination cannot work however the relay is set
		// up. Saying it here names the two settings that disagree.
		return "계정을 쓰려면 보안 연결이 필요합니다. 보안을 STARTTLS 또는 TLS로 바꾸거나, 계정을 비우십시오."
	}
	return ""
}

// encrypted asks whether the connection will carry a password safely.
//
// Asking "is it NONE" instead was wrong in a way only a test found: an empty
// value — a setting never written, a struct built in code — is not NONE and was
// read as encrypted. sendMail treats everything that is not STARTTLS or TLS as
// a plain connection, so this has to as well, or the two disagree about the one
// case where a password is at stake.
func (settings mailSettings) encrypted() bool {
	return settings.Security == "STARTTLS" || settings.Security == "TLS"
}

// validMailAddress accepts one plain address and nothing else.
//
// net/mail.ParseAddress also accepts a display name and a list, both of which
// would put somebody else's text into a header we build. One address, no
// angle brackets, no comma, no newline.
func validMailAddress(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 320 || strings.ContainsAny(value, "<>,;\r\n \t\"") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	// The last two are belt and braces: nothing that survives the line above is
	// known to parse into a different address or carry a second @. They are
	// kept because what net/mail accepts is not this package's to promise, and
	// the cost of being wrong here is a header we built carrying somebody
	// else's address. A mutation of either is therefore not observable, which
	// is the expected result rather than a missing test.
	return err == nil && parsed.Address == value && strings.Count(value, "@") == 1
}

func validOptionalMailAddress(value string) bool {
	return strings.TrimSpace(value) == "" || validMailAddress(value)
}

// headerSafe strips what would end a header line and start another one.
//
// The subject carries a report title and a person's name, both of which come
// from what somebody typed. A newline in either would let that text add its own
// headers — a Bcc, another recipient — to a message the product is sending on
// their behalf.
func headerSafe(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, value)
}

// mailMessageID returns a Message-ID nothing else will repeat.
//
// The local part is random rather than derived from the delivery, because a
// retry of the same report is a second message and a receiver that dedupes on
// this header would drop it.
func mailMessageID(from string) string {
	domain := "weekly.local"
	if at := strings.LastIndex(from, "@"); at >= 0 && at+1 < len(from) {
		if candidate := headerSafe(from[at+1:]); candidate != "" {
			domain = candidate
		}
	}
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		// Randomness is not available; the timestamp alone still separates two
		// messages from this process, which is better than no header at all.
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), domain)
	}
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), hex.EncodeToString(buffer), domain)
}

// buildMailMessage returns one RFC 5322 message.
//
// Base64 for the body and an encoded-word for the subject, because everything
// this product writes is Korean and a relay that is not 8-bit clean would
// deliver it as mojibake. Both are plain ASCII on the wire.
//
// Date and Message-ID are written here because nothing downstream will. RFC 5322
// names Date and From as the two required header fields, and a submission server
// is the thing that fills in a missing one — but this product relays to an
// internal MTA on port 25, which does not. Captured off the wire from a running
// deployment, the message carried From, To, Subject, MIME-Version, Content-Type,
// Content-Transfer-Encoding and Auto-Submitted, and neither of these. A reader's
// client then has no send time to show or sort by, and no key to thread or
// de-duplicate on.
func buildMailMessage(from, fromName, to, subject, body string) []byte {
	sender := from
	if fromName != "" {
		sender = fmt.Sprintf("%s <%s>", mime.BEncoding.Encode("UTF-8", headerSafe(fromName)), from)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	var wrapped strings.Builder
	for index := 0; index < len(encoded); index += 76 {
		end := index + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		wrapped.WriteString(encoded[index:end])
		wrapped.WriteString("\r\n")
	}
	headers := []string{
		"Date: " + time.Now().Format(time.RFC1123Z),
		"From: " + sender,
		"To: " + headerSafe(to),
		"Message-ID: " + mailMessageID(from),
		"Subject: " + mime.BEncoding.Encode("UTF-8", headerSafe(subject)),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
		"Auto-Submitted: auto-generated",
	}
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + wrapped.String())
}

// mailTLSConfig builds the client's TLS settings for one relay.
//
// A seam, and the reason for it is worth stating: an internal relay's
// certificate is signed by nobody a test process trusts, so without this the
// encrypted branch — the one where a password is at stake — could not be
// exercised at all, and a mutation run confirmed that both it and the
// authentication that follows were unguarded. Production never replaces this.
var mailTLSConfig = func(host string) *tls.Config { return &tls.Config{ServerName: host} }

// sendMail delivers one message and returns the relay's own refusal on failure.
func (a *App) sendMail(ctx context.Context, settings mailSettings, to, subject, body string) error {
	if reason := settings.unusable(); reason != "" {
		return errors.New(reason)
	}
	if !validMailAddress(to) {
		return errors.New("받는 주소 형식이 올바르지 않습니다.")
	}
	address := net.JoinHostPort(settings.Host, fmt.Sprint(settings.Port))
	dialer := &net.Dialer{Timeout: settings.Timeout}

	var connection net.Conn
	var err error
	if settings.Security == "TLS" {
		connection, err = tls.DialWithDialer(dialer, "tcp", address, mailTLSConfig(settings.Host))
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("연결할 수 없습니다: %w", err)
	}
	// Bounds the whole conversation, not just the dial. A relay that accepts a
	// connection and then says nothing used to hold the worker forever.
	_ = connection.SetDeadline(time.Now().Add(settings.Timeout))
	defer connection.Close()

	client, err := smtp.NewClient(connection, settings.Host)
	if err != nil {
		return fmt.Errorf("SMTP 세션을 열 수 없습니다: %w", err)
	}
	defer client.Close()

	if settings.Security == "STARTTLS" {
		if err := client.StartTLS(mailTLSConfig(settings.Host)); err != nil {
			return fmt.Errorf("STARTTLS에 실패했습니다: %w", err)
		}
	}
	if settings.Username != "" {
		auth := smtp.PlainAuth("", settings.Username, settings.Password, settings.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("계정 인증에 실패했습니다: %w", err)
		}
	}
	if err := client.Mail(settings.From); err != nil {
		return fmt.Errorf("보내는 주소를 릴레이가 거부했습니다: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("받는 주소를 릴레이가 거부했습니다: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("본문을 보낼 수 없습니다: %w", err)
	}
	if _, err := writer.Write(buildMailMessage(settings.From, settings.FromName, to, subject, body)); err != nil {
		return fmt.Errorf("본문을 보낼 수 없습니다: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("릴레이가 본문을 받지 않았습니다: %w", err)
	}
	return client.Quit()
}

// ---------------------------------------------------------------------------
// The report as a mail
// ---------------------------------------------------------------------------

// reportMailSubject is what shows in a mailbox list, so it carries the two
// things that tell one week's mail from another.
func reportMailSubject(serviceName string, report *reportView) string {
	return fmt.Sprintf("[%s] %s 주간보고 · %s", serviceName, report.WeekStart, report.DisplayName)
}

// reportMailBody writes the report the way it reads on the screen.
//
// Plain text on purpose. These deployments read mail in whatever the office
// standardised on years ago, and a layout that survives that is worth more than
// one that looks better in a modern client. The order is the report's own:
// summary, then each task with what happened, what is next, and what is in the
// way.
func reportMailBody(report *reportView, statusLabel string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s 주간보고\n", report.WeekStart)
	fmt.Fprintf(&out, "작성자: %s (%s)\n", report.DisplayName, report.Username)
	fmt.Fprintf(&out, "상태: %s\n", statusLabel)
	if summary := strings.TrimSpace(report.Summary); summary != "" {
		fmt.Fprintf(&out, "\n[주간 요약]\n%s\n", summary)
	}
	fmt.Fprintf(&out, "\n업무 %d건\n", len(report.Items))
	for index, item := range report.Items {
		fmt.Fprintf(&out, "\n%d. [%s] %s (%d%%)\n", index+1, item.Category, item.Title, item.Progress)
		writeMailField(&out, "금주 실적", item.CurrentResult)
		writeMailField(&out, "차주 계획", item.NextPlan)
		writeMailField(&out, "이슈", item.Issue)
		writeMailField(&out, "요청 사항", item.ManagementAsk)
	}
	out.WriteString("\n--\n이 메일은 주간보고를 제출할 때 자동으로 발송됩니다.\n")
	out.WriteString("받지 않으려면 개인 설정에서 발송을 끄십시오.\n")
	return out.String()
}

// writeMailField skips what was not written rather than printing an empty
// heading. A mail full of "이슈: -" is harder to read than one without them.
func writeMailField(out *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(out, "   %s: %s\n", label, strings.ReplaceAll(value, "\n", "\n      "))
}

// reportStatusLabel is the same word the screen uses for this state.
func reportStatusLabel(status string) string {
	switch status {
	case "DRAFT":
		return "작성 중"
	case "SUBMITTED":
		return "검토 대기"
	case "REVISION_REQUESTED":
		return "반려/수정"
	case "APPROVED":
		return "승인"
	case "CLOSED":
		return "확정"
	}
	return status
}

// ---------------------------------------------------------------------------
// The queue
// ---------------------------------------------------------------------------

// queueReportMail records that this report should be sent, inside the caller's
// transaction.
//
// Inside, so that a report that was filed always has its delivery recorded and
// a delivery is never recorded for a report that was not. Nothing is sent here:
// the relay is somebody else's machine and a submission must not wait on it.
//
// A writer who has not set an address, or has turned sending off, queues
// nothing — the row exists to be delivered, not to record a preference.
func queueReportMail(ctx context.Context, tx pgx.Tx, reportID, userID int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO report_mail_deliveries(report_id, user_id, address)
		SELECT $1, s.user_id, s.address FROM user_mail_settings s
		WHERE s.user_id = $2 AND s.on_submit = true AND s.address <> ''`, reportID, userID)
	return err
}

// mailWorker carries queued reports to the relay.
//
// Woken by a submission and on a timer, because the reason a delivery is still
// queued is usually that the relay was down a moment ago and nothing will wake
// this again until somebody else submits.
func (a *App) mailWorker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.mailWake:
		case <-ticker.C:
		}
		for {
			reportMore := a.sendNextQueuedMail(ctx)
			reminderMore := a.sendNextQueuedReminder(ctx)
			if !reportMore && !reminderMore {
				break
			}
		}
	}
}

// wakeMailWorker nudges the worker without blocking the caller if it is busy.
func (a *App) wakeMailWorker() {
	select {
	case a.mailWake <- struct{}{}:
	default:
	}
}

// sendNextQueuedMail delivers one report and reports whether to look for more.
func (a *App) sendNextQueuedMail(ctx context.Context) bool {
	settings, err := a.loadMailSettings(ctx)
	if err != nil {
		a.logger.Error("mail settings", "error", err)
		return false
	}
	if reason := settings.unusable(); reason != "" {
		// Not an error to log every thirty seconds: sending is off, or half
		// configured, which is the normal state of a deployment that does not
		// use this. The queue waits; nothing is lost and nothing is retried
		// against a relay that cannot work yet.
		return false
	}

	var deliveryID, reportID int64
	var address string
	var attempts int
	// Only what is due. A row that has just failed carries a time in the future,
	// so this reaches past it to somebody else's mail instead of spending the
	// tick on the same refusal again.
	err = a.db.QueryRow(ctx, `
		UPDATE report_mail_deliveries SET attempts = attempts + 1
		WHERE id = (SELECT id FROM report_mail_deliveries
		            WHERE status = 'QUEUED' AND next_attempt_at <= now()
		            ORDER BY next_attempt_at, created_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, report_id, address, attempts`).Scan(&deliveryID, &reportID, &address, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		// The queue is empty. The ordinary case, and not worth a line in a log
		// that would then carry one every thirty seconds forever.
		return false
	}
	if err != nil {
		// A store that cannot be read is not an empty queue, and answering the
		// same way for both is how a broken worker looks exactly like an idle
		// one. This cost an afternoon in its own test.
		a.logger.Error("claim queued mail", "error", err)
		return false
	}

	report, err := a.loadReport(ctx, reportID)
	if err != nil || report == nil {
		a.markMailFailed(ctx, deliveryID, "보고서를 읽을 수 없습니다.")
		return true
	}
	subject := reportMailSubject(a.setting(ctx, "service.name", "Weekly"), report)
	body := reportMailBody(report, reportStatusLabel(report.Status))

	if err := a.sendMail(ctx, settings, address, subject, body); err != nil {
		if attempts >= settings.MaxAttempts {
			a.logger.Error("report mail gave up", "delivery", deliveryID, "attempts", attempts, "error", err)
			a.markMailFailed(ctx, deliveryID, mailUserMessage(err))
			return true
		}
		// Left QUEUED with the reason on it, so the writer sees why it has not
		// arrived yet rather than only that it has not, and dated so the next
		// try is far enough away to outlast the outage that caused this one.
		if _, dbErr := a.db.Exec(ctx, `UPDATE report_mail_deliveries
			SET error_message=$2, next_attempt_at = now() + $3::interval WHERE id=$1`,
			deliveryID, trimRunes(mailUserMessage(err), 1000), retryDelay(attempts).String()); dbErr != nil {
			// Without the new time this row is due again immediately and the
			// queue spins on it, so a write that failed is worth saying.
			a.logger.Error("defer mail retry", "delivery", deliveryID, "error", dbErr)
		}
		a.logger.Warn("report mail retry", "delivery", deliveryID, "attempts", attempts, "error", err)
		// Stop the sweep: the next one would fail the same way against the same
		// relay, and hammering it is not a retry policy.
		return false
	}
	if _, err := a.db.Exec(ctx, `UPDATE report_mail_deliveries SET status='SENT',sent_at=now(),error_message='' WHERE id=$1`, deliveryID); err != nil {
		a.logger.Error("mark mail sent", "delivery", deliveryID, "error", err)
	}
	return true
}

// retryDelay spaces the attempts so the budget outlives an ordinary outage.
//
// Four to the power of the attempt: one minute, four, sixteen, an hour, then
// capped at two. Five attempts therefore span about three hours rather than the
// two and a half minutes a fixed thirty-second gap gave them — long enough that
// a relay restart, a certificate reload or a network blip does not spend a
// report's whole budget while nobody is looking.
func retryDelay(attempts int) time.Duration {
	delay := time.Minute
	for index := 1; index < attempts && delay < 2*time.Hour; index++ {
		delay *= 4
	}
	if delay > 2*time.Hour {
		delay = 2 * time.Hour
	}
	return delay
}

// mailUserMessage keeps the relay's own words and drops this deployment's.
//
// The reason on a delivery row is shown on 개인 설정 to the writer, and what a
// relay says about an address is exactly what they need — "받는 주소를 릴레이가
// 거부했습니다: 550 5.1.1 mailbox unavailable" tells somebody their address is
// wrong, and TestARefusedDeliveryKeepsTheRelaysOwnReason exists to keep it
// reaching them.
//
// What has no business there is the transport underneath. Measured on a
// deployment with the relay unreachable, the same field read "연결할 수
// 없습니다: dial tcp 10.20.0.25:25: connect: connection refused" and "…lookup
// smtp.internal.example on 127.0.0.11:53: no such host": the relay's address,
// its port and the container's resolver, on the screens of 61 ordinary writers
// who can do nothing with any of it. The relay did not say those things — Go
// did, about our own network.
//
// So only the failures that never got as far as a reply are replaced. The whole
// error stays in the server log either way.
func mailUserMessage(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	lowered := strings.ToLower(text)
	switch {
	case strings.Contains(lowered, "no such host"), strings.Contains(lowered, "connection refused"),
		strings.Contains(lowered, "dial "), strings.Contains(lowered, "i/o timeout"),
		strings.Contains(lowered, "deadline exceeded"), strings.Contains(lowered, "network is unreachable"):
		return "릴레이에 연결하지 못했습니다. 잠시 뒤 다시 시도합니다."
	case strings.Contains(lowered, "x509"), strings.Contains(lowered, "certificate"),
		strings.Contains(lowered, "tls:"):
		return "릴레이와 암호화 연결을 맺지 못했습니다. 관리자에게 알려 주세요."
	case strings.Contains(text, "계정 인증에 실패했습니다"):
		return "릴레이가 계정 인증을 거부했습니다. 관리자에게 알려 주세요."
	}
	return text
}

func (a *App) markMailFailed(ctx context.Context, deliveryID int64, reason string) {
	if _, err := a.db.Exec(ctx, `UPDATE report_mail_deliveries SET status='FAILED',error_message=$2 WHERE id=$1`,
		deliveryID, trimRunes(reason, 1000)); err != nil {
		a.logger.Error("mark mail failed", "delivery", deliveryID, "error", err)
	}
}

// ---------------------------------------------------------------------------
// What a writer sets, and what they can see happened
// ---------------------------------------------------------------------------

type mailPreferenceView struct {
	// RelayReady says whether an operator has configured sending at all. A
	// writer who turns this on and never receives anything is owed the reason,
	// and "the administrator has not set up a mail server" is not something the
	// screen could otherwise know.
	RelayReady bool               `json:"relayReady"`
	Address    string             `json:"address"`
	OnSubmit   bool               `json:"onSubmit"`
	Deliveries []mailDeliveryView `json:"deliveries"`
}

type mailDeliveryView struct {
	ID        int64      `json:"id"`
	WeekStart string     `json:"weekStart"`
	Address   string     `json:"address"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	Error     string     `json:"error"`
	CreatedAt time.Time  `json:"createdAt"`
	SentAt    *time.Time `json:"sentAt"`
	// When the worker will try again. A delivery that is waiting is a different
	// thing from one that is stuck, and only the date tells them apart.
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
}

// mailRecent is how many past attempts the screen shows. Enough to cover a
// couple of months of weeks without turning the card into a log.
const mailRecent = 10

func (a *App) myMailSettings(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	view := mailPreferenceView{Deliveries: []mailDeliveryView{}}

	settings, err := a.loadMailSettings(r.Context())
	view.RelayReady = err == nil && settings.unusable() == ""

	if err := a.db.QueryRow(r.Context(),
		`SELECT address, on_submit FROM user_mail_settings WHERE user_id=$1`, p.ID).
		Scan(&view.Address, &view.OnSubmit); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.logger.Error("read mail preference", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "메일 발송 설정을 읽을 수 없습니다.")
		return
	}

	rows, err := a.db.Query(r.Context(), `
		SELECT d.id, r.week_start, d.address, d.status, d.attempts, d.error_message,
		       d.created_at, d.sent_at,
		       CASE WHEN d.status = 'QUEUED' AND d.attempts > 0 THEN d.next_attempt_at END
		FROM report_mail_deliveries d JOIN weekly_reports r ON r.id = d.report_id
		WHERE d.user_id = $1 ORDER BY d.created_at DESC, d.id DESC LIMIT $2`, p.ID, mailRecent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "발송 이력을 읽을 수 없습니다.")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var delivery mailDeliveryView
		var week time.Time
		if err := rows.Scan(&delivery.ID, &week, &delivery.Address, &delivery.Status,
			&delivery.Attempts, &delivery.Error, &delivery.CreatedAt, &delivery.SentAt,
			&delivery.NextAttemptAt); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "발송 이력을 읽을 수 없습니다.")
			return
		}
		delivery.WeekStart = week.Format("2006-01-02")
		view.Deliveries = append(view.Deliveries, delivery)
	}
	writeData(w, http.StatusOK, view)
}

func (a *App) updateMyMailSettings(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	var input struct {
		Address  string `json:"address"`
		OnSubmit bool   `json:"onSubmit"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Address = strings.TrimSpace(input.Address)
	if input.Address != "" && !validMailAddress(input.Address) {
		writeError(w, http.StatusBadRequest, "INVALID_MAIL_ADDRESS", "받을 메일 주소 형식이 올바르지 않습니다.")
		return
	}
	// Turning it on without an address is the one combination that would look
	// configured and deliver nothing, so it is refused here rather than
	// discovered as a silent week.
	if input.OnSubmit && input.Address == "" {
		writeError(w, http.StatusBadRequest, "MAIL_ADDRESS_REQUIRED", "발송을 켜려면 받을 주소를 먼저 입력하세요.")
		return
	}
	if _, err := a.db.Exec(r.Context(), `
		INSERT INTO user_mail_settings(user_id, address, on_submit, updated_at)
		VALUES($1, $2, $3, now())
		ON CONFLICT (user_id) DO UPDATE SET address=EXCLUDED.address, on_submit=EXCLUDED.on_submit, updated_at=now()`,
		p.ID, input.Address, input.OnSubmit); err != nil {
		a.logger.Error("save mail preference", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "메일 발송 설정을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "mail.preference", "user", fmt.Sprint(p.ID), map[string]any{"onSubmit": input.OnSubmit})
	writeData(w, http.StatusOK, map[string]any{"address": input.Address, "onSubmit": input.OnSubmit})
}

// testMail sends one message to the administrator running the test.
//
// To them, not to an address they type: a test that can be pointed anywhere is
// a way to send mail from this server to a stranger, and the question being
// asked here is only whether the relay works.
func (a *App) testMail(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	settings, err := a.loadMailSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "MAIL_CONFIGURATION_INVALID", "메일 설정을 읽을 수 없습니다.")
		return
	}
	if reason := settings.unusable(); reason != "" {
		writeError(w, http.StatusBadRequest, "MAIL_CONFIGURATION_INVALID", reason)
		return
	}
	var to string
	if err := a.db.QueryRow(r.Context(),
		`SELECT coalesce(s.address, '') FROM (SELECT $1::bigint AS id) x
		 LEFT JOIN user_mail_settings s ON s.user_id = x.id`, p.ID).Scan(&to); err != nil {
		to = ""
	}
	if to == "" {
		to = strings.TrimSpace(p.Email)
	}
	if !validMailAddress(to) {
		writeError(w, http.StatusBadRequest, "MAIL_ADDRESS_REQUIRED",
			"시험 메일을 받을 주소가 없습니다. 개인 설정에서 받을 주소를 먼저 저장하세요.")
		return
	}
	subject := fmt.Sprintf("[%s] 메일 발송 시험", a.setting(r.Context(), "service.name", "Weekly"))
	body := fmt.Sprintf("이 메일이 도착했다면 SMTP 설정이 동작합니다.\n\n릴레이: %s:%d (%s)\n보내는 주소: %s\n",
		settings.Host, settings.Port, settings.Security, settings.From)
	if err := a.sendMail(r.Context(), settings, to, subject, body); err != nil {
		a.logger.Error("mail test", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusBadGateway, "MAIL_SEND_FAILED", err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true, "to": to})
}

// ---------------------------------------------------------------------------
// What the operator can see
// ---------------------------------------------------------------------------

// mailHealthDays is how far back the administrator's figures reach. Long enough
// to cover a fortnight of Mondays, short enough that a relay fixed last month
// does not keep the screen red.
const mailHealthDays = 14

type mailHealthView struct {
	Days       int        `json:"days"`
	Sent       int        `json:"sent"`
	Queued     int        `json:"queued"`
	Failed     int        `json:"failed"`
	Writers    int        `json:"writers"`
	LastError  string     `json:"lastError"`
	LastFailed *time.Time `json:"lastFailedAt"`
}

// adminMailHealth answers what has actually been happening to the mail.
//
// A writer sees their own deliveries; the operator who owns the relay saw
// nothing. So a relay that is refusing everybody looks, from the settings
// screen, exactly like one nobody has used yet — and the test button only says
// whether it works this second, not whether last Monday went out.
func (a *App) adminMailHealth(w http.ResponseWriter, r *http.Request) {
	view := mailHealthView{Days: mailHealthDays}
	if err := a.db.QueryRow(r.Context(), `
		SELECT
			count(*) FILTER (WHERE status = 'SENT'),
			count(*) FILTER (WHERE status = 'QUEUED'),
			count(*) FILTER (WHERE status = 'FAILED'),
			count(DISTINCT user_id)
		FROM report_mail_deliveries
		WHERE created_at > now() - make_interval(days => $1)`, mailHealthDays).
		Scan(&view.Sent, &view.Queued, &view.Failed, &view.Writers); err != nil {
		a.logger.Error("mail health", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "메일 발송 현황을 읽을 수 없습니다.")
		return
	}
	// The relay's own words, not a count. "3건 실패" sends an operator to a log;
	// "받는 주소를 릴레이가 거부했습니다: 550 …" sends them to the right place.
	if err := a.db.QueryRow(r.Context(), `
		SELECT coalesce(error_message, ''), created_at FROM report_mail_deliveries
		WHERE error_message <> '' AND created_at > now() - make_interval(days => $1)
		ORDER BY created_at DESC LIMIT 1`, mailHealthDays).
		Scan(&view.LastError, &view.LastFailed); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.logger.Error("mail health reason", "error", err, "trace", traceIDFromContext(r.Context()))
	}
	writeData(w, http.StatusOK, view)
}
