package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 메일 수신 끄기: every mail the service sends on its own carries a way out
// that needs no sign-in, and that way out can turn off what it names and
// nothing else.

func (s *testServer) setSetting(key, value string) {
	s.t.Helper()
	if w := s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{key: value}}, s.admin); w.Code != http.StatusOK {
		s.t.Fatalf("set %s: %d %s", key, w.Code, w.Body.String())
	}
}

// unsubscribeForm posts the way a browser form or a mail client does: no
// session, no Origin, a form body.
func (s *testServer) unsubscribeForm(token string, form url.Values) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mail/unsubscribe?t="+url.QueryEscape(token), strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(w, r)
	return w
}

func (s *testServer) mailSwitches(userID int64) map[string]bool {
	s.t.Helper()
	var report, schedule, team bool
	if err := s.app.db.QueryRow(s.ctx(), `SELECT on_submit, schedule_reminder, team_reminder
		FROM user_mail_settings WHERE user_id=$1`, userID).Scan(&report, &schedule, &team); err != nil {
		s.t.Fatal(err)
	}
	return map[string]bool{mailKindReport: report, mailKindSchedule: schedule, mailKindTeam: team}
}

// guards: mailUnsubscribeToken, parseMailUnsubscribeToken, mailUnsubscribePage, mailUnsubscribe, turnMailOff
func TestTheUnsubscribeLinkTurnsOffOnlyWhatItNames(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("수신 끄기 조직", "UNSUB")
	leader := createAutomationTestAccount(t, server, "unsub_leader", "TEAM_LEADER", &organization)
	reader := createAutomationTestAccount(t, server, "unsub_reader", "USER", &organization)
	other := createAutomationTestAccount(t, server, "unsub_other", "USER", &organization)
	for _, account := range []automationTestAccount{reader, other} {
		if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
			"address": account.username + "@internal.test", "onSubmit": true, "scheduleReminder": true, "teamReminder": true,
		}, account.cookie); w.Code != http.StatusOK {
			t.Fatalf("turn everything on: %d %s", w.Code, w.Body.String())
		}
	}
	week := currentWeekStart(time.Now().In(server.app.serviceLocation(server.ctx())),
		server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO team_reminder_deliveries
		(requested_by,recipient_user_id,week_start,address,origin,next_attempt_at)
		VALUES($1,$2,$3,'r@internal.test','MANUAL',now()+interval '1 hour')`, leader.id, reader.id, week); err != nil {
		t.Fatal(err)
	}

	teamToken := server.app.mailUnsubscribeToken(reader.id, mailKindTeam)

	// Looking is not unsubscribing: scanners open every link in a message.
	page := httptest.NewRecorder()
	server.app.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/mail/unsubscribe?t="+url.QueryEscape(teamToken), nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "주간보고 작성 권고 메일") ||
		!strings.Contains(page.Body.String(), "모든 메일 끄기") {
		t.Fatalf("the page: %d %s", page.Code, page.Body.String())
	}
	if page.Header().Get("Referrer-Policy") != "no-referrer" || page.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("the page leaks its token: Referrer-Policy=%q Cache-Control=%q",
			page.Header().Get("Referrer-Policy"), page.Header().Get("Cache-Control"))
	}
	if switches := server.mailSwitches(reader.id); !switches[mailKindTeam] {
		t.Fatal("opening the page turned the reminder off")
	}

	// A token is this instance's signature over one person and one kind.
	parts := strings.Split(teamToken, ".")
	forged := []string{
		"", "garbage", teamToken + "x",
		fmt.Sprintf("%d.%s.%s", other.id, parts[1], parts[2]),
		fmt.Sprintf("%s.%s.%s", parts[0], mailKindReport, parts[2]),
		fmt.Sprintf("%s.%s.%s", parts[0], "everything", parts[2]),
	}
	for _, token := range forged {
		if w := server.unsubscribeForm(token, url.Values{"scope": {"all"}}); w.Code != http.StatusBadRequest {
			t.Errorf("forged token %q: %d", token, w.Code)
		}
	}
	if switches := server.mailSwitches(other.id); !switches[mailKindReport] || !switches[mailKindSchedule] || !switches[mailKindTeam] {
		t.Fatalf("a forged token changed somebody's mail: %v", switches)
	}

	// The page's first button: only what the link names.
	if w := server.unsubscribeForm(teamToken, url.Values{"scope": {"kind"}}); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "수신을 껐습니다") {
		t.Fatalf("turn the reminder off: %d %s", w.Code, w.Body.String())
	}
	if got := server.mailSwitches(reader.id); fmt.Sprint(got) != fmt.Sprint(map[string]bool{
		mailKindReport: true, mailKindSchedule: true, mailKindTeam: false}) {
		t.Errorf("after turning the reminder off: %v", got)
	}
	var waiting int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries
		WHERE recipient_user_id=$1 AND status='QUEUED'`, reader.id).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting != 0 {
		t.Errorf("a reminder already waiting still goes out after its reader turned reminders off")
	}

	// A mail client's own button (RFC 8058) turns off what the header named,
	// whatever else the body says.
	scheduleToken := server.app.mailUnsubscribeToken(reader.id, mailKindSchedule)
	if w := server.unsubscribeForm(scheduleToken, url.Values{"List-Unsubscribe": {"One-Click"}, "scope": {"all"}}); w.Code != http.StatusOK {
		t.Fatalf("one-click: %d %s", w.Code, w.Body.String())
	}
	if got := server.mailSwitches(reader.id); !got[mailKindReport] || got[mailKindSchedule] {
		t.Errorf("one-click turned off more or less than the digest: %v", got)
	}

	// The second button: everything.
	if w := server.unsubscribeForm(server.app.mailUnsubscribeToken(other.id, mailKindReport), url.Values{"scope": {"all"}}); w.Code != http.StatusOK {
		t.Fatalf("everything off: %d %s", w.Code, w.Body.String())
	}
	if got := server.mailSwitches(other.id); got[mailKindReport] || got[mailKindSchedule] || got[mailKindTeam] {
		t.Errorf("모든 메일 끄기 left something on: %v", got)
	}

	var audits int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM audit_logs WHERE action='mail.unsubscribe'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 3 {
		t.Errorf("%d unsubscribe audit rows, want 3", audits)
	}
}

