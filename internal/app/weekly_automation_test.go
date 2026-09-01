package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type automationTestAccount struct {
	cookie   *http.Cookie
	username string
	id       int64
}

func createAutomationTestAccount(t *testing.T, server *testServer, stem, role string, organizationID *int64) automationTestAccount {
	t.Helper()
	cookie := server.createUser(stem, role, organizationID)
	username := server.lastCreatedUsername(stem)
	return automationTestAccount{cookie: cookie, username: username, id: server.userIDOf(username)}
}

func saveAutomationPreference(server *testServer, account automationTestAccount, autoClone, reminder bool, weekday string) *httptest.ResponseRecorder {
	return server.request(http.MethodPut, "/api/v1/me/preferences", map[string]any{
		"autoClonePrevious": autoClone,
		"reminderEnabled":   reminder,
		"reminderWeekday":   weekday,
	}, account.cookie)
}

func setAutomationTestEmail(t *testing.T, server *testServer, account automationTestAccount, address string) {
	t.Helper()
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET email=$2 WHERE id=$1`, account.id, address); err != nil {
		t.Fatalf("set %s email: %v", account.username, err)
	}
}

func TestWeeklyAutomationRoleAndScheduleRules(t *testing.T) {
	for role, want := range map[string]bool{
		"USER": false, "TEAM_LEADER": true, "ORG_MANAGER": true, "ADMIN": true,
	} {
		if got := canScheduleTeamReminder(role); got != want {
			t.Errorf("canScheduleTeamReminder(%q)=%v, want %v", role, got, want)
		}
	}
	for _, weekday := range []string{"SUNDAY", "MONDAY", "TUESDAY", "WEDNESDAY", "THURSDAY", "FRIDAY", "SATURDAY"} {
		if !validWeeklyWeekday("  " + strings.ToLower(weekday) + "  ") {
			t.Errorf("%s was not accepted as a weekday", weekday)
		}
	}
	if validWeeklyWeekday("FUNDAY") {
		t.Error("an unknown weekday was accepted")
	}

	location, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	// The reporting week begins on Wednesday. Monday therefore means the Monday
	// near the end of that reporting week, not the one two days before it.
	week := time.Date(2026, 9, 2, 0, 0, 0, 0, location)
	due := reminderDueAt(week, "MONDAY")
	if got, want := due.Format(time.RFC3339), "2026-09-07T09:00:00+09:00"; got != want {
		t.Errorf("Monday reminder is due at %s, want %s", got, want)
	}
}

// The scheduler reads a due-leader snapshot before it opens the queue
// transaction. A preference update may commit between those two operations;
// the old weekday must not recreate an AUTO row that the update just removed.
//
// guards: queueTeamReminderForLeader, queueTeamReminders, updateMyWeeklyPreferences
func TestAutomaticTeamReminderRejectsAStaleScheduledWeekday(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("자동 권고 요일 재검증 조직", "AUTOWEEKDAY")
	leader := createAutomationTestAccount(t, server, "auto_weekday_leader", "TEAM_LEADER", &organization)
	recipient := createAutomationTestAccount(t, server, "auto_weekday_recipient", "USER", &organization)
	setAutomationTestEmail(t, server, recipient, "auto-weekday@internal.test")
	oldPreference := saveAutomationPreference(server, leader, false, true, "THURSDAY")
	if oldPreference.Code != http.StatusOK {
		t.Fatalf("save old automatic weekday: %d %s", oldPreference.Code, oldPreference.Body.String())
	}

	location := server.app.serviceLocation(server.ctx())
	// Use next week so the real background worker awakened by the preference
	// handler cannot independently create the row under test, regardless of the
	// wall-clock weekday on which this suite runs.
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY")).AddDate(0, 0, 7)
	staleSnapshot := reminderLeader{ID: leader.id, Role: "TEAM_LEADER", OrganizationID: &organization, Weekday: "THURSDAY"}
	newPreference := saveAutomationPreference(server, leader, false, true, "FRIDAY")
	if newPreference.Code != http.StatusOK {
		t.Fatalf("change automatic weekday: %d %s", newPreference.Code, newPreference.Body.String())
	}
	if err := server.app.queueTeamReminderForLeader(server.ctx(), staleSnapshot, week); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries
		WHERE requested_by=$1 AND week_start=$2`, leader.id, week).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("stale weekday left %d AUTO delivery rows", rows)
	}

	staleSnapshot.Weekday = "FRIDAY"
	if err := server.app.queueTeamReminderForLeader(server.ctx(), staleSnapshot, week); err != nil {
		t.Fatal(err)
	}
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries
		WHERE requested_by=$1 AND week_start=$2 AND recipient_user_id=$3 AND origin='AUTO'`, leader.id, week, recipient.id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("current weekday left %d AUTO delivery rows, want 1", rows)
	}
}

func TestWeeklyPreferencesKeepReminderAuthorityAndStoredChoiceApart(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("자동화 설정 조직", "AUTOPREF")
	member := createAutomationTestAccount(t, server, "automation_member", "USER", &organization)
	leader := createAutomationTestAccount(t, server, "automation_leader", "TEAM_LEADER", &organization)

	defaults := decodeData(t, server.request(http.MethodGet, "/api/v1/me/preferences", nil, member.cookie))
	if defaults["autoClonePrevious"] != false || defaults["reminderAvailable"] != false || defaults["reminderEnabled"] != false {
		t.Fatalf("ordinary-user defaults are not safe: %#v", defaults)
	}

	refused := saveAutomationPreference(server, member, true, true, "FRIDAY")
	if refused.Code != http.StatusForbidden || errorCode(refused) != "REMINDER_ROLE_REQUIRED" {
		t.Fatalf("ordinary user enabled team reminders: %d %s", refused.Code, refused.Body.String())
	}
	var memberRows int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM user_weekly_preferences WHERE user_id=$1`, member.id).Scan(&memberRows); err != nil {
		t.Fatal(err)
	}
	if memberRows != 0 {
		t.Errorf("a refused preference write left %d row(s)", memberRows)
	}

	// Reminder fields are unavailable to this role, so an omitted/empty weekday
	// must not prevent the independent automatic-clone choice from being saved.
	savedMember := saveAutomationPreference(server, member, true, false, "")
	if savedMember.Code != http.StatusOK {
		t.Fatalf("save ordinary user's clone preference: %d %s", savedMember.Code, savedMember.Body.String())
	}
	var memberClone, memberReminder bool
	var memberWeekday string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT auto_clone_previous,team_reminder_enabled,team_reminder_weekday
		FROM user_weekly_preferences WHERE user_id=$1`, member.id).Scan(&memberClone, &memberReminder, &memberWeekday); err != nil {
		t.Fatal(err)
	}
	if !memberClone || memberReminder || memberWeekday != "FRIDAY" {
		t.Errorf("ordinary-user preference stored clone=%v reminder=%v weekday=%s", memberClone, memberReminder, memberWeekday)
	}

	savedLeader := saveAutomationPreference(server, leader, false, true, "WEDNESDAY")
	if savedLeader.Code != http.StatusOK {
		t.Fatalf("save leader preference: %d %s", savedLeader.Code, savedLeader.Body.String())
	}
	leaderView := decodeData(t, savedLeader)
	if leaderView["reminderAvailable"] != true || leaderView["reminderEnabled"] != true || leaderView["reminderWeekday"] != "WEDNESDAY" {
		t.Fatalf("leader preference did not round-trip: %#v", leaderView)
	}

	invalid := saveAutomationPreference(server, leader, false, true, "FUNDAY")
	if invalid.Code != http.StatusBadRequest || errorCode(invalid) != "INVALID_WEEKDAY" {
		t.Fatalf("invalid weekday was not refused: %d %s", invalid.Code, invalid.Body.String())
	}

	// A demotion masks the old privileged choice. Saving the still-authorised clone
	// option must not write that masked false value over the stored reminder.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET role='USER' WHERE id=$1`, leader.id); err != nil {
		t.Fatal(err)
	}
	masked := decodeData(t, server.request(http.MethodGet, "/api/v1/me/preferences", nil, leader.cookie))
	if masked["reminderAvailable"] != false || masked["reminderEnabled"] != false {
		t.Fatalf("a demoted leader still presents the reminder as active: %#v", masked)
	}
	changedClone := saveAutomationPreference(server, leader, true, false, "")
	if changedClone.Code != http.StatusOK {
		t.Fatalf("demoted leader could not save clone preference: %d %s", changedClone.Code, changedClone.Body.String())
	}
	var clone, reminder bool
	var weekday string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT auto_clone_previous,team_reminder_enabled,team_reminder_weekday
		FROM user_weekly_preferences WHERE user_id=$1`, leader.id).Scan(&clone, &reminder, &weekday); err != nil {
		t.Fatal(err)
	}
	if !clone || !reminder || weekday != "WEDNESDAY" {
		t.Errorf("saving clone after demotion stored clone=%v reminder=%v weekday=%s", clone, reminder, weekday)
	}
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET role='TEAM_LEADER' WHERE id=$1`, leader.id); err != nil {
		t.Fatal(err)
	}
	restored := decodeData(t, server.request(http.MethodGet, "/api/v1/me/preferences", nil, leader.cookie))
	if restored["reminderEnabled"] != true || restored["reminderWeekday"] != "WEDNESDAY" {
		t.Errorf("restoring the role did not restore the choice: %#v", restored)
	}
}

