package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Cloning has two modes and the difference is the whole feature: STRUCTURE
// carries the task names into next week and leaves the writing to be done, FULL
// carries last week's text so it can be edited down. The mode that arrives is
// only defaulted when none was sent — invert that one comparison and every FULL
// request quietly becomes a STRUCTURE one, so the author opens next week to find
// their work gone and nothing anywhere says why.

func cloneInto(t *testing.T, server *testServer, cookie *http.Cookie, reportID int64, week, mode string) *http.Response {
	t.Helper()
	body := map[string]any{"targetWeekStart": week}
	if mode != "" {
		body["mode"] = mode
	}
	return server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/clone", reportID), body, cookie).Result()
}

func clonedItems(t *testing.T, server *testServer, week string) []reportItem {
	t.Helper()
	rows, err := server.app.db.Query(server.ctx(),
		`SELECT i.title, i.current_result, i.next_plan, i.progress
			FROM report_items i JOIN weekly_reports r ON r.id = i.report_id
			WHERE r.week_start = $1::date ORDER BY i.sort_order`, week)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	items := []reportItem{}
	for rows.Next() {
		var item reportItem
		if err := rows.Scan(&item.Title, &item.CurrentResult, &item.NextPlan, &item.Progress); err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	return items
}

// guards: cloneReport
func TestCloningInFullModeCarriesTheWritingAndStructureModeDoesNot(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("clone_modes", "USER", nil)
	reportID := server.submitted(author, "2026-08-24", "복제할 보고서")

	if reply := cloneInto(t, server, author, reportID, "2026-09-07", "FULL"); reply.StatusCode != http.StatusOK && reply.StatusCode != http.StatusCreated {
		t.Fatalf("FULL 복제: %d", reply.StatusCode)
	}
	full := clonedItems(t, server, "2026-09-07")
	if len(full) == 0 {
		t.Fatal("FULL 복제가 항목을 하나도 만들지 않았습니다")
	}
	carried := false
	for _, item := range full {
		if item.CurrentResult != "" || item.Progress != 0 {
			carried = true
		}
	}
	if !carried {
		t.Errorf("FULL 인데 지난주 내용이 하나도 넘어오지 않았습니다: %+v", full)
	}

	if reply := cloneInto(t, server, author, reportID, "2026-09-14", "STRUCTURE"); reply.StatusCode != http.StatusOK && reply.StatusCode != http.StatusCreated {
		t.Fatalf("STRUCTURE 복제: %d", reply.StatusCode)
	}
	structure := clonedItems(t, server, "2026-09-14")
	if len(structure) == 0 {
		t.Fatal("STRUCTURE 복제가 항목을 하나도 만들지 않았습니다")
	}
	for _, item := range structure {
		if item.Title == "" {
			t.Errorf("STRUCTURE 인데 업무 이름이 비었습니다: %+v", item)
		}
		if item.CurrentResult != "" || item.NextPlan != "" || item.Progress != 0 {
			t.Errorf("STRUCTURE 인데 지난주 내용이 따라왔습니다: %+v", item)
		}
	}

	// The report's own history is where an author looks to find out where a
	// week came from. It records which of the two clones this was, and saying
	// the wrong one is worse than saying nothing: the author reads that their
	// text was carried over and goes looking for writing that was never copied.
	if comment := cloneComment(t, server, "2026-09-07"); !containsAny(comment, "전체 내용") {
		t.Errorf("FULL 복제인데 이력이 %q 라고 적혀 있습니다", comment)
	}
	if comment := cloneComment(t, server, "2026-09-14"); !containsAny(comment, "업무 구조") {
		t.Errorf("STRUCTURE 복제인데 이력이 %q 라고 적혀 있습니다", comment)
	}
}

// guards: cloneReport
//
// Cloning onto a week that already holds a report is an ordinary mistake — the
// author forgot they had started it. What they must get back is the week and
// what to do about it, not a database error.
//
// The overlap check answers first, which is why the unique-constraint branch
// below it survives every mutation: it is a second layer, reached only if the
// first ever lets one through. This test pins the sentence the author actually
// reads rather than the code path that produces it — the first draft asserted
// REPORT_EXISTS because that is what the branch I was looking at emits, and
// the product was already doing something better.
func TestCloningOntoAWeekThatAlreadyHasAReportSaysSo(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("clone_conflict", "USER", nil)
	reportID := server.submitted(author, "2026-08-24", "복제할 보고서")

	if reply := cloneInto(t, server, author, reportID, "2026-09-07", "STRUCTURE"); reply.StatusCode != http.StatusOK && reply.StatusCode != http.StatusCreated {
		t.Fatalf("첫 복제: %d", reply.StatusCode)
	}
	again := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/clone", reportID),
		map[string]any{"targetWeekStart": "2026-09-07", "mode": "STRUCTURE"}, author)
	if again.Code != http.StatusConflict {
		t.Fatalf("같은 주차로 다시 복제: %d %s", again.Code, again.Body.String())
	}
	if code := errorCode(again); code != "REPORT_PERIOD_OVERLAPS" && code != "REPORT_EXISTS" {
		t.Errorf("코드가 %q 입니다: %s", code, again.Body.String())
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(again.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !containsAny(envelope.Error.Message, "2026-09-07") {
		t.Errorf("어느 주차인지 말하지 않습니다: %q", envelope.Error.Message)
	}
	// A refusal that only says no leaves the author guessing which of the two
	// reports to keep.
	if !containsAny(envelope.Error.Message, "여십시오", "이미") {
		t.Errorf("무엇을 하라는지 말하지 않습니다: %q", envelope.Error.Message)
	}
	// And the refused clone must not have left a second report behind.
	var copies int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM weekly_reports WHERE week_start = '2026-09-07'`).Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Errorf("거부된 복제가 %d개의 보고서를 남겼습니다", copies)
	}
}

// cloneComment returns what the report's own history says about how it was made.
func cloneComment(t *testing.T, server *testServer, week string) string {
	t.Helper()
	var comment string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT coalesce(h.comment, '') FROM report_status_history h
			JOIN weekly_reports r ON r.id = h.report_id
			WHERE r.week_start = $1::date ORDER BY h.id DESC LIMIT 1`, week).Scan(&comment); err != nil {
		t.Fatalf("read the history for %s: %v", week, err)
	}
	return comment
}

// Two clones onto the same week at the same moment.
//
// weekIsFree runs before the insert, so an ordinary duplicate never reaches the
// unique index and the branch that turns 23505 into "그 주차 보고서가 이미
// 있습니다" is unreachable from a sequential test — a mutation run found both of
// its halves unguarded. The only way there is a race: one person on two
// devices, a double-clicked button, two tabs. Both requests pass the check and
// one loses at the index.
//
// What must not happen is that the loser is told the server broke. It did not:
// somebody else got there first, which is a sentence the reader can act on.

// guards: cloneReport
func TestTwoClonesOntoTheSameWeekAtOnceBothGetAnAnswerTheyCanActOn(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("clone_race", "USER", nil)
	source := server.submitted(author, "2026-08-17", "복제할 원본")

	const attempts = 6
	type outcome struct {
		code int
		body string
	}
	results := make(chan outcome, attempts)
	start := make(chan struct{})
	var waiting sync.WaitGroup
	for index := 0; index < attempts; index++ {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			<-start
			w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/clone", source),
				map[string]any{"targetWeekStart": "2026-09-07", "mode": "STRUCTURE"}, author)
			results <- outcome{w.Code, w.Body.String()}
		}()
	}
	close(start)
	waiting.Wait()
	close(results)

	created, refused := 0, 0
	for item := range results {
		switch {
		case item.code == http.StatusOK || item.code == http.StatusCreated:
			created++
		case item.code == http.StatusConflict:
			refused++
			// The reader has to be able to tell "somebody already made this" from
			// "the server fell over", and the week has to be named either way.
			if !strings.Contains(item.body, "2026-09-07") {
				t.Errorf("a refusal did not say which week: %s", item.body)
			}
		default:
			t.Errorf("a concurrent clone answered %d, which tells the reader nothing to do: %s", item.code, item.body)
		}
	}
	if created != 1 {
		t.Errorf("%d of %d clones were created — the week is meant to hold one report", created, attempts)
	}
	if refused != attempts-1 {
		t.Errorf("%d refusals for %d attempts", refused, attempts-1)
	}

	// And exactly one report exists for that week, whatever the ordering was.
	var reports int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM weekly_reports WHERE week_start='2026-09-07' AND user_id=$1`,
		server.userIDOf(server.lastCreatedUsername("clone_race"))).Scan(&reports); err != nil {
		t.Fatalf("count the week's reports: %v", err)
	}
	if reports != 1 {
		t.Errorf("the week holds %d reports, want 1", reports)
	}
}
