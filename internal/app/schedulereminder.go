package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 마감 임박 알림.
//
// The board answers "what is this department doing this month". It does not
// walk over and tell somebody that the thing they put on it three weeks ago is
// due on Thursday — and a line nobody has looked at since the day it was
// written is exactly the line that gets missed.
//
// So once a day, each assignee who asked for it gets the short list: their own
// open lines whose deadline is within the next five days, grouped by how many
// days are left. Grouped rather than sorted, because "이틀 남았습니다" is the
// sentence somebody acts on and a date is one they have to do arithmetic with.
//
// Deliberately not included, and both are worth saying out loud:
//
//   - The start date. Work that has not begun but is due on Friday is exactly
//     what this is for; filtering by start date would hide it.
//   - Anything already due or overdue. The window is D-5 to D-1: this is a
//     warning, and a warning that arrives on the day is not one. The board
//     itself already shows 오늘 and 지연 in red.

const (
	// scheduleReminderDays is how far ahead the digest looks.
	scheduleReminderDays = 5
	// scheduleReminderHour is when it goes out, in the service timezone — the
	// same hour the team reminder uses, because both are "read this with your
	// morning coffee" mail and two different hours would be two surprises.
	scheduleReminderHour = weeklyReminderHour
	// scheduleReminderLimit caps one digest. A list longer than this is not a
	// reminder any more, and the count in the subject still tells the truth.
	scheduleReminderLimit = 100
)

// dueSoonTask is one line of the digest.
type dueSoonTask struct {
	DaysLeft int
	Title    string
	Category string
	Priority string
	EndDate  string
	SRID     string
	SRURL    string
	Note     string
}