func TestWeeklyAutomaticCloneCarriesAllWritingAndRunsOnce(t *testing.T) {
	server := newTestServer(t)
	author := createAutomationTestAccount(t, server, "automation_clone", "USER", nil)
	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	previous := week.AddDate(0, 0, -7)

	sourceID, version := server.draft(author.cookie, previous.Format("2006-01-02"), "지난주 전체 요약")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", sourceID), map[string]any{
		"summary": "지난주 전체 요약",
		"version": version,
		"items": []map[string]any{
			{"category": "개발", "title": "자동 복제", "currentResult": "구현 완료", "nextPlan": "배포", "issue": "검토 대기", "managementAsk": "검토자 지정", "progress": 80},
			{"category": "운영", "title": "릴레이 점검", "currentResult": "연결 확인", "nextPlan": "장애 훈련", "issue": "", "managementAsk": "일정 확정", "progress": 45},
		},
	}, author.cookie)
	if filled.Code != http.StatusOK {
		t.Fatalf("fill source report: %d %s", filled.Code, filled.Body.String())
	}
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_weekly_preferences
		(user_id,auto_clone_previous,team_reminder_enabled,team_reminder_weekday)
		VALUES($1,true,false,'FRIDAY')`, author.id); err != nil {
		t.Fatal(err)
	}

	if err := server.app.runAutomaticClones(server.ctx(), week); err != nil {
		t.Fatal(err)
	}
	if err := server.app.runAutomaticClones(server.ctx(), week); err != nil {
		t.Fatal(err)
	}
	var reportID int64
	var status, summary, sourceType, sourceRef string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT id,status,summary,source_type,source_ref
		FROM weekly_reports WHERE user_id=$1 AND week_start=$2`, author.id, week).
		Scan(&reportID, &status, &summary, &sourceType, &sourceRef); err != nil {
		t.Fatalf("read automatic clone: %v", err)
	}
	if status != "DRAFT" || summary != "지난주 전체 요약" || sourceType != "CLONED" || sourceRef != fmt.Sprintf("report:%d", sourceID) {
		t.Errorf("automatic clone metadata: status=%s summary=%q source=%s/%s", status, summary, sourceType, sourceRef)
	}

	type copiedItem struct {
		category, title, current, next, issue, ask string
		progress                                   int
	}
	rows, err := server.app.db.Query(server.ctx(), `SELECT category,title,current_result,next_plan,issue,management_ask,progress
		FROM report_items WHERE report_id=$1 ORDER BY sort_order,id`, reportID)
	if err != nil {
		t.Fatal(err)
	}
	items := []copiedItem{}
	for rows.Next() {
		var item copiedItem
		if err := rows.Scan(&item.category, &item.title, &item.current, &item.next, &item.issue, &item.ask, &item.progress); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	wantItems := []copiedItem{
		{"개발", "자동 복제", "구현 완료", "배포", "검토 대기", "검토자 지정", 80},
		{"운영", "릴레이 점검", "연결 확인", "장애 훈련", "", "일정 확정", 45},
	}
	if fmt.Sprint(items) != fmt.Sprint(wantItems) {
		t.Errorf("automatic FULL clone items=%+v, want %+v", items, wantItems)
	}
	var linkedItems int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM report_items copied
		JOIN report_items source ON source.report_id=$1 AND source.title=copied.title
		WHERE copied.report_id=$2 AND copied.work_item_id=source.work_item_id
		  AND copied.work_item_id IS NOT NULL`, sourceID, reportID).Scan(&linkedItems); err != nil {
		t.Fatal(err)
	}
	if linkedItems != len(wantItems) {
		t.Errorf("automatic FULL clone kept %d/%d work-item links", linkedItems, len(wantItems))
	}
	if comment := cloneComment(t, server, week.Format("2006-01-02")); !strings.Contains(comment, "자동 전체 내용 복제") {
		t.Errorf("automatic clone history does not explain its origin: %q", comment)
	}

	var copies int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM weekly_reports WHERE user_id=$1 AND week_start=$2`, author.id, week).Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Errorf("two scheduler passes made %d current-week reports", copies)
	}
	// Deleting an automatically generated draft is an explicit choice. The weekly
	// processed marker prevents the minute worker from immediately recreating it.
	if _, err := server.app.db.Exec(server.ctx(), `DELETE FROM weekly_reports WHERE id=$1`, reportID); err != nil {
		t.Fatal(err)
	}
	if err := server.app.runAutomaticClones(server.ctx(), week); err != nil {
		t.Fatal(err)
	}
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM weekly_reports WHERE user_id=$1 AND week_start=$2`, author.id, week).Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 0 {
		t.Errorf("a deleted automatic draft was recreated %d time(s)", copies)
	}
	var processed time.Time
	if err := server.app.db.QueryRow(server.ctx(), `SELECT auto_clone_processed_week FROM user_weekly_preferences WHERE user_id=$1`, author.id).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed.Format("2006-01-02") != week.Format("2006-01-02") {
		t.Errorf("processed week=%s, want %s", processed.Format("2006-01-02"), week.Format("2006-01-02"))
	}
}

func TestWeeklyTeamReminderTargetsOnlyUnsubmittedActiveTeamMembersOnce(t *testing.T) {
	server := newTestServer(t)
	root := server.createOrganization("권고 본부", "REMINDROOT")
	child := server.createChildOrganization("권고 하위팀", "REMINDCHILD", &root)
	other := server.createOrganization("권고 외부팀", "REMINDOTHER")

	leader := createAutomationTestAccount(t, server, "reminder_leader", "TEAM_LEADER", &root)
	manager := createAutomationTestAccount(t, server, "reminder_manager", "ORG_MANAGER", &root)
	sameTeam := createAutomationTestAccount(t, server, "reminder_same", "USER", &root)
	childTeam := createAutomationTestAccount(t, server, "reminder_child", "USER", &child)
	draftMember := createAutomationTestAccount(t, server, "reminder_draft", "USER", &root)
	submittedMember := createAutomationTestAccount(t, server, "reminder_submitted", "USER", &root)
	outside := createAutomationTestAccount(t, server, "reminder_outside", "USER", &other)
	inactive := createAutomationTestAccount(t, server, "reminder_inactive", "USER", &root)
	noAddress := createAutomationTestAccount(t, server, "reminder_no_address", "USER", &root)

	setAutomationTestEmail(t, server, sameTeam, "account-same@internal.test")
	setAutomationTestEmail(t, server, childTeam, "child@internal.test")
	setAutomationTestEmail(t, server, draftMember, "draft@internal.test")
	setAutomationTestEmail(t, server, submittedMember, "submitted@internal.test")
	setAutomationTestEmail(t, server, outside, "outside@internal.test")
	setAutomationTestEmail(t, server, inactive, "inactive@internal.test")
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_mail_settings(user_id,address,on_submit)
		VALUES($1,'preferred-same@internal.test',false)`, sameTeam.id); err != nil {
		t.Fatal(err)
	}
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET active=false WHERE id=$1`, inactive.id); err != nil {
		t.Fatal(err)
	}
	_ = noAddress

	location := server.app.serviceLocation(server.ctx())
	week := time.Date(2026, 8, 31, 0, 0, 0, 0, location)
	server.draft(draftMember.cookie, week.Format("2006-01-02"), "아직 작성 중")
	server.submitted(submittedMember.cookie, week.Format("2006-01-02"), "이미 제출")
	for _, requester := range []automationTestAccount{leader, manager} {
		if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_weekly_preferences
			(user_id,auto_clone_previous,team_reminder_enabled,team_reminder_weekday)
			VALUES($1,false,true,'FRIDAY')`, requester.id); err != nil {
			t.Fatal(err)
		}
	}

	due := reminderDueAt(week, "FRIDAY")
	if err := server.app.queueDueTeamReminders(server.ctx(), due.Add(-time.Minute), week); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries WHERE week_start=$1`, week).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Errorf("%d reminder(s) were queued before the chosen day and hour", total)
	}

	if err := server.app.queueDueTeamReminders(server.ctx(), due, week); err != nil {
		t.Fatal(err)
	}
	// Re-running the minute sweep and a second overlapping manager must still leave
	// one delivery per recipient and reporting week.
	if err := server.app.queueDueTeamReminders(server.ctx(), due.Add(time.Hour), week); err != nil {
		t.Fatal(err)
	}

	rows, err := server.app.db.Query(server.ctx(), `SELECT requested_by,recipient_user_id,address
		FROM team_reminder_deliveries WHERE week_start=$1 ORDER BY recipient_user_id`, week)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[int64]string{}
	requesters := map[int64]bool{}
	for rows.Next() {
		var requester, recipient int64
		var address string
		if err := rows.Scan(&requester, &recipient, &address); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		targets[recipient] = address
		requesters[requester] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	want := map[int64]string{
		sameTeam.id:    "preferred-same@internal.test",
		childTeam.id:   "child@internal.test",
		draftMember.id: "draft@internal.test",
	}
	if fmt.Sprint(targets) != fmt.Sprint(want) {
		t.Errorf("reminder targets=%v, want %v", targets, want)
	}
	if len(requesters) != 1 || !requesters[leader.id] {
		t.Errorf("overlapping leaders produced requester set %v; the first scoped reminder should win once", requesters)
	}
	for _, excluded := range []automationTestAccount{submittedMember, outside, inactive, noAddress, leader, manager} {
		if _, found := targets[excluded.id]; found {
			t.Errorf("excluded account %s was queued", excluded.username)
		}
	}
}

func TestWeeklyQueuedReminderIsDeliveredAndCannotBeQueuedAgainThatWeek(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	organization := server.createOrganization("권고 발송 조직", "REMINDDELIVERY")
	leader := createAutomationTestAccount(t, server, "reminder_delivery_leader", "TEAM_LEADER", &organization)
	recipient := createAutomationTestAccount(t, server, "reminder_delivery_recipient", "USER", &organization)
	setAutomationTestEmail(t, server, recipient, "reminder-delivery@internal.test")

	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_weekly_preferences
		(user_id,auto_clone_previous,team_reminder_enabled,team_reminder_weekday)
		VALUES($1,false,true,'FRIDAY')`, leader.id); err != nil {
		t.Fatal(err)
	}
	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO team_reminder_deliveries
		(requested_by,recipient_user_id,week_start,address) VALUES($1,$2,$3,$4)`,
		leader.id, recipient.id, week, "reminder-delivery@internal.test"); err != nil {
		t.Fatal(err)
	}
	if more := server.app.sendNextQueuedReminder(server.ctx()); !more {
		t.Fatal("the queued reminder was not processed")
	}
	messages := relay.awaitRelay(t, 1)
	if len(messages) != 1 {
		t.Fatalf("relay received %d reminder messages, want 1", len(messages))
	}
	body := decodedBody(t, messages[0])
	for _, fragment := range []string{week.Format("2006-01-02"), recipient.username, leader.username, "제출해 주세요", "선택한 요일에 자동으로 발송했습니다"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("reminder body does not contain %q:\n%s", fragment, body)
		}
	}
	if envelope := strings.Join(relay.envelopeLines(), " "); !strings.Contains(envelope, "reminder-delivery@internal.test") {
		t.Errorf("reminder went to the wrong SMTP recipient: %s", envelope)
	}
	var status string
	var attempts int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT status,attempts FROM team_reminder_deliveries
		WHERE recipient_user_id=$1 AND week_start=$2`, recipient.id, week).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "SENT" || attempts != 1 {
		t.Errorf("delivered reminder has status=%s attempts=%d", status, attempts)
	}
	command, err := server.app.db.Exec(server.ctx(), `INSERT INTO team_reminder_deliveries
		(requested_by,recipient_user_id,week_start,address) VALUES($1,$2,$3,$4)
		ON CONFLICT(recipient_user_id,week_start) DO NOTHING`, leader.id, recipient.id, week, "reminder-delivery@internal.test")
	if err != nil {
		t.Fatal(err)
	}
	if command.RowsAffected() != 0 {
		t.Error("a sent reminder could be queued again in the same reporting week")
	}
}