// guards: mailFooter, mailUnsubscribeHeaders, mailLinksFor, publicBaseURL, sendNextQueuedReminder, sendMailWithHeaders
func TestEveryAutomaticMailSaysWhyItCameAndHowToStopIt(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	server.setSetting("service.public_url", "https://weekly.example.test/")
	organization := server.createOrganization("수신 끄기 링크 조직", "UNSUBLINK")
	leader := createAutomationTestAccount(t, server, "unsublink_leader", "TEAM_LEADER", &organization)
	reader := createAutomationTestAccount(t, server, "unsublink_reader", "USER", &organization)
	setAutomationTestEmail(t, server, reader, "unsublink-reader@internal.test")

	week := currentWeekStart(time.Now().In(server.app.serviceLocation(server.ctx())),
		server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	if _, err := server.app.queueTeamReminders(server.ctx(), leader.id, week, teamReminderOriginManual, ""); err != nil {
		t.Fatal(err)
	}
	server.app.wakeMailWorker()
	messages := relay.awaitRelay(t, 1)
	if len(messages) != 1 {
		t.Fatalf("relay received %d messages", len(messages))
	}
	parsed, err := mail.ReadMessage(strings.NewReader(messages[0]))
	if err != nil {
		t.Fatal(err)
	}
	header := parsed.Header.Get("List-Unsubscribe")
	if !strings.HasPrefix(header, "<https://weekly.example.test/mail/unsubscribe?t=") || !strings.HasSuffix(header, ">") {
		t.Fatalf("List-Unsubscribe=%q", header)
	}
	if parsed.Header.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" {
		t.Errorf("an HTTPS link without one-click: %q", parsed.Header.Get("List-Unsubscribe-Post"))
	}
	link := strings.Trim(header, "<>")
	address, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if userID, kind, ok := server.app.parseMailUnsubscribeToken(address.Query().Get("t")); !ok || userID != reader.id || kind != mailKindTeam {
		t.Errorf("the link names user %d kind %q (ok=%v), want the reader's reminder", userID, kind, ok)
	}
	body := decodedBody(t, messages[0])
	for _, fragment := range []string{"'작성 권고 메일 받기'를 켜 두셔서", link, "https://weekly.example.test/#/profile"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("the reminder does not carry %q:\n%s", fragment, body)
		}
	}

	// Without a configured address the Keycloak redirect URL's origin stands
	// in; without either there is no link, and the footer says where the
	// switch is in words.
	server.setSetting("service.public_url", "")
	server.setSetting("oidc.redirect_url", "http://weekly.internal:8080/api/v1/auth/oidc/callback")
	if base := server.app.publicBaseURL(server.ctx()); base != "http://weekly.internal:8080" {
		t.Errorf("base from the redirect URL = %q", base)
	}
	links := server.app.mailLinksFor(server.ctx(), reader.id, mailKindSchedule)
	if headers := mailUnsubscribeHeaders(links); len(headers) != 1 || !strings.HasPrefix(headers[0], "List-Unsubscribe: <http://") {
		t.Errorf("plain HTTP gets the link but not one-click (RFC 8058 is HTTPS only): %v", headers)
	}
	server.setSetting("oidc.redirect_url", "")
	links = server.app.mailLinksFor(server.ctx(), reader.id, mailKindReport)
	if links.unsubscribe != "" || mailUnsubscribeHeaders(links) != nil {
		t.Errorf("a link was made with no address to make it from: %+v", links)
	}
	if footer := mailFooter(mailKindReport, links); !strings.Contains(footer, "개인 설정 → 주간보고 메일 발송에서 '제출할 때 보내기'를 끄십시오") {
		t.Errorf("without a link the footer does not say where the switch is:\n%s", footer)
	}
}