// scheduleTasksDueSoon returns one person's open work due within the window,
// most urgent first.
//
// Open, because a ticked line is done work and mailing somebody about it is how
// a reminder becomes noise. Their own, because this is the assignee's list —
// the person who put the line on the board for somebody else is not the one who
// has to finish it.
func (a *App) scheduleTasksDueSoon(ctx context.Context, userID int64, today time.Time) ([]dueSoonTask, error) {
	from := today.AddDate(0, 0, 1)
	to := today.AddDate(0, 0, scheduleReminderDays)
	rows, err := a.db.Query(ctx, `
		SELECT s.title, s.category, s.priority, s.end_date, s.sr_id, s.note
		FROM schedule_tasks s
		WHERE s.user_id=$1 AND s.done_at IS NULL AND s.end_date BETWEEN $2 AND $3
		ORDER BY s.end_date, `+priorityRank+`, s.title, s.id
		LIMIT $4`, userID, from, to, scheduleReminderLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	itsm := a.loadITSMSettings(ctx)
	tasks := []dueSoonTask{}
	for rows.Next() {
		var task dueSoonTask
		var end time.Time
		if err := rows.Scan(&task.Title, &task.Category, &task.Priority, &end, &task.SRID, &task.Note); err != nil {
			return nil, err
		}
		task.EndDate = end.Format(dateLayout)
		task.Priority = normalizePriority(task.Priority)
		task.DaysLeft = int(end.Sub(today).Hours()/24 + 0.5)
		if task.SRID != "" {
			task.SRURL = itsm.linkFor(task.SRID)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func scheduleReminderSubject(serviceName string, tasks []dueSoonTask) string {
	// The subject carries the two facts a person decides on from the list view:
	// how many, and how soon the nearest one is.
	soonest := scheduleReminderDays
	for _, task := range tasks {
		if task.DaysLeft < soonest {
			soonest = task.DaysLeft
		}
	}
	return fmt.Sprintf("[%s] 마감 임박 업무 %d건 · 가장 빠른 마감 D-%d", serviceName, len(tasks), soonest)
}

// scheduleReminderBody writes the list the way it is read: by how long is left.
func scheduleReminderBody(recipientName, today string, tasks []dueSoonTask, label string) string {
	byDay := map[int][]dueSoonTask{}
	for _, task := range tasks {
		byDay[task.DaysLeft] = append(byDay[task.DaysLeft], task)
	}
	days := make([]int, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Ints(days)

	var out strings.Builder
	fmt.Fprintf(&out, "%s님, 안녕하세요.\n\n", recipientName)
	fmt.Fprintf(&out, "%s 기준으로 마감이 %d일 안으로 다가온 업무가 %d건 있습니다.\n",
		today, scheduleReminderDays, len(tasks))
	for _, day := range days {
		group := byDay[day]
		fmt.Fprintf(&out, "\n[D-%d · %s 마감] %d건\n", day, group[0].EndDate, len(group))
		for _, task := range group {
			fmt.Fprintf(&out, " - [%s] %s", priorityLabel(task.Priority), task.Title)
			if task.Category != "" {
				fmt.Fprintf(&out, " · %s", task.Category)
			}
			if task.SRID != "" {
				fmt.Fprintf(&out, " · %s %s", label, task.SRID)
				if task.SRURL != "" {
					fmt.Fprintf(&out, " (%s)", task.SRURL)
				}
			}
			out.WriteString("\n")
			if note := strings.TrimSpace(task.Note); note != "" {
				fmt.Fprintf(&out, "     %s\n", strings.ReplaceAll(note, "\n", "\n     "))
			}
		}
	}
	out.WriteString("\n--\n이 메일은 업무 상황판의 마감 임박 알림입니다. 끝낸 일은 상황판에서 체크하면 다음 알림에서 빠집니다.\n")
	out.WriteString("받지 않으려면 개인 설정에서 마감 임박 알림을 끄십시오.\n")
	return out.String()
}

// priorityLabel is the word beside the colour: 긴급, 중요, 필요, 일반.
//
// In a mail there is no colour at all, which is the case the board's own legend
// exists for — the word has to carry it on its own here.
func priorityLabel(priority string) string {
	switch priority {
	case priorityUrgent:
		return "긴급"
	case priorityImportant:
		return "중요"
	case priorityNeeded:
		return "필요"
	}
	return "일반"
}

// queueDueScheduleReminders puts today's digest in the queue for everybody who
// asked for one and has something to be reminded of.
//
// Nothing is queued for a person with an empty list: an empty reminder is a
// reminder to ignore reminders.
func (a *App) queueDueScheduleReminders(ctx context.Context, localNow time.Time) error {
	if localNow.Hour() < scheduleReminderHour {
		return nil
	}
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)

	// The whole cohort in one query rather than one per person: the window is
	// the same for everybody and the board is small.
	rows, err := a.db.Query(ctx, `
		SELECT m.user_id, m.address
		FROM user_mail_settings m
		JOIN users u ON u.id = m.user_id AND u.active
		WHERE m.schedule_reminder = true AND m.address <> ''
		  AND EXISTS (SELECT 1 FROM schedule_tasks s
		              WHERE s.user_id = m.user_id AND s.done_at IS NULL
		                AND s.end_date BETWEEN $1 AND $2)
		  AND NOT EXISTS (SELECT 1 FROM schedule_reminder_deliveries d
		                  WHERE d.user_id = m.user_id AND d.reminder_on = $3)`,
		today.AddDate(0, 0, 1), today.AddDate(0, 0, scheduleReminderDays), today)
	if err != nil {
		return err
	}
	defer rows.Close()
	type recipient struct {
		id      int64
		address string
	}
	var recipients []recipient
	for rows.Next() {
		var item recipient
		if err := rows.Scan(&item.id, &item.address); err != nil {
			return err
		}
		recipients = append(recipients, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range recipients {
		// ON CONFLICT rather than a check, because two replicas reach this line
		// at the same second and the unique key is the only thing that decides.
		if _, err := a.db.Exec(ctx, `
			INSERT INTO schedule_reminder_deliveries(user_id, reminder_on, address)
			VALUES($1,$2,$3) ON CONFLICT (user_id, reminder_on) DO NOTHING`,
			item.id, today, item.address); err != nil {
			return err
		}
	}
	return nil
}

type queuedScheduleReminder struct {
	id       int64
	userID   int64
	day      time.Time
	address  string
	attempts int
}

func (a *App) claimNextQueuedScheduleReminder(ctx context.Context, lease time.Duration) (queuedScheduleReminder, error) {
	delivery := queuedScheduleReminder{}
	err := a.db.QueryRow(ctx, `UPDATE schedule_reminder_deliveries
		SET attempts=attempts+1,next_attempt_at=now()+$1::interval
		WHERE id=(SELECT id FROM schedule_reminder_deliveries
			WHERE status='QUEUED' AND next_attempt_at<=now()
			ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id,user_id,reminder_on,address,attempts`, lease.String()).
		Scan(&delivery.id, &delivery.userID, &delivery.day, &delivery.address, &delivery.attempts)
	return delivery, err
}

// sendNextQueuedScheduleReminder delivers one digest and says whether to look
// for another.
func (a *App) sendNextQueuedScheduleReminder(ctx context.Context) bool {
	settings, err := a.loadMailSettings(ctx)
	if err != nil || settings.unusable() != "" {
		return false
	}
	delivery, err := a.claimNextQueuedScheduleReminder(ctx, teamReminderClaimLease(settings.Timeout))
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		a.logger.Error("claim queued schedule reminder", "error", err)
		return false
	}

	// Everything is re-read after the claim. A person may switch the reminder
	// off, change their address, or simply finish the work while the relay was
	// down — none of those authorise yesterday's mail going out today.
	var name, address string
	var wanted, active bool
	if err := a.db.QueryRow(ctx, `SELECT u.display_name, u.active,
		coalesce(m.schedule_reminder,false), coalesce(m.address,'')
		FROM users u LEFT JOIN user_mail_settings m ON m.user_id=u.id WHERE u.id=$1`,
		delivery.userID).Scan(&name, &active, &wanted, &address); err != nil {
		a.logger.Error("read schedule reminder recipient", "delivery", delivery.id, "error", err)
		return false
	}
	tasks, err := a.scheduleTasksDueSoon(ctx, delivery.userID, delivery.day)
	if err != nil {
		a.logger.Error("read due schedule tasks", "delivery", delivery.id, "error", err)
		return false
	}
	if !wanted || !active || !validMailAddress(address) || len(tasks) == 0 {
		// Nothing to say any more. The row is removed rather than marked sent,
		// so tomorrow starts clean and nobody reads a "SENT" that never was.
		if _, err := a.db.Exec(ctx, `DELETE FROM schedule_reminder_deliveries WHERE id=$1`, delivery.id); err != nil {
			a.logger.Error("discard obsolete schedule reminder", "delivery", delivery.id, "error", err)
		}
		return true
	}

	subject := scheduleReminderSubject(a.setting(ctx, "service.name", "Weekly"), tasks)
	body := scheduleReminderBody(name, delivery.day.Format(dateLayout), tasks,
		itsmLabelOrDefault(a.setting(ctx, "itsm.label", "")))
	if err := a.sendMail(ctx, settings, address, subject, body); err != nil {
		if delivery.attempts >= settings.MaxAttempts {
			a.logger.Error("schedule reminder gave up", "delivery", delivery.id, "attempts", delivery.attempts, "error", err)
			if _, dbErr := a.db.Exec(ctx, `UPDATE schedule_reminder_deliveries
				SET status='FAILED',error_message=$2 WHERE id=$1`, delivery.id,
				trimRunes(mailUserMessage(err), 1000)); dbErr != nil {
				a.logger.Error("mark schedule reminder failed", "delivery", delivery.id, "error", dbErr)
			}
			return true
		}
		if _, dbErr := a.db.Exec(ctx, `UPDATE schedule_reminder_deliveries
			SET error_message=$2,next_attempt_at=now()+$3::interval WHERE id=$1`,
			delivery.id, trimRunes(mailUserMessage(err), 1000), retryDelay(delivery.attempts).String()); dbErr != nil {
			a.logger.Error("defer schedule reminder retry", "delivery", delivery.id, "error", dbErr)
		}
		a.logger.Warn("schedule reminder retry", "delivery", delivery.id, "attempts", delivery.attempts, "error", err)
		return false
	}
	if _, err := a.db.Exec(ctx, `UPDATE schedule_reminder_deliveries
		SET status='SENT',sent_at=now(),error_message='' WHERE id=$1`, delivery.id); err != nil {
		a.logger.Error("mark schedule reminder sent", "delivery", delivery.id, "error", err)
	}
	return true
}