func TestWeeklyReminderClaimLeaseKeepsAnotherWorkerFromSendingTheSameRow(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("권고 claim 조직", "REMINDCLAIM")
	leader := createAutomationTestAccount(t, server, "reminder_claim_leader", "TEAM_LEADER", &organization)
	recipient := createAutomationTestAccount(t, server, "reminder_claim_recipient", "USER", &organization)
	setAutomationTestEmail(t, server, recipient, "reminder-claim@internal.test")

	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_weekly_preferences
		(user_id,auto_clone_previous,team_reminder_enabled,team_reminder_weekday)
		VALUES($1,false,true,'FRIDAY')`, leader.id); err != nil {
		t.Fatal(err)
	}
	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO team_reminder_deliveries
		(requested_by,recipient_user_id,week_start,address) VALUES($1,$2,$3,$4)`,
		leader.id, recipient.id, week, "reminder-claim@internal.test"); err != nil {
		t.Fatal(err)
	}

	mailTimeout := 5 * time.Minute
	lease := teamReminderClaimLease(mailTimeout)
	if lease <= mailTimeout {
		t.Fatalf("claim lease %s must outlive SMTP timeout %s", lease, mailTimeout)
	}
	first, err := server.app.claimNextQueuedReminder(server.ctx(), lease)
	if err != nil {
		t.Fatalf("first worker claim: %v", err)
	}
	if first.recipientID != recipient.id || first.attempts != 1 {
		t.Fatalf("first claim=%+v, want recipient=%d attempts=1", first, recipient.id)
	}

	// The first UPDATE has already committed, just as it would in another service
	// replica. Until that worker's SMTP deadline plus margin passes, a second
	// replica must see no due row instead of sending the same weekly message.
	if _, err := server.app.claimNextQueuedReminder(server.ctx(), lease); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second worker claim before lease expiry=%v, want pgx.ErrNoRows", err)
	}
	var attempts int
	var nextAttempt time.Time
	if err := server.app.db.QueryRow(server.ctx(), `SELECT attempts,next_attempt_at
		FROM team_reminder_deliveries WHERE id=$1`, first.id).Scan(&attempts, &nextAttempt); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !nextAttempt.After(time.Now().Add(mailTimeout)) {
		t.Errorf("claimed row attempts=%d next=%s; lease did not cover SMTP timeout", attempts, nextAttempt)
	}

	// A process can die after claiming. Expiring the lease makes the QUEUED row
	// recoverable and spends the next attempt instead of losing it forever.
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE team_reminder_deliveries
		SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, first.id); err != nil {
		t.Fatal(err)
	}
	recovered, err := server.app.claimNextQueuedReminder(server.ctx(), lease)
	if err != nil {
		t.Fatalf("claim after simulated worker crash: %v", err)
	}
	if recovered.id != first.id || recovered.attempts != 2 {
		t.Errorf("recovered claim=%+v, want id=%d attempts=2", recovered, first.id)
	}
}

