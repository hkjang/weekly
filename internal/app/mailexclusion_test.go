package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 작성 권고 메일은 받겠다고 한 사람에게만 갑니다.
//
// It was the one mail this product sent to somebody who had not asked for it:
// a leader turned the schedule on and every unsubmitted member received it.
// Off until the member turns it on — and an old screen that saves an address
// without knowing the switch exists must not flip it either way.
//
// guards: updateMyMailSettings, myMailSettings, queueTeamReminders, reminderStillWanted
func TestATeamReminderReachesOnlySomebodyWhoAskedForIt(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	organization := server.createOrganization("권고 수신 선택 조직", "REMINDOPTIN")
	leader := createAutomationTestAccount(t, server, "optin_leader", "TEAM_LEADER", &organization)
	silent := createAutomationTestAccount(t, server, "optin_silent", "USER", &organization)
	asked := createAutomationTestAccount(t, server, "optin_asked", "USER", &organization)
	for _, account := range []automationTestAccount{silent, asked} {
		if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET email=$2 WHERE id=$1`,
			account.id, account.username+"@internal.test"); err != nil {
			t.Fatal(err)
		}
	}

	view := decodeData(t, server.request(http.MethodGet, "/api/v1/me/mail", nil, silent.cookie))
	if view["teamReminder"] != false {
		t.Fatalf("a person who never chose has teamReminder=%v; the default is off", view["teamReminder"])
	}
	if view["reminderAddress"] != silent.username+"@internal.test" {
		t.Errorf("reminderAddress=%v, want the account address when none is saved", view["reminderAddress"])
	}

	if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
		"address": "", "onSubmit": false, "scheduleReminder": false, "teamReminder": true,
	}, asked.cookie); w.Code != http.StatusOK {
		t.Fatalf("turn reminders on without a saved address: %d %s", w.Code, w.Body.String())
	}
	// A screen from before the switch sends no such key. It keeps the answer.
	if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
		"address": "", "onSubmit": false, "scheduleReminder": false,
	}, asked.cookie); w.Code != http.StatusOK {
		t.Fatalf("save from an old screen: %d %s", w.Code, w.Body.String())
	}
	if got := decodeData(t, server.request(http.MethodGet, "/api/v1/me/mail", nil, asked.cookie))["teamReminder"]; got != true {
		t.Fatalf("saving without the key turned the reminder %v", got)
	}

	week := currentWeekStart(time.Now().In(server.app.serviceLocation(server.ctx())),
		server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	result, err := server.app.queueTeamReminders(server.ctx(), leader.id, week, teamReminderOriginManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible != 2 || result.Queued != 1 || result.SkippedNotOptedIn != 1 {
		t.Fatalf("queue counts %+v, want one queued and one who did not ask", result)
	}
	server.app.wakeMailWorker()
	if messages := relay.awaitRelay(t, 1); len(messages) != 1 {
		t.Fatalf("relay received %d reminders, want the one that was asked for", len(messages))
	}

	// A row queued for somebody who has not asked — by an older build, or
	// before they turned it off — is discarded at the moment of sending.
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO team_reminder_deliveries
		(requested_by,recipient_user_id,week_start,address,origin) VALUES($1,$2,$3,$4,'MANUAL')`,
		leader.id, silent.id, week, silent.username+"@internal.test"); err != nil {
		t.Fatal(err)
	}
	// The worker woken by the first send may claim it first; either way the
	// row has to go and nothing may be sent.
	server.app.sendNextQueuedReminder(server.ctx())
	var leftover int
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries
			WHERE recipient_user_id=$1`, silent.id).Scan(&leftover); err != nil {
			t.Fatal(err)
		}
		if leftover == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if leftover != 0 {
		t.Errorf("a reminder for somebody who never asked is still queued")
	}
	if messages := relay.awaitRelay(t, 1); len(messages) != 1 {
		t.Errorf("relay received %d reminders, want only the one that was asked for", len(messages))
	}

	// Turning it off takes back what is waiting, not only what comes later.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE team_reminder_deliveries
		SET status='QUEUED', sent_at=NULL, next_attempt_at=now()+interval '1 hour' WHERE recipient_user_id=$1`, asked.id); err != nil {
		t.Fatal(err)
	}
	if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
		"address": "", "onSubmit": false, "scheduleReminder": false, "teamReminder": false,
	}, asked.cookie); w.Code != http.StatusOK {
		t.Fatalf("turn reminders off: %d %s", w.Code, w.Body.String())
	}
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries
		WHERE recipient_user_id=$1 AND status='QUEUED'`, asked.id).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Errorf("turning reminders off left %d queued reminder(s)", leftover)
	}
}

// The administrator's schedule covers every member of the service, and the
// first schedule to reach a member used to take that week's reminder. A team
// whose leader chose Friday was reminded on the administrator's Thursday. A
// schedule now leaves a member to the leader nearest them who has one.
//
// guards: queueTeamReminders, nearerReminderLeaderClause, queueDueTeamReminders
func TestAnAutomaticReminderIsLeftToTheNearestLeaderWithASchedule(t *testing.T) {
	server := newTestServer(t)
	division := server.createOrganization("권고 우선 본부", "NEARDIV")
	team := server.createChildOrganization("권고 우선 팀", "NEARTEAM", &division)
	lone := server.createOrganization("권고 우선 단독 조직", "NEARLONE")

	admin := createAutomationTestAccount(t, server, "near_admin", "ADMIN", nil)
	manager := createAutomationTestAccount(t, server, "near_manager", "ORG_MANAGER", &division)
	leader := createAutomationTestAccount(t, server, "near_leader", "TEAM_LEADER", &team)
	teamMember := createAutomationTestAccount(t, server, "near_team_member", "USER", &team)
	divisionMember := createAutomationTestAccount(t, server, "near_division_member", "USER", &division)
	loneMember := createAutomationTestAccount(t, server, "near_lone_member", "USER", &lone)
	for _, account := range []automationTestAccount{teamMember, divisionMember, loneMember} {
		setAutomationTestEmail(t, server, account, account.username+"@internal.test")
	}
	for account, weekday := range map[automationTestAccount]string{admin: "MONDAY", manager: "TUESDAY", leader: "FRIDAY"} {
		if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_weekly_preferences
			(user_id,auto_clone_previous,team_reminder_enabled,team_reminder_weekday)
			VALUES($1,false,true,$2)`, account.id, weekday); err != nil {
			t.Fatal(err)
		}
	}
	owners := func(week time.Time) map[int64]int64 {
		t.Helper()
		rows, err := server.app.db.Query(server.ctx(), `SELECT recipient_user_id,requested_by
			FROM team_reminder_deliveries WHERE week_start=$1`, week)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		found := map[int64]int64{}
		for rows.Next() {
			var recipient, requester int64
			if err := rows.Scan(&recipient, &requester); err != nil {
				t.Fatal(err)
			}
			found[recipient] = requester
		}
		return found
	}

	location := server.app.serviceLocation(server.ctx())
	week := time.Date(2026, 8, 31, 0, 0, 0, 0, location)
	// Monday: only the administrator is due, and takes only the member nobody
	// nearer has a schedule for.
	if err := server.app.queueDueTeamReminders(server.ctx(), reminderDueAt(week, "MONDAY"), week); err != nil {
		t.Fatal(err)
	}
	if got, want := owners(week), map[int64]int64{loneMember.id: admin.id}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after the administrator's day, reminders=%v, want %v", got, want)
	}
	// Friday: everybody is due. The division manager does not take the team
	// member from the team's own leader.
	if err := server.app.queueDueTeamReminders(server.ctx(), reminderDueAt(week, "FRIDAY"), week); err != nil {
		t.Fatal(err)
	}
	want := map[int64]int64{loneMember.id: admin.id, divisionMember.id: manager.id, teamMember.id: leader.id}
	if got := owners(week); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after every schedule ran, reminders=%v, want %v", got, want)
	}

	// A leader who turned the schedule off no longer holds their team back:
	// the team falls to the next leader up who has one.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE user_weekly_preferences
		SET team_reminder_enabled=false WHERE user_id=$1`, leader.id); err != nil {
		t.Fatal(err)
	}
	next := week.AddDate(0, 0, 7)
	if err := server.app.queueDueTeamReminders(server.ctx(), reminderDueAt(next, "FRIDAY"), next); err != nil {
		t.Fatal(err)
	}
	want = map[int64]int64{loneMember.id: admin.id, divisionMember.id: manager.id, teamMember.id: manager.id}
	if got := owners(next); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("with the team's schedule off, reminders=%v, want %v", got, want)
	}

	// A button press means the people it was pressed for.
	manual := week.AddDate(0, 0, 14)
	result, err := server.app.queueTeamReminders(server.ctx(), manager.id, manual, teamReminderOriginManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Queued != 2 {
		t.Errorf("the manager's own click queued %d, want the division and its team", result.Queued)
	}
}

// 임원실 receives no mail from this service: not a reminder, not a deadline
// digest, not a copy of their own report, not a test. Nor does anything
// beneath it. The organisation carries the flag, a new 임원실 starts with it,
// and the worker asks again before every send.
//
// guards: defaultMailBlocked, createOrganization, updateOrganization, adminOrganizations, queueReportMail, sendNextQueuedMail, queueTeamReminders, queueDueScheduleReminders, sendNextQueuedScheduleReminder, refuseBlockedMailRecipient, mailBlockedOrganization
func TestTheExecutiveOfficeReceivesNoMail(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)

	office := server.createOrganization(" 임원실 ", "EXECOFFICE")
	secretariat := server.createChildOrganization("비서실", "EXECSECRETARIAT", &office)
	plain := server.createOrganization("일반 조직", "EXECPLAIN")
	var listed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(server.request(http.MethodGet, "/api/v1/admin/organizations", nil, server.admin).Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	organizations := map[int64]map[string]any{}
	for _, item := range listed.Data {
		organizations[int64(item["id"].(float64))] = item
	}
	if organizations[office]["mailBlocked"] != true {
		t.Errorf("a new 임원실 is not excluded from mail: %v", organizations[office])
	}
	if organizations[secretariat]["mailBlocked"] != false || organizations[secretariat]["mailBlockedByParent"] != true {
		t.Errorf("an organisation under 임원실 does not show the exclusion it inherits: %v", organizations[secretariat])
	}

	leader := createAutomationTestAccount(t, server, "exec_admin", "ADMIN", &plain)
	executive := server.createUser("exec_member", "USER", &secretariat)
	executiveID := server.meID(executive)
	if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
		"address": "executive@internal.test", "onSubmit": true, "scheduleReminder": true, "teamReminder": true,
	}, executive); w.Code != http.StatusOK {
		t.Fatalf("save the executive's mail settings: %d %s", w.Code, w.Body.String())
	}
	view := decodeData(t, server.request(http.MethodGet, "/api/v1/me/mail", nil, executive))
	if view["mailBlockedOrganization"] != "임원실" {
		t.Errorf("the settings screen does not say why nothing will arrive: %v", view["mailBlockedOrganization"])
	}

	// The test button is mail too.
	if w := server.request(http.MethodPost, "/api/v1/me/mail/test", nil, executive); w.Code != http.StatusConflict || errorCode(w) != "MAIL_BLOCKED_ORGANIZATION" {
		t.Errorf("a test mail for an executive: %d %s", w.Code, w.Body.String())
	}

	week := currentWeekStart(time.Now().In(server.app.serviceLocation(server.ctx())),
		server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	result, err := server.app.queueTeamReminders(server.ctx(), leader.id, week, teamReminderOriginManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Queued != 0 || result.SkippedExcluded != 1 {
		t.Errorf("a reminder to the executive office: %+v", result)
	}

	// Their own report, filed, is not mailed back to them.
	server.submitted(executive, week.Format("2006-01-02"), "임원 보고")
	var queued int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM report_mail_deliveries WHERE user_id=$1`, executiveID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Errorf("a submitted report queued %d mail(s) to the executive office", queued)
	}

	// A row that was waiting when the person moved in — or the organisation was
	// marked — is refused by the worker, with the reason on the writer's card.
	var reportID int64
	if err := server.app.db.QueryRow(server.ctx(), `SELECT id FROM weekly_reports WHERE user_id=$1`, executiveID).Scan(&reportID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO report_mail_deliveries(report_id,user_id,address)
		VALUES($1,$2,'executive@internal.test')`, reportID, executiveID); err != nil {
		t.Fatal(err)
	}
	server.app.sendNextQueuedMail(server.ctx())
	var status, reason string
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := server.app.db.QueryRow(server.ctx(), `SELECT status,error_message FROM report_mail_deliveries
			WHERE user_id=$1`, executiveID).Scan(&status, &reason); err != nil {
			t.Fatal(err)
		}
		if status != "QUEUED" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status != "FAILED" || reason != mailBlockedReason {
		t.Errorf("a waiting report mail to the executive office ended %s %q", status, reason)
	}

	// The deadline digest: queued days ago, refused now, though there is
	// something due to tell them about.
	server.addScheduleTask(executive, map[string]any{"title": "임원 일정",
		"startDate": dayFromReminder(0), "endDate": dayFromReminder(1)})
	if err := server.app.queueDueScheduleReminders(server.ctx(), reminderDay.Add(9*time.Hour+30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM schedule_reminder_deliveries WHERE user_id=$1`, executiveID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Errorf("a deadline digest was queued for the executive office")
	}
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO schedule_reminder_deliveries(user_id,reminder_on,address)
		VALUES($1,$2,'executive@internal.test')`, executiveID, dayFromReminder(0)); err != nil {
		t.Fatal(err)
	}
	server.app.sendNextQueuedScheduleReminder(server.ctx())
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM schedule_reminder_deliveries WHERE user_id=$1`, executiveID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Errorf("a waiting deadline digest to the executive office was kept")
	}
	if messages := relay.awaitRelay(t, 0); len(messages) != 0 {
		for _, message := range messages {
			t.Errorf("the executive office received a mail:\n%s", message)
		}
	}

	// Renaming an organisation to 임원실 makes it the 임원실; the flag can still
	// be cleared on purpose.
	if w := server.request(http.MethodPatch, fmt.Sprintf("/api/v1/admin/organizations/%d", plain),
		map[string]any{"name": "임 원 실"}, server.admin); w.Code != http.StatusOK || decodeData(t, w)["mailBlocked"] != true {
		t.Errorf("renaming to 임원실 did not exclude it: %d %s", w.Code, w.Body.String())
	}
	if w := server.request(http.MethodPatch, fmt.Sprintf("/api/v1/admin/organizations/%d", plain),
		map[string]any{"mailBlocked": false}, server.admin); w.Code != http.StatusOK || decodeData(t, w)["mailBlocked"] != false {
		t.Errorf("the exclusion could not be cleared: %d %s", w.Code, w.Body.String())
	}
	if defaultMailBlocked("임원실장") || defaultMailBlocked("") {
		t.Error("only an organisation named 임원실 starts excluded")
	}
}

