package app

import (
	"fmt"
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"
)

// mimeDecoder reads the encoded-words the product writes into a subject.
var mimeDecoder = mime.WordDecoder{}

// A line put on the board three weeks ago is the line that gets missed. Once a
// day, the assignee gets the short list of their own work whose deadline is
// inside the next five days, grouped by how many days are left.

// reminderDay is the day these tests pretend it is, and the dates below are
// counted from it. A fixed day rather than the real one so the groups in the
// mail can be asserted exactly.
var reminderDay = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

func dayFromReminder(offset int) string {
	return reminderDay.AddDate(0, 0, offset).Format("2006-01-02")
}

// guards: queueDueScheduleReminders, sendNextQueuedScheduleReminder, scheduleTasksDueSoon, scheduleReminderBody
func TestTheDeadlineDigestListsWhatIsDueGroupedByDaysLeft(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	org := server.createOrganization("마감 본부", "DUE")
	owner := server.createUser("due_owner", "TEAM_LEADER", &org)
	other := server.createUser("due_other", "USER", &org)
	otherID := server.meID(other)

	if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
		"address": "owner@internal.test", "onSubmit": false, "scheduleReminder": true,
	}, owner); w.Code != http.StatusOK {
		t.Fatalf("turn the reminder on: %d %s", w.Code, w.Body.String())
	}

	// Inside the window. The first two share a day, which is what makes the
	// grouping worth asserting.
	server.addScheduleTask(owner, map[string]any{"title": "안전점검 보고서 제출",
		"startDate": dayFromReminder(-10), "endDate": dayFromReminder(1), "priority": "URGENT"})
	server.addScheduleTask(owner, map[string]any{"title": "협력사 계약 검토",
		"startDate": dayFromReminder(1), "endDate": dayFromReminder(1), "category": "계약"})
	server.addScheduleTask(owner, map[string]any{"title": "창고 재고실사",
		"startDate": dayFromReminder(-1), "endDate": dayFromReminder(2), "priority": "IMPORTANT"})
	server.addScheduleTask(owner, map[string]any{"title": "월간 결산 마감",
		"startDate": dayFromReminder(4), "endDate": dayFromReminder(5), "priority": "NEEDED"})

	// Outside it, each for a different reason.
	server.addScheduleTask(owner, map[string]any{"title": "아직 먼 일",
		"startDate": dayFromReminder(6), "endDate": dayFromReminder(6)})
	server.addScheduleTask(owner, map[string]any{"title": "오늘 마감",
		"startDate": dayFromReminder(0), "endDate": dayFromReminder(0)})
	server.addScheduleTask(owner, map[string]any{"title": "이미 지난 일",
		"startDate": dayFromReminder(-9), "endDate": dayFromReminder(-2)})
	done := server.addScheduleTask(owner, map[string]any{"title": "이미 완료한 일",
		"startDate": dayFromReminder(1), "endDate": dayFromReminder(1)})
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", done),
		map[string]any{"done": true}, owner); w.Code != http.StatusOK {
		t.Fatalf("tick a line: %d %s", w.Code, w.Body.String())
	}
	// And somebody else's line, which is somebody else's mail.
	server.addScheduleTask(owner, map[string]any{"title": "남의 일",
		"startDate": dayFromReminder(1), "endDate": dayFromReminder(1), "assigneeId": otherID})

	// 09:30 in the service timezone: after the hour the digest goes out.
	when := reminderDay.Add(9*time.Hour + 30*time.Minute)
	if err := server.app.queueDueScheduleReminders(server.ctx(), when); err != nil {
		t.Fatalf("queue the digests: %v", err)
	}
	if !server.app.sendNextQueuedScheduleReminder(server.ctx()) {
		t.Fatalf("nothing was sent")
	}

	messages := relay.awaitRelay(t, 1)
	if len(messages) != 1 {
		t.Fatalf("the relay received %d messages, want 1 — one person asked for a digest", len(messages))
	}
	head, _, _ := strings.Cut(messages[0], "\r\n\r\n")
	body := decodedBody(t, messages[0])

	if !strings.Contains(head, "To: owner@internal.test") {
		t.Errorf("the digest went to the wrong address:\n%s", head)
	}
	for _, want := range []string{"마감 임박 업무 4건", "D-1"} {
		if !strings.Contains(decodeSubject(t, head), want) {
			t.Errorf("the subject does not carry %q: %s", want, decodeSubject(t, head))
		}
	}
	// Grouped by days left, most urgent group first.
	first := strings.Index(body, "[D-1 ·")
	second := strings.Index(body, "[D-2 ·")
	fifth := strings.Index(body, "[D-5 ·")
	if first < 0 || second < 0 || fifth < 0 || !(first < second && second < fifth) {
		t.Errorf("the groups are not in order (D-1 %d, D-2 %d, D-5 %d):\n%s", first, second, fifth, body)
	}
	if !strings.Contains(body, fmt.Sprintf("[D-1 · %s 마감] 2건", dayFromReminder(1))) {
		t.Errorf("the D-1 group does not say the date and the count:\n%s", body)
	}
	// The word carries the priority; there is no colour in a mail.
	if !strings.Contains(body, "[긴급] 안전점검 보고서 제출") || !strings.Contains(body, "[일반] 협력사 계약 검토 · 계약") {
		t.Errorf("a line lost its priority or its category:\n%s", body)
	}
	for _, unwanted := range []string{"아직 먼 일", "오늘 마감", "이미 지난 일", "이미 완료한 일", "남의 일"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the digest carried %q, which is outside what it is for:\n%s", unwanted, body)
		}
	}
}