// A manual command uses the same durable queue as the schedule, but neither a
// saved weekday nor an enabled automatic preference. This drives the public
// route through the real worker and SMTP protocol so a row that is merely
// inserted and then discarded by the pre-send check cannot pass.
//
// guards: sendMyTeamReminders, queueTeamReminders, reminderStillWanted
func TestManualTeamReminderQueuesTheCurrentScopedTeamAndSendsWithoutAnAutomaticPreference(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	root := server.createOrganization("수동 권고 본부", "MANUALREMROOT")
	child := server.createChildOrganization("수동 권고 하위팀", "MANUALREMCHILD", &root)
	other := server.createOrganization("수동 권고 외부팀", "MANUALREMOTHER")

	leader := createAutomationTestAccount(t, server, "manual_reminder_leader", "TEAM_LEADER", &root)
	sameTeam := createAutomationTestAccount(t, server, "manual_reminder_same", "USER", &root)
	childTeam := createAutomationTestAccount(t, server, "manual_reminder_child", "USER", &child)
	draftMember := createAutomationTestAccount(t, server, "manual_reminder_draft", "USER", &root)
	submittedMember := createAutomationTestAccount(t, server, "manual_reminder_submitted", "USER", &root)
	outside := createAutomationTestAccount(t, server, "manual_reminder_outside", "USER", &other)
	inactive := createAutomationTestAccount(t, server, "manual_reminder_inactive", "USER", &root)
	noAddress := createAutomationTestAccount(t, server, "manual_reminder_no_address", "USER", &root)
	invalidPreferred := createAutomationTestAccount(t, server, "manual_reminder_invalid_preferred", "USER", &root)
	ordinary := createAutomationTestAccount(t, server, "manual_reminder_ordinary", "USER", &other)

	setAutomationTestEmail(t, server, sameTeam, "manual-same@internal.test")
	setAutomationTestEmail(t, server, childTeam, "manual-child@internal.test")
	setAutomationTestEmail(t, server, draftMember, "manual-draft@internal.test")
	setAutomationTestEmail(t, server, submittedMember, "manual-submitted@internal.test")
	setAutomationTestEmail(t, server, outside, "manual-outside@internal.test")
	setAutomationTestEmail(t, server, inactive, "manual-inactive@internal.test")
	setAutomationTestEmail(t, server, invalidPreferred, "valid-account-fallback@internal.test")
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_mail_settings(user_id,address,on_submit)
		VALUES($1,'invalid preferred address',false)`, invalidPreferred.id); err != nil {
		t.Fatal(err)
	}
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET active=false WHERE id=$1`, inactive.id); err != nil {
		t.Fatal(err)
	}
	_ = noAddress

	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	server.draft(draftMember.cookie, week.Format("2006-01-02"), "수동 권고 시점 작성 중")
	server.submitted(submittedMember.cookie, week.Format("2006-01-02"), "이미 제출")

	unknown := server.request(http.MethodPost, "/api/v1/me/team-reminders", map[string]any{"unexpected": true}, leader.cookie)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown manual-reminder field: %d %s", unknown.Code, unknown.Body.String())
	}
	nullBody := server.requestRaw(http.MethodPost, "/api/v1/me/team-reminders", "null", leader.cookie)
	if nullBody.Code != http.StatusBadRequest || errorCode(nullBody) != "INVALID_REQUEST" {
		t.Fatalf("null manual-reminder body: %d %s", nullBody.Code, nullBody.Body.String())
	}
	refused := server.request(http.MethodPost, "/api/v1/me/team-reminders", map[string]any{}, ordinary.cookie)
	if refused.Code != http.StatusForbidden || errorCode(refused) != "REMINDER_ROLE_REQUIRED" {
		t.Fatalf("ordinary user sent team reminders: %d %s", refused.Code, refused.Body.String())
	}

	response := server.request(http.MethodPost, "/api/v1/me/team-reminders", map[string]any{}, leader.cookie)
	if response.Code != http.StatusAccepted {
		t.Fatalf("manual reminder: %d %s", response.Code, response.Body.String())
	}
	data := decodeData(t, response)
	if data["weekStart"] != week.Format("2006-01-02") || int(data["eligible"].(float64)) != 5 ||
		int(data["queued"].(float64)) != 3 || int(data["alreadyQueued"].(float64)) != 0 ||
		int(data["skippedNoAddress"].(float64)) != 2 {
		t.Fatalf("manual reminder counts: %#v", data)
	}
	var preferences int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM user_weekly_preferences WHERE user_id=$1`, leader.id).Scan(&preferences); err != nil {
		t.Fatal(err)
	}
	if preferences != 0 {
		t.Errorf("manual sending created or required %d automatic preference row(s)", preferences)
	}

	messages := relay.awaitRelay(t, 3)
	if len(messages) != 3 {
		t.Fatalf("relay received %d manual reminders, want 3", len(messages))
	}
	for index, message := range messages {
		body := decodedBody(t, message)
		for _, fragment := range []string{week.Format("2006-01-02"), leader.username, "개인 설정에서 직접 발송했습니다"} {
			if !strings.Contains(body, fragment) {
				t.Errorf("manual reminder %d lost %q:\n%s", index, fragment, body)
			}
		}
	}
	var manualRows int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries
		WHERE requested_by=$1 AND week_start=$2 AND origin='MANUAL'`, leader.id, week).Scan(&manualRows); err != nil {
		t.Fatal(err)
	}
	if manualRows != 3 {
		t.Errorf("manual queue stored %d rows, want 3", manualRows)
	}

	again := decodeData(t, server.request(http.MethodPost, "/api/v1/me/team-reminders", map[string]any{}, leader.cookie))
	if int(again["eligible"].(float64)) != 5 || int(again["queued"].(float64)) != 0 ||
		int(again["alreadyQueued"].(float64)) != 3 || int(again["skippedNoAddress"].(float64)) != 2 {
		t.Fatalf("repeated manual reminder counts: %#v", again)
	}
	var audits int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM audit_logs
		WHERE actor_id=$1 AND action='mail.team_reminders_manual'`, leader.id).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Errorf("manual reminder clicks wrote %d audit rows, want 2", audits)
	}
	_ = outside
}

func TestManualTeamReminderRefusesAnUnreadyRelayWithoutLeavingAQueue(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("수동 권고 릴레이 조직", "MANUALRELAY")
	leader := createAutomationTestAccount(t, server, "manual_relay_leader", "TEAM_LEADER", &organization)
	recipient := createAutomationTestAccount(t, server, "manual_relay_recipient", "USER", &organization)
	setAutomationTestEmail(t, server, recipient, "manual-relay@internal.test")

	response := server.request(http.MethodPost, "/api/v1/me/team-reminders", map[string]any{}, leader.cookie)
	if response.Code != http.StatusConflict || errorCode(response) != "MAIL_RELAY_NOT_READY" {
		t.Fatalf("unready relay answered %d/%s: %s", response.Code, errorCode(response), response.Body.String())
	}
	var rows int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("unready relay left %d queued reminder(s)", rows)
	}
}

// guards: queueManualTeamReminderRecipient, queueTeamReminders
func TestManualTeamReminderRestartsFailedRowsButKeepsSentRowsDeduplicated(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("수동 권고 재시도 조직", "MANUALRETRY")
	leader := createAutomationTestAccount(t, server, "manual_retry_leader", "TEAM_LEADER", &organization)
	failed := createAutomationTestAccount(t, server, "manual_retry_failed", "USER", &organization)
	sent := createAutomationTestAccount(t, server, "manual_retry_sent", "USER", &organization)
	failedNoAddress := createAutomationTestAccount(t, server, "manual_retry_no_address", "USER", &organization)
	setAutomationTestEmail(t, server, failed, "manual-retry-new@internal.test")
	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO team_reminder_deliveries
		(requested_by,recipient_user_id,week_start,address,origin,status,attempts,error_message,next_attempt_at,sent_at)
		VALUES
		($1,$2,$5,'old-failed@internal.test','AUTO','FAILED',5,'relay refused',now()+interval '2 hours',now()),
		($1,$3,$5,'manual-sent@internal.test','AUTO','SENT',1,'',now(),now()),
		($1,$4,$5,'old-no-address@internal.test','AUTO','FAILED',4,'still failed',now()+interval '2 hours',NULL)`,
		leader.id, failed.id, sent.id, failedNoAddress.id, week); err != nil {
		t.Fatal(err)
	}

	result, err := server.app.queueTeamReminders(server.ctx(), leader.id, week, teamReminderOriginManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible != 3 || result.Queued != 1 || result.AlreadyQueued != 1 || result.SkippedNoAddress != 1 {
		t.Fatalf("failed/sent manual queue counts: %+v", result)
	}
	var requester int64
	var address, origin, status, failure string
	var attempts int
	var nextAttempt time.Time
	var sentAt *time.Time
	if err := server.app.db.QueryRow(server.ctx(), `SELECT requested_by,address,origin,status,attempts,error_message,next_attempt_at,sent_at
		FROM team_reminder_deliveries WHERE recipient_user_id=$1 AND week_start=$2`, failed.id, week).
		Scan(&requester, &address, &origin, &status, &attempts, &failure, &nextAttempt, &sentAt); err != nil {
		t.Fatal(err)
	}
	if requester != leader.id || address != "manual-retry-new@internal.test" || origin != teamReminderOriginManual ||
		status != "QUEUED" || attempts != 0 || failure != "" || sentAt != nil || nextAttempt.After(time.Now().Add(time.Minute)) {
		t.Errorf("failed reminder was not reset for a manual retry: requester=%d address=%s origin=%s status=%s attempts=%d error=%q next=%s sent=%v",
			requester, address, origin, status, attempts, failure, nextAttempt, sentAt)
	}
	var sentOrigin, sentStatus string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT origin,status FROM team_reminder_deliveries
		WHERE recipient_user_id=$1 AND week_start=$2`, sent.id, week).Scan(&sentOrigin, &sentStatus); err != nil {
		t.Fatal(err)
	}
	if sentOrigin != teamReminderOriginAuto || sentStatus != "SENT" {
		t.Errorf("sent reminder changed to %s/%s", sentOrigin, sentStatus)
	}
	var invalidOrigin, invalidStatus, invalidAddress, invalidError string
	var invalidAttempts int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT origin,status,address,attempts,error_message
		FROM team_reminder_deliveries WHERE recipient_user_id=$1 AND week_start=$2`, failedNoAddress.id, week).
		Scan(&invalidOrigin, &invalidStatus, &invalidAddress, &invalidAttempts, &invalidError); err != nil {
		t.Fatal(err)
	}
	if invalidOrigin != teamReminderOriginAuto || invalidStatus != "FAILED" || invalidAddress != "old-no-address@internal.test" ||
		invalidAttempts != 4 || invalidError != "still failed" {
		t.Errorf("failed row without a current address was changed: %s/%s address=%s attempts=%d error=%q",
			invalidOrigin, invalidStatus, invalidAddress, invalidAttempts, invalidError)
	}
}

