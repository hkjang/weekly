package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// weeklyReminderHour is deliberately fixed rather than another setting to
// guess at. The chosen weekday is personal; 09:00 in the service timezone is
// the common, visible delivery time and is stated on the profile screen.
const weeklyReminderHour = 9

const (
	teamReminderOriginAuto   = "AUTO"
	teamReminderOriginManual = "MANUAL"
)

var errTeamReminderRoleRequired = errors.New("team reminder role required")

var weeklyWeekdays = map[string]time.Weekday{
	"SUNDAY": time.Sunday, "MONDAY": time.Monday, "TUESDAY": time.Tuesday,
	"WEDNESDAY": time.Wednesday, "THURSDAY": time.Thursday,
	"FRIDAY": time.Friday, "SATURDAY": time.Saturday,
}

type weeklyPreferenceView struct {
	AutoClonePrevious bool   `json:"autoClonePrevious"`
	ReminderAvailable bool   `json:"reminderAvailable"`
	ReminderEnabled   bool   `json:"reminderEnabled"`
	ReminderWeekday   string `json:"reminderWeekday"`
	ReminderHour      int    `json:"reminderHour"`
	Timezone          string `json:"timezone"`
	RelayReady        bool   `json:"relayReady"`
}

type teamReminderQueueResult struct {
	WeekStart        string `json:"weekStart"`
	Eligible         int    `json:"eligible"`
	Queued           int    `json:"queued"`
	AlreadyQueued    int    `json:"alreadyQueued"`
	SkippedNoAddress int    `json:"skippedNoAddress"`
	// SkippedNotOptedIn is the members who have not turned the reminder on in
	// 개인 설정. It is off until they do.
	SkippedNotOptedIn int `json:"skippedNotOptedIn"`
	// SkippedExcluded is the members of an organisation excluded from mail.
	SkippedExcluded int `json:"skippedExcluded"`
}

func canScheduleTeamReminder(role string) bool {
	return role == "TEAM_LEADER" || role == "ORG_MANAGER" || role == "ADMIN"
}

func validWeeklyWeekday(value string) bool {
	_, ok := weeklyWeekdays[strings.ToUpper(strings.TrimSpace(value))]
	return ok
}

func (a *App) weeklyPreferenceFor(ctx context.Context, p *principal) (weeklyPreferenceView, error) {
	view := weeklyPreferenceView{
		ReminderAvailable: canScheduleTeamReminder(p.Role),
		ReminderWeekday:   "FRIDAY",
		ReminderHour:      weeklyReminderHour,
		Timezone:          a.setting(ctx, "service.timezone", "Asia/Seoul"),
	}
	settings, err := a.loadMailSettings(ctx)
	view.RelayReady = err == nil && settings.unusable() == ""

	err = a.db.QueryRow(ctx, `SELECT auto_clone_previous,team_reminder_enabled,team_reminder_weekday
		FROM user_weekly_preferences WHERE user_id=$1`, p.ID).
		Scan(&view.AutoClonePrevious, &view.ReminderEnabled, &view.ReminderWeekday)
	if errors.Is(err, pgx.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return weeklyPreferenceView{}, err
	}
	// A role may have been lowered since this preference was saved. The stored
	// bit remains so restoring the role restores the choice, but neither the API
	// nor the worker may present it as active while the authority is absent.
	if !view.ReminderAvailable {
		view.ReminderEnabled = false
	}
	return view, nil
}