// An installation that already has an 임원실 gets it excluded by the upgrade
// itself, before anybody has opened the organisation screen.
//
// guards: migrate
func TestTheUpgradeExcludesAnExistingExecutiveOffice(t *testing.T) {
	dsn := os.Getenv("WEEKLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEEKLY_TEST_POSTGRES_DSN is not configured")
	}
	target, drop := emptyScratchDatabase(t, dsn)
	defer drop()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	versions := migrationVersions(t)
	before := 0
	for index, version := range versions {
		if version == 32 {
			before = index
		}
	}
	if before == 0 {
		t.Fatal("migration 032 is not in the embedded set")
	}
	applyMigrationsUpTo(t, pool, before)
	if _, err := pool.Exec(ctx, `INSERT INTO organizations(name,code) VALUES
		('임원실','UPG-EXEC'),('영업팀','UPG-SALES')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT code, mail_blocked FROM organizations WHERE code LIKE 'UPG-%' ORDER BY code`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var code string
		var blocked bool
		if err := rows.Scan(&code, &blocked); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%v", code, blocked))
	}
	if strings.Join(got, ",") != "UPG-EXEC=true,UPG-SALES=false" {
		t.Errorf("after the upgrade: %v", got)
	}
}

// 메일은 보수적으로 나갑니다: 사람이 받겠다고 켠 것만, 처음에는 아무것도.
//
// A new account with a perfectly good address, in a team whose leader has the
// weekly reminder on, who writes and submits a report and has a deadline on
// the board tomorrow, receives nothing — not the reminder, not the digest, not
// a copy of the report. Saving an address is not asking for mail either. Every
// kind this product sends is checked here, so a new kind that starts out on
// has a place to fail.
//
// guards: myMailSettings, updateMyMailSettings, queueTeamReminders, queueDueTeamReminders, queueDueScheduleReminders, queueReportMail
func TestANewAccountReceivesNoMailUntilItAsks(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	organization := server.createOrganization("보수적 발송 조직", "QUIETMAIL")
	leader := createAutomationTestAccount(t, server, "quiet_leader", "TEAM_LEADER", &organization)
	member := createAutomationTestAccount(t, server, "quiet_member", "USER", &organization)
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET email=$2 WHERE id=$1`,
		member.id, "quiet-member@internal.test"); err != nil {
		t.Fatal(err)
	}
	if w := saveAutomationPreference(server, leader, false, true, "MONDAY"); w.Code != http.StatusOK {
		t.Fatalf("the leader turns the weekly reminder on: %d %s", w.Code, w.Body.String())
	}

	expectNothingOn := func(when string) {
		t.Helper()
		view := decodeData(t, server.request(http.MethodGet, "/api/v1/me/mail", nil, member.cookie))
		for _, key := range []string{"onSubmit", "scheduleReminder", "teamReminder"} {
			if view[key] != false {
				t.Errorf("%s: %s=%v; nothing is sent until the person turns it on", when, key, view[key])
			}
		}
	}
	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	remind := func(when string) {
		t.Helper()
		if err := server.app.queueDueTeamReminders(server.ctx(), reminderDueAt(week, "MONDAY").Add(time.Hour), week); err != nil {
			t.Fatal(err)
		}
		if result, err := server.app.queueTeamReminders(server.ctx(), leader.id, week, teamReminderOriginManual, ""); err != nil {
			t.Fatal(err)
		} else if result.Queued != 0 || result.SkippedNotOptedIn < 1 {
			t.Errorf("%s: the leader's reminder reached somebody who never asked: %+v", when, result)
		}
	}

	// No settings row at all: the account address is the only one there is,
	// and it is not consent.
	expectNothingOn("a new account")
	remind("a new account")
	// An address saved on its own is somewhere to send, not a request to send.
	if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
		"address": "quiet-member@internal.test", "onSubmit": false, "scheduleReminder": false,
	}, member.cookie); w.Code != http.StatusOK {
		t.Fatalf("save an address: %d %s", w.Code, w.Body.String())
	}
	expectNothingOn("after saving only an address")
	remind("after saving only an address")

	server.addScheduleTask(member.cookie, map[string]any{"title": "내일 마감",
		"startDate": dayFromReminder(0), "endDate": dayFromReminder(1)})
	if err := server.app.queueDueScheduleReminders(server.ctx(), reminderDay.Add(9*time.Hour+30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	server.submitted(member.cookie, week.Format("2006-01-02"), "조용히 제출")

	var queued int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT
		(SELECT count(*) FROM team_reminder_deliveries WHERE recipient_user_id=$1) +
		(SELECT count(*) FROM schedule_reminder_deliveries WHERE user_id=$1) +
		(SELECT count(*) FROM report_mail_deliveries WHERE user_id=$1)`, member.id).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Errorf("%d mail(s) were queued for somebody who asked for none", queued)
	}
	server.app.wakeMailWorker()
	if messages := relay.awaitRelay(t, 0); len(messages) != 0 {
		t.Errorf("the relay received %d message(s) for somebody who asked for none", len(messages))
	}
}