// A manual click on an already queued automatic delivery is still a duplicate
// for the counter, but it must detach that row from the automatic setting. The
// lease and retry history belong to the in-flight worker and must not move.
//
// guards: sendMyTeamReminders, queueManualTeamReminderRecipient, updateMyWeeklyPreferences
func TestManualTeamReminderPromotesAQueuedAutomaticRowBeforeAutomationIsDisabled(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	organization := server.createOrganization("수동 권고 승격 조직", "MANUALPROMOTE")
	leader := createAutomationTestAccount(t, server, "manual_promote_leader", "TEAM_LEADER", &organization)
	recipient := createAutomationTestAccount(t, server, "manual_promote_recipient", "USER", &organization)
	invalidAddress := createAutomationTestAccount(t, server, "manual_promote_no_address", "USER", &organization)
	setAutomationTestEmail(t, server, recipient, "manual-promoted@internal.test")
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO user_weekly_preferences
		(user_id,auto_clone_previous,team_reminder_enabled,team_reminder_weekday)
		VALUES($1,false,true,'FRIDAY')`, leader.id); err != nil {
		t.Fatal(err)
	}
	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	nextAttempt := time.Now().Add(time.Hour)
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO team_reminder_deliveries
		(requested_by,recipient_user_id,week_start,address,origin,status,attempts,error_message,next_attempt_at)
		VALUES
		($1,$2,$4,'old-auto@internal.test','AUTO','QUEUED',2,'temporary relay error',$5),
		($1,$3,$4,'keep-queued@internal.test','AUTO','QUEUED',1,'',$5)`,
		leader.id, recipient.id, invalidAddress.id, week, nextAttempt); err != nil {
		t.Fatal(err)
	}

	response := server.request(http.MethodPost, "/api/v1/me/team-reminders", map[string]any{}, leader.cookie)
	if response.Code != http.StatusAccepted {
		t.Fatalf("promote automatic reminder: %d %s", response.Code, response.Body.String())
	}
	data := decodeData(t, response)
	if int(data["eligible"].(float64)) != 2 || int(data["queued"].(float64)) != 0 || int(data["alreadyQueued"].(float64)) != 2 ||
		int(data["skippedNoAddress"].(float64)) != 0 {
		t.Fatalf("promoted reminder counts: %#v", data)
	}
	var requester int64
	var address, origin, status, failure string
	var attempts int
	var storedNext time.Time
	if err := server.app.db.QueryRow(server.ctx(), `SELECT requested_by,address,origin,status,attempts,error_message,next_attempt_at
		FROM team_reminder_deliveries WHERE recipient_user_id=$1 AND week_start=$2`, recipient.id, week).
		Scan(&requester, &address, &origin, &status, &attempts, &failure, &storedNext); err != nil {
		t.Fatal(err)
	}
	if requester != leader.id || address != "manual-promoted@internal.test" || origin != teamReminderOriginManual ||
		status != "QUEUED" || attempts != 2 || failure != "temporary relay error" || storedNext.Before(time.Now().Add(50*time.Minute)) {
		t.Errorf("automatic queue promotion changed its lease/history or lost manual ownership: requester=%d address=%s origin=%s status=%s attempts=%d error=%q next=%s",
			requester, address, origin, status, attempts, failure, storedNext)
	}
	var preservedAddress, preservedOrigin string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT address,origin FROM team_reminder_deliveries
		WHERE recipient_user_id=$1 AND week_start=$2`, invalidAddress.id, week).Scan(&preservedAddress, &preservedOrigin); err != nil {
		t.Fatal(err)
	}
	if preservedAddress != "keep-queued@internal.test" || preservedOrigin != teamReminderOriginManual {
		t.Errorf("queued row with no current address was not safely promoted: address=%s origin=%s", preservedAddress, preservedOrigin)
	}

	disabled := saveAutomationPreference(server, leader, false, false, "FRIDAY")
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable automatic reminder: %d %s", disabled.Code, disabled.Body.String())
	}
	var retained int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries
		WHERE requested_by=$1 AND week_start=$2 AND origin='MANUAL' AND status='QUEUED'`, leader.id, week).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 2 {
		t.Errorf("turning automatic reminders off left %d/2 promoted manual queues", retained)
	}
}