func (a *App) myWeeklyPreferences(w http.ResponseWriter, r *http.Request) {
	view, err := a.weeklyPreferenceFor(r.Context(), currentPrincipal(r.Context()))
	if err != nil {
		a.logger.Error("read weekly preferences", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "주간보고 자동화 설정을 읽을 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}

func (a *App) updateMyWeeklyPreferences(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	reminderAvailable := canScheduleTeamReminder(p.Role)
	var input struct {
		AutoClonePrevious bool   `json:"autoClonePrevious"`
		ReminderEnabled   bool   `json:"reminderEnabled"`
		ReminderWeekday   string `json:"reminderWeekday"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ReminderWeekday = strings.ToUpper(strings.TrimSpace(input.ReminderWeekday))
	if reminderAvailable && !validWeeklyWeekday(input.ReminderWeekday) {
		writeError(w, http.StatusBadRequest, "INVALID_WEEKDAY", "권고 메일을 보낼 요일이 올바르지 않습니다.")
		return
	}
	if input.ReminderEnabled && !reminderAvailable {
		writeError(w, http.StatusForbidden, "REMINDER_ROLE_REQUIRED", "팀원 권고 메일은 팀장 이상만 설정할 수 있습니다.")
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 자동화 설정을 저장할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	oldEnabled, oldWeekday := false, "FRIDAY"
	err = tx.QueryRow(r.Context(), `SELECT team_reminder_enabled,team_reminder_weekday
		FROM user_weekly_preferences WHERE user_id=$1 FOR UPDATE`, p.ID).
		Scan(&oldEnabled, &oldWeekday)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 자동화 설정을 저장할 수 없습니다.")
		return
	}
	// A user without team authority may still change automatic cloning, but the
	// reminder fields are not theirs to edit. In particular, weeklyPreferenceFor
	// deliberately masks an old enabled bit after a role is lowered. Writing that
	// masked false value back while saving an unrelated clone choice would silently
	// destroy the leader's stored preference, contrary to the promise that restoring
	// the role restores the choice.
	if !reminderAvailable {
		input.ReminderEnabled = oldEnabled
		input.ReminderWeekday = oldWeekday
	}
	_, err = tx.Exec(r.Context(), `
		INSERT INTO user_weekly_preferences AS preference
			(user_id,auto_clone_previous,team_reminder_enabled,team_reminder_weekday,updated_at)
		VALUES($1,$2,$3,$4,now())
		ON CONFLICT (user_id) DO UPDATE SET
			auto_clone_processed_week=CASE
				WHEN preference.auto_clone_previous=false AND EXCLUDED.auto_clone_previous=true THEN NULL
				ELSE preference.auto_clone_processed_week END,
			auto_clone_previous=EXCLUDED.auto_clone_previous,
			team_reminder_enabled=EXCLUDED.team_reminder_enabled,
			team_reminder_weekday=EXCLUDED.team_reminder_weekday,
			updated_at=now()`,
		p.ID, input.AutoClonePrevious, input.ReminderEnabled, input.ReminderWeekday)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 자동화 설정을 저장할 수 없습니다.")
		return
	}
	// A queued retry belongs to the choice that made it. If the leader turns
	// sending off or moves it to another day, do not deliver that old choice
	// hours later. A different eligible leader may enqueue the shared reminder.
	if oldEnabled != input.ReminderEnabled || oldWeekday != input.ReminderWeekday {
		if _, err = tx.Exec(r.Context(), `DELETE FROM team_reminder_deliveries
			WHERE requested_by=$1 AND status='QUEUED' AND origin='AUTO'`, p.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 자동화 설정을 저장할 수 없습니다.")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "주간보고 자동화 설정을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "weekly.preference", "user", strconv.FormatInt(p.ID, 10), map[string]any{
		"autoClonePrevious": input.AutoClonePrevious,
		"reminderEnabled":   input.ReminderEnabled,
		"reminderWeekday":   input.ReminderWeekday,
	})
	a.wakeWeeklyAutomation()
	view, err := a.weeklyPreferenceFor(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "저장한 자동화 설정을 다시 읽을 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}

func (a *App) wakeWeeklyAutomation() {
	select {
	case a.automationWake <- struct{}{}:
	default:
	}
}

// weeklyAutomationWorker runs once at startup to catch a service that was down
// at the scheduled time, then often enough that 09:00 does not become 09:30.
func (a *App) weeklyAutomationWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		a.runWeeklyAutomations(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-a.automationWake:
		case <-ticker.C:
		}
	}
}

func (a *App) runWeeklyAutomations(ctx context.Context, now time.Time) {
	location := a.serviceLocation(ctx)
	localNow := now.In(location)
	weekStartSetting := a.setting(ctx, "workflow.week_start", "MONDAY")
	week := currentWeekStart(localNow, weekStartSetting)
	if err := a.runAutomaticClones(ctx, week); err != nil {
		a.logger.Error("automatic weekly clone", "error", err, "week", week.Format("2006-01-02"))
	}
	// Unlike a submitted report, a recommendation has no durable business event
	// behind it. Do not build a backlog while SMTP is deliberately unavailable;
	// if it becomes ready later this reporting week, the next tick catches up.
	mailSettings, err := a.loadMailSettings(ctx)
	if err != nil || mailSettings.unusable() != "" {
		return
	}
	if err := a.queueDueTeamReminders(ctx, localNow, week); err != nil {
		a.logger.Error("queue team report reminders", "error", err, "week", week.Format("2006-01-02"))
	}
	// The board's own reminder rides the same tick. It is a daily digest rather
	// than a weekly one, and it belongs here for the same reason: this is the
	// loop that already knows what time it is where the service lives.
	if err := a.queueDueScheduleReminders(ctx, localNow); err != nil {
		a.logger.Error("queue schedule deadline reminders", "error", err)
	}
}

func (a *App) runAutomaticClones(ctx context.Context, week time.Time) error {
	rows, err := a.db.Query(ctx, `SELECT p.user_id FROM user_weekly_preferences p
		JOIN users u ON u.id=p.user_id
		WHERE p.auto_clone_previous=true AND u.active=true
		  AND p.auto_clone_processed_week IS DISTINCT FROM $1::date
		ORDER BY p.user_id`, week)
	if err != nil {
		return err
	}
	userIDs := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		userIDs = append(userIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, userID := range userIDs {
		reportID, sourceID, cloneErr := a.runAutomaticCloneForUser(ctx, userID, week)
		if cloneErr != nil {
			a.logger.Error("automatic clone for user", "error", cloneErr, "userId", userID, "week", week.Format("2006-01-02"))
			continue
		}
		if reportID != 0 {
			a.auditSystem(ctx, "report.auto_clone", "report", strconv.FormatInt(reportID, 10), map[string]any{
				"userId": userID, "sourceReportId": sourceID, "weekStart": week.Format("2006-01-02"), "mode": "FULL",
			})
		}
	}
	return nil
}

// runAutomaticCloneForUser serialises on the preference row. The preference is
// both the authority and the once-per-week claim, so two service replicas can
// safely reach this function together.
func (a *App) runAutomaticCloneForUser(ctx context.Context, userID int64, week time.Time) (int64, int64, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	var enabled bool
	var processed *time.Time
	if err = tx.QueryRow(ctx, `SELECT auto_clone_previous,auto_clone_processed_week
		FROM user_weekly_preferences WHERE user_id=$1 FOR UPDATE`, userID).Scan(&enabled, &processed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if !enabled || (processed != nil && processed.Format("2006-01-02") == week.Format("2006-01-02")) {
		return 0, 0, nil
	}
	markProcessed := func() error {
		_, updateErr := tx.Exec(ctx, `UPDATE user_weekly_preferences
			SET auto_clone_processed_week=$2,updated_at=now() WHERE user_id=$1`, userID, week)
		return updateErr
	}

	// An exact current-week row is not the only conflict after the administrator
	// changes the week-start day. Respect any report covering the same dates.
	var existing int64
	err = tx.QueryRow(ctx, `WITH target(day) AS (VALUES($2::date))
		SELECT report.id FROM weekly_reports report CROSS JOIN target
		WHERE report.user_id=$1 AND report.week_start <= target.day + 6 AND report.week_start + 6 >= target.day
		ORDER BY report.week_start LIMIT 1`, userID, week).Scan(&existing)
	if err == nil {
		if err = markProcessed(); err == nil {
			err = tx.Commit(ctx)
		}
		return 0, 0, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, err
	}

	// The source is asked for the same way the conflict above is: which report
	// covers last week's seven days, not which one carries that date. The
	// conflict check already tolerates the moved grid, so an exact match here
	// only made the two halves disagree — the week after the grid moves, the
	// report the author carried on writing sits two days off the date this
	// arithmetic produces, and the copy the author asked to be made every week
	// was skipped once without a word. The latest overlapping report is the one
	// they were last writing in, which is what "last week's" means to them.
	previousWeek := week.AddDate(0, 0, -7)
	var sourceID int64
	var summary string
	err = tx.QueryRow(ctx, `SELECT r.id,r.summary FROM weekly_reports r
		WHERE r.user_id=$1 AND `+weekCoveringDays("r", 2)+`
		  AND EXISTS(SELECT 1 FROM report_items i WHERE i.report_id=r.id)
		ORDER BY r.week_start DESC LIMIT 1
		FOR SHARE`, userID, previousWeek).Scan(&sourceID, &summary)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = markProcessed(); err == nil {
			err = tx.Commit(ctx)
		}
		return 0, 0, err
	}
	if err != nil {
		return 0, 0, err
	}

	var reportID int64
	err = tx.QueryRow(ctx, `INSERT INTO weekly_reports(user_id,week_start,status,summary,source_type,source_ref)
		VALUES($1,$2,'DRAFT',$3,'CLONED',$4)
		ON CONFLICT (user_id,week_start) DO NOTHING RETURNING id`,
		userID, week, summary, "report:"+strconv.FormatInt(sourceID, 10)).Scan(&reportID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = markProcessed(); err == nil {
			err = tx.Commit(ctx)
		}
		return 0, 0, err
	}
	if err != nil {
		return 0, 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO report_items
		(report_id,work_item_id,category,title,current_result,next_plan,issue,management_ask,progress,sort_order)
		SELECT $1,work_item_id,category,title,current_result,next_plan,issue,management_ask,progress,sort_order
		FROM report_items WHERE report_id=$2 ORDER BY sort_order,id`, reportID, sourceID); err != nil {
		return 0, 0, err
	}
	comment := "지난주 보고서 #" + strconv.FormatInt(sourceID, 10) + "에서 자동 전체 내용 복제"
	if _, err = tx.Exec(ctx, `INSERT INTO report_status_history(report_id,actor_id,to_status,comment)
		VALUES($1,$2,'DRAFT',$3)`, reportID, userID, comment); err != nil {
		return 0, 0, err
	}
	if err = markProcessed(); err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return reportID, sourceID, nil
}

type reminderLeader struct {
	ID             int64
	Role           string
	OrganizationID *int64
	Weekday        string
}

func reminderDueAt(week time.Time, weekday string) time.Time {
	desired, ok := weeklyWeekdays[weekday]
	if !ok {
		desired = time.Friday
	}
	offset := (7 + int(desired) - int(week.Weekday())) % 7
	day := week.AddDate(0, 0, offset)
	return time.Date(day.Year(), day.Month(), day.Day(), weeklyReminderHour, 0, 0, 0, day.Location())
}

func (a *App) queueDueTeamReminders(ctx context.Context, now, week time.Time) error {
	// The narrowest scope first. A member covered by two leaders whose days
	// coincide is reminded once, and that once belongs to the person they
	// report to rather than whoever happens to have the lower id — which, on
	// every deployment, is the administrator account.
	rows, err := a.db.Query(ctx, `SELECT p.user_id,u.role,u.organization_id,p.team_reminder_weekday
		FROM user_weekly_preferences p JOIN users u ON u.id=p.user_id
		WHERE p.team_reminder_enabled=true AND u.active=true
		  AND u.role IN ('TEAM_LEADER','ORG_MANAGER','ADMIN')
		ORDER BY CASE u.role WHEN 'TEAM_LEADER' THEN 0 WHEN 'ORG_MANAGER' THEN 1 ELSE 2 END, p.user_id`)
	if err != nil {
		return err
	}
	leaders := []reminderLeader{}
	for rows.Next() {
		var leader reminderLeader
		if err := rows.Scan(&leader.ID, &leader.Role, &leader.OrganizationID, &leader.Weekday); err != nil {
			rows.Close()
			return err
		}
		leaders = append(leaders, leader)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, leader := range leaders {
		if now.Before(reminderDueAt(week, leader.Weekday)) {
			continue
		}
		if err := a.queueTeamReminderForLeader(ctx, leader, week); err != nil {
			a.logger.Error("queue reminders for leader", "error", err, "leaderId", leader.ID)
		}
	}
	return nil
}

func (a *App) queueTeamReminderForLeader(ctx context.Context, leader reminderLeader, week time.Time) error {
	result, err := a.queueTeamReminders(ctx, leader.ID, week, teamReminderOriginAuto, leader.Weekday)
	if errors.Is(err, errTeamReminderRoleRequired) {
		return nil
	}
	if err != nil {
		return err
	}
	if result.Queued > 0 {
		a.auditSystem(ctx, "mail.team_reminders_queued", "user", strconv.FormatInt(leader.ID, 10), map[string]any{
			"weekStart": result.WeekStart, "queued": result.Queued,
		})
		a.wakeMailWorker()
	}
	return nil
}

// queueTeamReminders is the one durable claim shared by the schedule and the
// explicit profile action. The transaction makes a manual response truthful:
// it never reports half a team queued after a later recipient insert failed.
func (a *App) queueTeamReminders(ctx context.Context, requesterID int64, week time.Time, origin, scheduledWeekday string) (teamReminderQueueResult, error) {
	result := teamReminderQueueResult{WeekStart: week.Format("2006-01-02")}
	if origin != teamReminderOriginAuto && origin != teamReminderOriginManual {
		return result, fmt.Errorf("unknown team reminder origin %q", origin)
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)

	var role string
	var organizationID *int64
	var active bool
	err = tx.QueryRow(ctx, `SELECT role,organization_id,active FROM users WHERE id=$1 FOR SHARE`, requesterID).
		Scan(&role, &organizationID, &active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!active || !canScheduleTeamReminder(role))) {
		return result, errTeamReminderRoleRequired
	}
	if err != nil {
		return result, err
	}
	if origin == teamReminderOriginAuto {
		var enabled bool
		var currentWeekday string
		err = tx.QueryRow(ctx, `SELECT team_reminder_enabled,team_reminder_weekday FROM user_weekly_preferences
			WHERE user_id=$1 FOR SHARE`, requesterID).Scan(&enabled, &currentWeekday)
		// queueDueTeamReminders took an earlier schedule snapshot. Re-check both
		// fields while holding the preference row so a concurrent weekday change
		// cannot delete an old AUTO queue and then have that old schedule recreate
		// it after the preference transaction commits.
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!enabled || currentWeekday != scheduledWeekday)) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
	}
	if role != "ADMIN" && organizationID == nil {
		return result, nil
	}

	// A member is owed a recommendation unless they have already filed for these
	// days. Asked as an overlap rather than as an exact week_start: see
	// weekCoveringDays.
	query := `SELECT u.id,u.display_name,
		COALESCE(NULLIF(btrim(mail.address),''),btrim(coalesce(u.email,''))),
		coalesce(mail.team_reminder,false), organization_mail_blocked(u.organization_id)
		FROM users u LEFT JOIN user_mail_settings mail ON mail.user_id=u.id
		WHERE u.active=true AND u.id<>$1
		  AND NOT EXISTS(SELECT 1 FROM weekly_reports report
		    WHERE report.user_id=u.id AND report.status<>'DRAFT' AND ` + weekCoveringDays("report", 2) + `)`
	args := []any{requesterID, week}
	nearer := "true"
	if role != "ADMIN" {
		args = append(args, *organizationID)
		query += ` AND u.organization_id IN ` + orgSubtree(len(args))
		nearer = fmt.Sprintf(`chain.depth < (SELECT min(own.depth) FROM chain own WHERE own.id=$%d)`, len(args))
	}
	if origin == teamReminderOriginAuto {
		query += nearerReminderLeaderClause(nearer)
	}
	query += ` ORDER BY u.id`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return result, err
	}
	type recipient struct {
		id       int64
		name     string
		address  string
		accepts  bool
		excluded bool
	}
	recipients := []recipient{}
	for rows.Next() {
		var item recipient
		if err := rows.Scan(&item.id, &item.name, &item.address, &item.accepts, &item.excluded); err != nil {
			rows.Close()
			return result, err
		}
		recipients = append(recipients, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	result.Eligible = len(recipients)
	for _, recipient := range recipients {
		// Both outrank the requester, the schedule and the click: an
		// organisation excluded from mail is excluded from this one, and a
		// reminder goes only to somebody who asked to receive reminders.
		if recipient.excluded {
			result.SkippedExcluded++
			continue
		}
		if !recipient.accepts {
			result.SkippedNotOptedIn++
			continue
		}
		if origin == teamReminderOriginManual {
			var outcome manualTeamReminderOutcome
			outcome, err = queueManualTeamReminderRecipient(ctx, tx, requesterID, recipient.id, week, recipient.address)
			if err != nil {
				return result, err
			}
			switch outcome {
			case manualTeamReminderQueued:
				result.Queued++
			case manualTeamReminderAlreadyQueued:
				result.AlreadyQueued++
			case manualTeamReminderSkippedNoAddress:
				result.SkippedNoAddress++
			}
		} else {
			if !validMailAddress(recipient.address) {
				result.SkippedNoAddress++
				continue
			}
			command, execErr := tx.Exec(ctx, `INSERT INTO team_reminder_deliveries
				(requested_by,recipient_user_id,week_start,address,origin)
				VALUES($1,$2,$3,$4,'AUTO') ON CONFLICT (recipient_user_id,week_start) DO NOTHING`,
				requesterID, recipient.id, week, recipient.address)
			if execErr != nil {
				return result, execErr
			}
			if command.RowsAffected() > 0 {
				result.Queued++
			} else {
				result.AlreadyQueued++
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	if result.SkippedNoAddress > 0 {
		a.logger.Warn("team reminder recipients have no valid address", "leaderId", requesterID,
			"origin", origin, "count", result.SkippedNoAddress)
	}
	return result, nil
}

// nearerReminderLeaderClause leaves out a member whose reminder belongs to a
// leader nearer to them than the requester.
//
// Reminders are one per member per week, and the first schedule to reach a
// member used to take them. The administrator's schedule covers everybody, so
// a team whose leader chose Friday was reminded on the administrator's
// Thursday instead — the administrator's choice overrode the leader's for
// every team in the service. A schedule now reaches only the members no nearer
// leader has a schedule for: a team leader or organisation manager whose
// organisation sits on the member's way up the chart, strictly below the
// requester's own (any of them, for an administrator, whose scope is not a
// place on the chart).
//
// Only the automatic schedule defers. A leader who presses the button means
// the people they pressed it for.
//
// farther is the SQL condition, over chain.depth, that makes a leader nearer
// than the requester.
func nearerReminderLeaderClause(farther string) string {
	return ` AND NOT EXISTS (WITH RECURSIVE chain(id, depth) AS (
			SELECT u.organization_id, 0 WHERE u.organization_id IS NOT NULL
			UNION ALL
			SELECT o.parent_id, chain.depth+1 FROM organizations o JOIN chain ON o.id=chain.id
			WHERE o.parent_id IS NOT NULL AND chain.depth < ` + strconv.Itoa(orgSubtreeDepth) + `)
		SELECT 1 FROM chain
		JOIN users nearer ON nearer.organization_id=chain.id
		JOIN user_weekly_preferences nearer_preference ON nearer_preference.user_id=nearer.id
		WHERE nearer_preference.team_reminder_enabled AND nearer.active
		  AND nearer.role IN ('TEAM_LEADER','ORG_MANAGER')
		  AND nearer.id<>$1 AND nearer.id<>u.id AND ` + farther + `)`
}

type manualTeamReminderOutcome int

const (
	manualTeamReminderQueued manualTeamReminderOutcome = iota
	manualTeamReminderAlreadyQueued
	manualTeamReminderSkippedNoAddress
)

// queueManualTeamReminderRecipient serialises a manual click with every other
// requester for this recipient/week. A queued automatic row is promoted to a
// manual one without moving its lease: waking a second worker while the first
// is using SMTP would send the same message twice. A failed row, by contrast,
// starts a fresh explicit attempt budget.
func queueManualTeamReminderRecipient(ctx context.Context, tx pgx.Tx, requesterID, recipientID int64, week time.Time, address string) (manualTeamReminderOutcome, error) {
	var status, origin string
	err := tx.QueryRow(ctx, `SELECT status,origin FROM team_reminder_deliveries
		WHERE recipient_user_id=$1 AND week_start=$2 FOR UPDATE`, recipientID, week).Scan(&status, &origin)
	if errors.Is(err, pgx.ErrNoRows) {
		if !validMailAddress(address) {
			return manualTeamReminderSkippedNoAddress, nil
		}
		command, insertErr := tx.Exec(ctx, `INSERT INTO team_reminder_deliveries
			(requested_by,recipient_user_id,week_start,address,origin)
			VALUES($1,$2,$3,$4,'MANUAL') ON CONFLICT (recipient_user_id,week_start) DO NOTHING`,
			requesterID, recipientID, week, address)
		if insertErr != nil {
			return manualTeamReminderQueued, insertErr
		}
		if command.RowsAffected() > 0 {
			return manualTeamReminderQueued, nil
		}
		// Another transaction inserted after the first statement's snapshot.
		// Its commit made ON CONFLICT do nothing; lock and classify that row now.
		err = tx.QueryRow(ctx, `SELECT status,origin FROM team_reminder_deliveries
			WHERE recipient_user_id=$1 AND week_start=$2 FOR UPDATE`, recipientID, week).Scan(&status, &origin)
	}
	if err != nil {
		return manualTeamReminderQueued, err
	}
	switch status {
	case "FAILED":
		if !validMailAddress(address) {
			return manualTeamReminderSkippedNoAddress, nil
		}
		_, err = tx.Exec(ctx, `UPDATE team_reminder_deliveries SET
			requested_by=$3,address=$4,origin='MANUAL',status='QUEUED',attempts=0,
			error_message='',next_attempt_at=now(),sent_at=NULL
			WHERE recipient_user_id=$1 AND week_start=$2`, recipientID, week, requesterID, address)
		return manualTeamReminderQueued, err
	case "QUEUED":
		// A previous explicit request already has a durable owner. A second
		// manager's duplicate click must not steal that ownership: if the second
		// manager later loses scope, pre-send revalidation would otherwise cancel
		// the first manager's still-authorised request and misattribute the mail.
		if origin == teamReminderOriginManual {
			return manualTeamReminderAlreadyQueued, nil
		}
		// This may already be claimed. Preserve attempts and next_attempt_at, but
		// detach it from the automatic preference so turning automation off cannot
		// cancel the explicit request. If the current profile address was removed,
		// keep the valid address already carried by this durable queue row.
		_, err = tx.Exec(ctx, `UPDATE team_reminder_deliveries
			SET requested_by=$3,address=CASE WHEN $5 THEN $4 ELSE address END,origin='MANUAL'
			WHERE recipient_user_id=$1 AND week_start=$2`, recipientID, week, requesterID, address, validMailAddress(address))
		return manualTeamReminderAlreadyQueued, err
	case "SENT":
		return manualTeamReminderAlreadyQueued, nil
	default:
		return manualTeamReminderQueued, fmt.Errorf("unknown team reminder status %q", status)
	}
}

func writeTeamReminderRoleRequired(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "REMINDER_ROLE_REQUIRED", "팀원 권고 메일은 팀장 이상만 발송할 수 있습니다.")
}

func (a *App) sendMyTeamReminders(w http.ResponseWriter, r *http.Request) {
	var input *struct{}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "요청 형식이 올바르지 않습니다.")
		return
	}
	p := currentPrincipal(r.Context())
	if p == nil || !canScheduleTeamReminder(p.Role) {
		writeTeamReminderRoleRequired(w)
		return
	}
	settings, err := a.loadMailSettings(r.Context())
	if err != nil {
		a.logger.Error("read mail settings for manual team reminder", "error", err,
			"trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "메일 발송 설정을 확인할 수 없습니다.")
		return
	}
	if settings.unusable() != "" {
		writeError(w, http.StatusConflict, "MAIL_RELAY_NOT_READY", "메일 릴레이가 준비되지 않았습니다. 관리자에게 SMTP 설정 확인을 요청하십시오.")
		return
	}
	location := a.serviceLocation(r.Context())
	week := currentWeekStart(time.Now().In(location), a.setting(r.Context(), "workflow.week_start", "MONDAY"))
	result, err := a.queueTeamReminders(r.Context(), p.ID, week, teamReminderOriginManual, "")
	if errors.Is(err, errTeamReminderRoleRequired) {
		writeTeamReminderRoleRequired(w)
		return
	}
	if err != nil {
		a.logger.Error("queue manual team reminders", "error", err, "userId", p.ID,
			"trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "팀원 주간보고 작성 권고 메일을 예약할 수 없습니다.")
		return
	}
	a.audit(r, p, "mail.team_reminders_manual", "user", strconv.FormatInt(p.ID, 10), map[string]any{
		"weekStart": result.WeekStart, "eligible": result.Eligible, "queued": result.Queued,
		"alreadyQueued": result.AlreadyQueued, "skippedNoAddress": result.SkippedNoAddress,
	})
	if result.Queued > 0 || result.AlreadyQueued > 0 {
		a.wakeMailWorker()
	}
	writeData(w, http.StatusAccepted, result)
}

func teamReminderSubject(serviceName, week string) string {
	return fmt.Sprintf("[%s] %s 주간보고 작성을 부탁드립니다", serviceName, week)
}

// teamReminderBody writes the reminder.
//
// It names nobody but the reader. A reminder used to end "권고한 사람: …",
// and since the administrator's schedule covers the whole service, most of
// them were signed by the administrator account — the mail read as the system
// account chasing people. Who turned the schedule on or pressed the button is
// kept on the delivery row and in the audit log, where an operator asks; the
// member is told what is missing and how to stop these mails, which is all
// the message is for.
func teamReminderBody(serviceName, week, recipientName string) string {
	return fmt.Sprintf("%s님, 안녕하세요.\n\n%s 주간보고가 아직 제출되지 않았습니다.\n"+
		"이번 주 업무와 다음 주 계획을 작성한 뒤 제출해 주세요.\n\n"+
		"--\n이 메일은 %s 에서 이번 주 주간보고 미제출자에게 보내는 작성 안내입니다.\n"+
		"받지 않으려면 개인 설정 → 주간보고 메일 발송에서 '작성 권고 메일 받기'를 끄십시오.\n",
		recipientName, week, serviceName)
}

// A claim must remain invisible to the other service replicas for longer than
// sendMail can be using the relay. The extra minute covers the small database
// updates on either side of the bounded SMTP operation. A crashed worker leaves
// the row QUEUED, so another replica can recover it after this lease expires.
func teamReminderClaimLease(mailTimeout time.Duration) time.Duration {
	if mailTimeout < 0 {
		mailTimeout = 0
	}
	return mailTimeout + time.Minute
}

type queuedTeamReminder struct {
	id          int64
	requesterID int64
	recipientID int64
	week        time.Time
	address     string
	origin      string
	attempts    int
}

func (a *App) claimNextQueuedReminder(ctx context.Context, lease time.Duration) (queuedTeamReminder, error) {
	delivery := queuedTeamReminder{}
	err := a.db.QueryRow(ctx, `UPDATE team_reminder_deliveries
		SET attempts=attempts+1,next_attempt_at=now()+$1::interval
		WHERE id=(SELECT id FROM team_reminder_deliveries
			WHERE status='QUEUED' AND next_attempt_at<=now()
			ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id,requested_by,recipient_user_id,week_start,address,origin,attempts`, lease.String()).
		Scan(&delivery.id, &delivery.requesterID, &delivery.recipientID,
			&delivery.week, &delivery.address, &delivery.origin, &delivery.attempts)
	return delivery, err
}

// reminderStillWanted is checked after claiming a row. A recipient may submit,
// a leader may be demoted, or an organisation may move while SMTP is retrying;
// none of those old snapshots authorises a late reminder. Nor does a recipient
// who has since turned reminders off, or whose organisation has since been
// excluded from mail.
func (a *App) reminderStillWanted(ctx context.Context, requesterID, recipientID int64, week time.Time, origin string) (string, bool) {
	if origin != teamReminderOriginAuto && origin != teamReminderOriginManual {
		return "", false
	}
	location := a.serviceLocation(ctx)
	configuredStart := a.setting(ctx, "workflow.week_start", "MONDAY")
	if currentWeekStart(time.Now().In(location), configuredStart).Format("2006-01-02") != week.Format("2006-01-02") {
		return "", false
	}
	var recipientName, role string
	var requesterOrg, recipientOrg *int64
	var requesterActive, recipientActive, enabled, accepts, excluded bool
	err := a.db.QueryRow(ctx, `SELECT recipient.display_name,leader.role,
		leader.organization_id,recipient.organization_id,leader.active,recipient.active,
		coalesce(preference.team_reminder_enabled,false),
		coalesce(recipient_mail.team_reminder,false),
		organization_mail_blocked(recipient.organization_id)
		FROM users leader JOIN users recipient ON recipient.id=$2
		LEFT JOIN user_weekly_preferences preference ON preference.user_id=leader.id
		LEFT JOIN user_mail_settings recipient_mail ON recipient_mail.user_id=recipient.id
		WHERE leader.id=$1`, requesterID, recipientID).
		Scan(&recipientName, &role, &requesterOrg, &recipientOrg,
			&requesterActive, &recipientActive, &enabled, &accepts, &excluded)
	if err != nil || !requesterActive || !recipientActive || !accepts || excluded ||
		(origin == teamReminderOriginAuto && !enabled) || !canScheduleTeamReminder(role) || requesterID == recipientID {
		return "", false
	}
	if role != "ADMIN" {
		if requesterOrg == nil || recipientOrg == nil {
			return "", false
		}
		var inScope bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+orgSubtree(1)+` reachable WHERE reachable.id=$2)`,
			*requesterOrg, *recipientOrg).Scan(&inScope); err != nil || !inScope {
			return "", false
		}
	}
	// The same question the queue asks, asked again at the moment of sending.
	var submitted bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM weekly_reports report
		WHERE report.user_id=$1 AND report.status<>'DRAFT' AND `+weekCoveringDays("report", 2)+`)`,
		recipientID, week).Scan(&submitted); err != nil || submitted {
		return "", false
	}
	return recipientName, true
}

// sendNextQueuedReminder is the reminder half of mailWorker. It mirrors the
// report queue's claim/backoff semantics, while building a message that does
// not require a report row to exist.
func (a *App) sendNextQueuedReminder(ctx context.Context) bool {
	settings, err := a.loadMailSettings(ctx)
	if err != nil || settings.unusable() != "" {
		return false
	}
	delivery, err := a.claimNextQueuedReminder(ctx, teamReminderClaimLease(settings.Timeout))
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		a.logger.Error("claim queued team reminder", "error", err)
		return false
	}
	recipientName, wanted := a.reminderStillWanted(ctx, delivery.requesterID, delivery.recipientID, delivery.week, delivery.origin)
	if !wanted || !validMailAddress(delivery.address) {
		// A manual click may promote an AUTO row after this worker claimed its old
		// snapshot. Delete only that snapshot; if the row changed origin/requester,
		// leave the explicit request for the next pass instead of cancelling it.
		if _, err := a.db.Exec(ctx, `DELETE FROM team_reminder_deliveries
			WHERE id=$1 AND requested_by=$2 AND origin=$3`, delivery.id, delivery.requesterID, delivery.origin); err != nil {
			a.logger.Error("discard obsolete team reminder", "delivery", delivery.id, "error", err)
		}
		return true
	}
	serviceName := a.setting(ctx, "service.name", "Weekly")
	subject := teamReminderSubject(serviceName, delivery.week.Format("2006-01-02"))
	body := teamReminderBody(serviceName, delivery.week.Format("2006-01-02"), recipientName)
	if err := a.sendMail(ctx, settings, delivery.address, subject, body); err != nil {
		if delivery.attempts >= settings.MaxAttempts {
			a.logger.Error("team reminder mail gave up", "delivery", delivery.id, "attempts", delivery.attempts, "error", err)
			if _, dbErr := a.db.Exec(ctx, `UPDATE team_reminder_deliveries
				SET status='FAILED',error_message=$2 WHERE id=$1`, delivery.id,
				trimRunes(mailUserMessage(err), 1000)); dbErr != nil {
				a.logger.Error("mark team reminder failed", "delivery", delivery.id, "error", dbErr)
			}
			return true
		}
		if _, dbErr := a.db.Exec(ctx, `UPDATE team_reminder_deliveries
			SET error_message=$2,next_attempt_at=now()+$3::interval WHERE id=$1`,
			delivery.id, trimRunes(mailUserMessage(err), 1000), retryDelay(delivery.attempts).String()); dbErr != nil {
			a.logger.Error("defer team reminder retry", "delivery", delivery.id, "error", dbErr)
		}
		a.logger.Warn("team reminder mail retry", "delivery", delivery.id, "attempts", delivery.attempts, "error", err)
		return false
	}
	if _, err := a.db.Exec(ctx, `UPDATE team_reminder_deliveries
		SET status='SENT',sent_at=now(),error_message='' WHERE id=$1`, delivery.id); err != nil {
		a.logger.Error("mark team reminder sent", "delivery", delivery.id, "error", err)
	}
	return true
}