// guards: previewMyTeamReminders, previewTeamReminders, planTeamReminders
func TestTheReminderPreviewIsWhatTheButtonDoesAndWritesNothing(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	organization := server.createOrganization("권고 미리보기 조직", "REMINDPREVIEW")
	office := server.createChildOrganization("임원실", "REMINDPREVIEWEXEC", &organization)
	leader := createAutomationTestAccount(t, server, "preview_leader", "TEAM_LEADER", &organization)
	first := createAutomationTestAccount(t, server, "preview_first", "USER", &organization)
	second := createAutomationTestAccount(t, server, "preview_second", "USER", &organization)
	silent := createAutomationTestAccount(t, server, "preview_silent", "USER", &organization)
	executive := createAutomationTestAccount(t, server, "preview_exec", "USER", &office)
	for _, account := range []automationTestAccount{first, second, executive} {
		setAutomationTestEmail(t, server, account, account.username+"@internal.test")
	}
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET email='preview-silent@internal.test' WHERE id=$1`, silent.id); err != nil {
		t.Fatal(err)
	}

	if w := server.request(http.MethodGet, "/api/v1/me/team-reminders/preview", nil, first.cookie); w.Code != http.StatusForbidden {
		t.Errorf("a member previewed the leader's button: %d", w.Code)
	}
	w := server.request(http.MethodGet, "/api/v1/me/team-reminders/preview", nil, leader.cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	preview := decodeData(t, w)
	if preview["queued"] != float64(2) || preview["skippedNotOptedIn"] != float64(1) || preview["skippedExcluded"] != float64(1) {
		t.Fatalf("preview counts: %v", preview)
	}
	var rows int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("the preview queued %d reminder(s)", rows)
	}

	sent := decodeData(t, server.request(http.MethodPost, "/api/v1/me/team-reminders", map[string]any{}, leader.cookie))
	for _, key := range []string{"eligible", "queued", "alreadyQueued", "skippedNoAddress", "skippedNotOptedIn", "skippedExcluded"} {
		if sent[key] != preview[key] {
			t.Errorf("%s: the preview said %v, the button did %v", key, preview[key], sent[key])
		}
	}
	again := decodeData(t, server.request(http.MethodGet, "/api/v1/me/team-reminders/preview", nil, leader.cookie))
	if again["queued"] != float64(0) || again["alreadyQueued"] != float64(2) {
		t.Errorf("after sending, the preview reads %v", again)
	}
}

// guards: adminMailHealth
func TestTheOperatorSeesWhoWantsEachMail(t *testing.T) {
	server := newTestServer(t)
	read := func() map[string]any {
		t.Helper()
		w := server.request(http.MethodGet, "/api/v1/admin/mail/health", nil, server.admin)
		if w.Code != http.StatusOK {
			t.Fatalf("mail health: %d %s", w.Code, w.Body.String())
		}
		subscribers, _ := decodeData(t, w)["subscribers"].(map[string]any)
		return subscribers
	}
	before := read()
	organization := server.createOrganization("수신 현황 조직", "MAILREACH")
	office := server.createOrganization("임원실", "MAILREACHEXEC")
	everything := createAutomationTestAccount(t, server, "reach_all", "USER", &organization)
	reminderOnly := createAutomationTestAccount(t, server, "reach_reminder", "USER", &organization)
	createAutomationTestAccount(t, server, "reach_none", "USER", &organization)
	executive := createAutomationTestAccount(t, server, "reach_exec", "USER", &office)
	for _, account := range []automationTestAccount{everything, executive} {
		if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
			"address": account.username + "@internal.test", "onSubmit": true, "scheduleReminder": true, "teamReminder": true,
		}, account.cookie); w.Code != http.StatusOK {
			t.Fatalf("turn on: %d %s", w.Code, w.Body.String())
		}
	}
	setAutomationTestEmail(t, server, reminderOnly, "reach-reminder@internal.test")
	after := read()
	delta := func(key string) int { return int(after[key].(float64) - before[key].(float64)) }
	want := map[string]int{"activeUsers": 4, "excludedUsers": 1, "report": 1, "scheduleReminder": 1, "teamReminder": 2}
	for key, value := range want {
		if got := delta(key); got != value {
			t.Errorf("%s grew by %d, want %d", key, got, value)
		}
	}
}