// The uniqueness key is recipient/week, so two overlapping managers can click
// the manual action for the same person. The second click is a duplicate, not a
// transfer of the first manager's durable request. Otherwise a later demotion
// of the second manager would make the worker discard a reminder that the first
// manager was still authorised to send.
//
// guards: sendMyTeamReminders, queueManualTeamReminderRecipient, reminderStillWanted
func TestManualTeamReminderDuplicateKeepsTheFirstManualRequester(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	organization := server.createOrganization("수동 권고 소유권 조직", "MANUALOWNER")
	first := createAutomationTestAccount(t, server, "manual_owner_first", "TEAM_LEADER", &organization)
	duplicate := createAutomationTestAccount(t, server, "manual_owner_duplicate", "TEAM_LEADER", &organization)
	recipient := createAutomationTestAccount(t, server, "manual_owner_recipient", "USER", &organization)
	setAutomationTestEmail(t, server, recipient, "new-profile-address@internal.test")

	location := server.app.serviceLocation(server.ctx())
	week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
	nextAttempt := time.Now().Add(time.Hour)
	if _, err := server.app.db.Exec(server.ctx(), `INSERT INTO team_reminder_deliveries
		(requested_by,recipient_user_id,week_start,address,origin,status,attempts,error_message,next_attempt_at)
		VALUES($1,$2,$3,'first-durable-address@internal.test','MANUAL','QUEUED',2,'temporary relay error',$4)`,
		first.id, recipient.id, week, nextAttempt); err != nil {
		t.Fatal(err)
	}

	response := server.request(http.MethodPost, "/api/v1/me/team-reminders", map[string]any{}, duplicate.cookie)
	if response.Code != http.StatusAccepted {
		t.Fatalf("duplicate manual reminder: %d %s", response.Code, response.Body.String())
	}
	data := decodeData(t, response)
	if int(data["eligible"].(float64)) != 2 || int(data["queued"].(float64)) != 0 ||
		int(data["alreadyQueued"].(float64)) != 1 || int(data["skippedNoAddress"].(float64)) != 1 {
		t.Fatalf("duplicate manual reminder counts: %#v", data)
	}

	var requester int64
	var address, origin, status, failure string
	var attempts int
	var storedNext time.Time
	if err := server.app.db.QueryRow(server.ctx(), `SELECT requested_by,address,origin,status,attempts,error_message,next_attempt_at
		FROM team_reminder_deliveries WHERE recipient_user_id=$1 AND week_start=$2`, recipient.id, week).
		Scan(&requester, &address, &origin, &status, &attempts, &failure, &storedNext); err != nil {
		t.Fatal(err)
	}
	if requester != first.id || address != "first-durable-address@internal.test" || origin != teamReminderOriginManual ||
		status != "QUEUED" || attempts != 2 || failure != "temporary relay error" || storedNext.Before(time.Now().Add(50*time.Minute)) {
		t.Fatalf("duplicate click transferred or rewrote the first manual request: requester=%d address=%s origin=%s status=%s attempts=%d error=%q next=%s",
			requester, address, origin, status, attempts, failure, storedNext)
	}

	if _, err := server.app.db.Exec(server.ctx(), `UPDATE users SET role='USER' WHERE id=$1`, duplicate.id); err != nil {
		t.Fatal(err)
	}
	if _, err := server.app.db.Exec(server.ctx(), `UPDATE team_reminder_deliveries SET next_attempt_at=now()
		WHERE recipient_user_id=$1 AND week_start=$2`, recipient.id, week); err != nil {
		t.Fatal(err)
	}
	// Either this wake or the explicit worker wake from the POST may win the
	// claim; the observable contract is the one SMTP delivery, not which test
	// goroutine acquired the row.
	server.app.wakeMailWorker()
	messages := relay.awaitRelay(t, 1)
	if len(messages) != 1 {
		t.Fatalf("relay received %d messages, want 1", len(messages))
	}
	body := decodedBody(t, messages[0])
	if !strings.Contains(body, first.username) || strings.Contains(body, duplicate.username) {
		t.Errorf("manual reminder attribution changed after a duplicate click:\n%s", body)
	}
	if envelope := strings.Join(relay.envelopeLines(), " "); !strings.Contains(envelope, "first-durable-address@internal.test") {
		t.Errorf("duplicate click changed the durable delivery address: %s", envelope)
	}
}