func decodeSubject(t *testing.T, head string) string {
	t.Helper()
	for _, line := range strings.Split(head, "\r\n") {
		if rest, found := strings.CutPrefix(line, "Subject: "); found {
			decoded, err := mimeDecoder.DecodeHeader(rest)
			if err != nil {
				t.Fatalf("decode the subject: %v", err)
			}
			return decoded
		}
	}
	t.Fatalf("the message has no subject:\n%s", head)
	return ""
}

// One a day, and the unique key is what makes that true when the worker runs
// every minute and the service may be replicated or restarted.

// guards: queueDueScheduleReminders
func TestTheDigestGoesOutOnceADayHoweverOftenTheWorkerRuns(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	owner := server.createUser("due_once", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
		"address": "once@internal.test", "onSubmit": false, "scheduleReminder": true,
	}, owner); w.Code != http.StatusOK {
		t.Fatalf("turn the reminder on: %d %s", w.Code, w.Body.String())
	}
	server.addScheduleTask(owner, map[string]any{"title": "하루 한 번",
		"startDate": dayFromReminder(1), "endDate": dayFromReminder(1)})
	// Still inside the window tomorrow, when the line above has become D-0 and
	// dropped out of it.
	server.addScheduleTask(owner, map[string]any{"title": "모레도 남는 일",
		"startDate": dayFromReminder(1), "endDate": dayFromReminder(3)})

	when := reminderDay.Add(9*time.Hour + 30*time.Minute)
	for round := 0; round < 3; round++ {
		if err := server.app.queueDueScheduleReminders(server.ctx(), when); err != nil {
			t.Fatalf("queue round %d: %v", round+1, err)
		}
	}
	var queued int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM schedule_reminder_deliveries`).Scan(&queued); err != nil {
		t.Fatalf("count the queue: %v", err)
	}
	if queued != 1 {
		t.Fatalf("three ticks queued %d digests, want 1", queued)
	}
	// Tomorrow is a different day and gets its own.
	if err := server.app.queueDueScheduleReminders(server.ctx(), when.AddDate(0, 0, 1)); err != nil {
		t.Fatalf("queue tomorrow: %v", err)
	}
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM schedule_reminder_deliveries`).Scan(&queued); err != nil {
		t.Fatalf("count the queue: %v", err)
	}
	if queued != 2 {
		t.Errorf("the next day queued %d digests in total, want 2", queued)
	}
}

// Nobody is mailed for asking nothing, and nobody is mailed about an empty
// list. Both are how a reminder turns into something people filter away.

// guards: queueDueScheduleReminders, sendNextQueuedScheduleReminder
func TestNoDigestForSomebodyWhoDidNotAskAndNoneWithNothingInIt(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	when := reminderDay.Add(9*time.Hour + 30*time.Minute)

	// Off by default: an address saved for report mail is not consent to this.
	quiet := server.createUser("due_quiet", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "quiet@internal.test", "onSubmit": true}, quiet); w.Code != http.StatusOK {
		t.Fatalf("save the address: %d %s", w.Code, w.Body.String())
	}
	server.addScheduleTask(quiet, map[string]any{"title": "조용히",
		"startDate": dayFromReminder(1), "endDate": dayFromReminder(1)})

	// Asked for it, and then finished the work before the relay came back.
	keen := server.createUser("due_keen", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail", map[string]any{
		"address": "keen@internal.test", "onSubmit": false, "scheduleReminder": true,
	}, keen); w.Code != http.StatusOK {
		t.Fatalf("turn the reminder on: %d %s", w.Code, w.Body.String())
	}
	task := server.addScheduleTask(keen, map[string]any{"title": "곧 끝낼 일",
		"startDate": dayFromReminder(1), "endDate": dayFromReminder(1)})

	if err := server.app.queueDueScheduleReminders(server.ctx(), when); err != nil {
		t.Fatalf("queue the digests: %v", err)
	}
	var queued int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM schedule_reminder_deliveries`).Scan(&queued); err != nil {
		t.Fatalf("count the queue: %v", err)
	}
	if queued != 1 {
		t.Fatalf("%d digests were queued, want 1 — only one person asked", queued)
	}

	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/schedule/%d/done", task),
		map[string]any{"done": true}, keen); w.Code != http.StatusOK {
		t.Fatalf("tick the line: %d %s", w.Code, w.Body.String())
	}
	if !server.app.sendNextQueuedScheduleReminder(server.ctx()) {
		t.Fatalf("the worker did not deal with the queued row")
	}
	if messages := relay.received(); len(messages) != 0 {
		t.Errorf("the relay received %d messages; the list was empty by the time it went out:\n%v",
			len(messages), messages)
	}
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM schedule_reminder_deliveries`).Scan(&queued); err != nil {
		t.Fatalf("count the queue: %v", err)
	}
	if queued != 0 {
		t.Errorf("%d rows are still queued; a digest with nothing to say should be dropped", queued)
	}
}

// guards: priorityLabel, scheduleReminderSubject
func TestTheSubjectSaysHowManyAndHowSoon(t *testing.T) {
	tasks := []dueSoonTask{{DaysLeft: 3}, {DaysLeft: 1}, {DaysLeft: 5}}
	if got := scheduleReminderSubject("Weekly", tasks); got != "[Weekly] 마감 임박 업무 3건 · 가장 빠른 마감 D-1" {
		t.Errorf("the subject reads %q", got)
	}
	for value, want := range map[string]string{
		priorityUrgent: "긴급", priorityImportant: "중요", priorityNeeded: "필요",
		priorityNormal: "일반", "무엇이든": "일반",
	} {
		if got := priorityLabel(value); got != want {
			t.Errorf("priorityLabel(%q) = %q, want %q", value, got, want)
		}
	}
}