// guards: reminderStillWanted, sendNextQueuedReminder
func TestManualQueuedReminderRevalidatesCurrentAuthorityScopeAndSubmission(t *testing.T) {
	for _, scenario := range []string{"demoted requester", "moved recipient", "submitted recipient", "inactive recipient"} {
		t.Run(scenario, func(t *testing.T) {
			server := newTestServer(t)
			relay := startFakeRelay(t)
			server.configureRelay(relay)
			organization := server.createOrganization("수동 권고 재검증 조직", "MANUALRECHECK")
			outside := server.createOrganization("수동 권고 재검증 외부", "MANUALRECHECKOUT")
			leader := createAutomationTestAccount(t, server, "manual_recheck_leader", "TEAM_LEADER", &organization)
			recipient := createAutomationTestAccount(t, server, "manual_recheck_recipient", "USER", &organization)
			setAutomationTestEmail(t, server, recipient, "manual-recheck@internal.test")
			location := server.app.serviceLocation(server.ctx())
			week := currentWeekStart(time.Now().In(location), server.app.setting(server.ctx(), "workflow.week_start", "MONDAY"))
			result, err := server.app.queueTeamReminders(server.ctx(), leader.id, week, teamReminderOriginManual, "")
			if err != nil || result.Queued != 1 {
				t.Fatalf("queue manual reminder: %+v, %v", result, err)
			}

			switch scenario {
			case "demoted requester":
				_, err = server.app.db.Exec(server.ctx(), `UPDATE users SET role='USER' WHERE id=$1`, leader.id)
			case "moved recipient":
				_, err = server.app.db.Exec(server.ctx(), `UPDATE users SET organization_id=$2 WHERE id=$1`, recipient.id, outside)
			case "submitted recipient":
				server.submitted(recipient.cookie, week.Format("2006-01-02"), "권고 뒤 제출")
			case "inactive recipient":
				_, err = server.app.db.Exec(server.ctx(), `UPDATE users SET active=false WHERE id=$1`, recipient.id)
			}
			if err != nil {
				t.Fatal(err)
			}
			if more := server.app.sendNextQueuedReminder(server.ctx()); !more {
				t.Fatal("obsolete manual reminder was not consumed")
			}
			if messages := relay.awaitRelay(t, 0); len(messages) != 0 {
				t.Errorf("obsolete manual reminder sent %d message(s)", len(messages))
			}
			var remaining int
			if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM team_reminder_deliveries`).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining != 0 {
				t.Errorf("obsolete manual reminder left %d queue row(s)", remaining)
			}
		})
	}
}
