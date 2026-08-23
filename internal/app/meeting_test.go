package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func sectionOf(view meetingView, key string) meetingSection {
	for _, section := range view.Sections {
		if section.Key == key {
			return section
		}
	}
	return meetingSection{}
}

// A meeting agenda exists to leave things out. Work that continued exactly as
// before belongs in the written report, not on the agenda.
func TestMeetingOmitsUnchangedWork(t *testing.T) {
	unchanged := workItem(1, "정기 점검", 10, org(1), []workItemWeek{
		week("2026-08-03", 50, "점검", "점검", "", ""),
		week("2026-08-10", 50, "점검", "점검", "", "")})
	// Two weeks at the same progress is not yet a stall under the default rules.
	view := buildMeeting([]workItemView{unchanged}, "2026-08-10", rollupConfig{MergeSimilarity: 80, StallWeeks: 3, PersistentIssueWeeks: 2})
	for _, section := range view.Sections {
		if len(section.Entries) != 0 {
			t.Errorf("%s should be empty for unchanged work, got %+v", section.Key, section.Entries)
		}
	}
}

func TestMeetingSeparatesNewFromContinuingIssues(t *testing.T) {
	fresh := workItem(1, "신규 장애 대응", 10, org(1), []workItemWeek{
		week("2026-08-03", 30, "진행", "", "", ""),
		week("2026-08-10", 30, "진행", "", "이번 주 발생한 인증 오류", "")})
	ongoing := workItem(2, "결산 자동화", 20, org(1), []workItemWeek{
		week("2026-08-03", 40, "진행", "", "전표 검증 규칙 미확정", ""),
		week("2026-08-10", 40, "진행", "", "전표 검증 규칙 미확정", "")})
	view := buildMeeting([]workItemView{fresh, ongoing}, "2026-08-10", defaultRollupConfig())

	newIssues := sectionOf(view, "NEW_ISSUE")
	if len(newIssues.Entries) != 1 || newIssues.Entries[0].WorkItemID != 1 {
		t.Errorf("new issue section = %+v", newIssues.Entries)
	}
	longIssues := sectionOf(view, "LONG_ISSUE")
	if len(longIssues.Entries) != 1 || longIssues.Entries[0].WorkItemID != 2 {
		t.Errorf("persistent issue section = %+v", longIssues.Entries)
	}
	if !strings.Contains(longIssues.Entries[0].Note, "주째") {
		t.Errorf("a continuing issue should say how long, got %q", longIssues.Entries[0].Note)
	}
}

// A request that only the meeting can settle is the first thing on the agenda.
func TestMeetingPutsDecisionsFirst(t *testing.T) {
	item := workItem(1, "GPU 증설", 10, org(1), []workItemWeek{
		week("2026-08-10", 20, "검토", "", "", "예산 승인이 필요합니다")})
	view := buildMeeting([]workItemView{item}, "2026-08-10", defaultRollupConfig())
	if view.Sections[0].Key != "DECISION" {
		t.Fatalf("first section = %q, want DECISION", view.Sections[0].Key)
	}
	if len(view.Sections[0].Entries) != 1 || view.Sections[0].Entries[0].Detail != "예산 승인이 필요합니다" {
		t.Errorf("decision entries = %+v", view.Sections[0].Entries)
	}
}

// Work that vanished from this week's report is the failure a meeting catches
// and a written report never mentions.
func TestMeetingReportsWorkThatWentSilent(t *testing.T) {
	item := workItem(1, "마이그레이션", 10, org(1), []workItemWeek{
		week("2026-08-03", 40, "진행", "다음 주 이관", "", "")})
	view := buildMeeting([]workItemView{item}, "2026-08-10", defaultRollupConfig())
	silent := sectionOf(view, "SILENT")
	if len(silent.Entries) != 1 {
		t.Fatalf("silent section = %+v", silent.Entries)
	}
	if silent.Entries[0].Detail != "다음 주 이관" {
		t.Errorf("the missing week should carry last week's plan, got %q", silent.Entries[0].Detail)
	}
}

// A digest of nothing but bad news gets ignored, so completion of long work
// competes on the same scale.
func TestDigestIncludesCompletedLongRunningWork(t *testing.T) {
	done := workItem(1, "인증 체계 전환", 10, org(1), []workItemWeek{
		week("2026-07-06", 20, "a", "", "", ""), week("2026-07-13", 40, "b", "", "", ""),
		week("2026-07-20", 70, "c", "", "", ""), week("2026-07-27", 100, "완료", "", "", "")})
	entries := buildDigest([]workItemView{done}, nil, nil, defaultRollupConfig())
	if len(entries) != 1 || entries[0].Kind != "PROGRESS" {
		t.Fatalf("digest entries = %+v", entries)
	}
	if len(entries[0].Grounds) == 0 {
		t.Error("every digest entry must carry the facts it was selected on")
	}
}

// The digest is a briefing; an unbounded list is not one.
func TestDigestIsCappedAndOrderedByScore(t *testing.T) {
	items := []workItemView{}
	for id := int64(1); id <= 20; id++ {
		items = append(items, workItem(id, "업무", id, org(1), []workItemWeek{
			week("2026-08-03", 10, "a", "", "이슈", "결정 요청"),
			week("2026-08-10", 10, "a", "", "이슈", "결정 요청")}))
	}
	entries := buildDigest(items, nil, nil, defaultRollupConfig())
	if len(entries) > digestMaximumEntries {
		t.Fatalf("digest returned %d entries, want at most %d", len(entries), digestMaximumEntries)
	}
	for index := 1; index < len(entries); index++ {
		if entries[index-1].Score < entries[index].Score {
			t.Errorf("entries are not ordered by score: %d then %d", entries[index-1].Score, entries[index].Score)
		}
	}
}

// The handover exists to surface what a status field cannot: issues that
// disappeared without being explained.
func TestHandoverIssueHistoryMarksResolution(t *testing.T) {
	item := workItem(1, "연동 개발", 10, org(1), []workItemWeek{
		week("2026-07-06", 10, "시작", "", "인증서 만료", ""),
		week("2026-07-13", 30, "재발급 완료", "", "", ""),
		week("2026-07-20", 40, "진행", "", "대역폭 부족", "")})
	history := issueHistoryOf(item)
	if len(history) != 2 {
		t.Fatalf("issue history = %+v", history)
	}
	if !history[0].Resolved {
		t.Error("an issue that stopped being reported should be marked resolved")
	}
	if history[1].Resolved {
		t.Error("the issue in the latest week is still open")
	}
}

func TestHandoverMilestonesSkipUnchangedWeeks(t *testing.T) {
	item := workItem(1, "포털 개편", 10, org(1), []workItemWeek{
		week("2026-07-06", 10, "설계 착수", "", "", ""),
		week("2026-07-13", 10, "설계 계속", "", "", ""),
		week("2026-07-20", 60, "구현 완료", "", "", "")})
	milestones := milestonesOf(item)
	if len(milestones) != 2 {
		t.Fatalf("milestones = %v", milestones)
	}
	if !strings.Contains(milestones[1], "구현 완료") {
		t.Errorf("the week progress moved should be kept, got %v", milestones)
	}
}

// Issue weeks are a historical count. Reporting a finished task as an open
// risk because it once had issues is the opposite of what happened.
func TestDigestDoesNotReportCompletedWorkAsRisk(t *testing.T) {
	item := workItem(1, "전표 검증 자동화", 10, org(1), []workItemWeek{
		week("2026-07-06", 20, "a", "", "예외 케이스 미정의", ""),
		week("2026-07-13", 30, "b", "", "예외 케이스 미정의", ""),
		week("2026-07-20", 40, "c", "", "예외 케이스 미정의", ""),
		week("2026-07-27", 100, "적용 완료", "", "", "")})
	entries := buildDigest([]workItemView{item}, nil, nil, defaultRollupConfig())
	if len(entries) != 1 {
		t.Fatalf("digest entries = %+v", entries)
	}
	if entries[0].Kind != "PROGRESS" {
		t.Errorf("a finished task is news, not risk: kind = %q, grounds = %v", entries[0].Kind, entries[0].Grounds)
	}
	for _, ground := range entries[0].Grounds {
		if strings.Contains(ground.Text, "이슈가") {
			t.Errorf("resolved issue history must not be presented as an open risk: %q", ground.Text)
		}
	}
}

// Routine work is reported at 100% every week. Treating that as a completion
// fills the briefing with maintenance and buries the actual news.
func TestDigestExcludesRoutineWorkFromCompletions(t *testing.T) {
	routine := workItem(1, "정기 배포 지원", 10, org(1), []workItemWeek{
		week("2026-07-06", 100, "배포 지원", "", "", ""),
		week("2026-07-13", 100, "배포 지원", "", "", ""),
		week("2026-07-20", 100, "배포 지원", "", "", ""),
		week("2026-07-27", 100, "배포 지원", "", "", "")})
	if entries := buildDigest([]workItemView{routine}, nil, nil, defaultRollupConfig()); len(entries) != 0 {
		t.Errorf("weekly maintenance is not an executive headline: %+v", entries)
	}
}

// The screen draws the score as a bar made of its grounds. A total that its
// parts do not add up to is a bar that lies about its own arithmetic, and the
// digest's only claim to authority is that it is arithmetic anyone can check.
func TestDigestScoreEqualsTheSumOfItsGrounds(t *testing.T) {
	link := workLink{Duplicate: true, Right: workRef{OrganizationName: "다른 본부", Title: "같은 업무"}}
	items := []workItemView{
		// 결정 대기 + 이슈 지속 + 정체 + 보고 누락이 한 업무에 겹친 경우.
		workItem(1, "인증 연동", 10, org(1), []workItemWeek{
			week("2026-06-01", 30, "진행", "", "방화벽 정책 대기", "정책 결정 요청"),
			week("2026-06-08", 30, "진행", "", "방화벽 정책 대기", "정책 결정 요청"),
			week("2026-06-15", 30, "진행", "", "방화벽 정책 대기", "정책 결정 요청"),
			week("2026-07-06", 30, "진행", "", "방화벽 정책 대기", "정책 결정 요청")}),
		// 장기 진행 후 완료.
		workItem(2, "월결산 리포트", 11, org(1), []workItemWeek{
			week("2026-06-01", 20, "착수", "", "", ""),
			week("2026-06-08", 45, "데이터 정리", "", "", ""),
			week("2026-06-15", 70, "리포트 작성", "", "", ""),
			week("2026-06-22", 100, "적용 완료", "", "", "")}),
	}
	entries := buildDigest(items, map[int64]workLink{1: link}, nil, defaultRollupConfig())
	if len(entries) == 0 {
		t.Fatal("the fixture is meant to produce entries")
	}
	kinds := map[string]bool{"DECISION": true, "ISSUE": true, "STALLED": true,
		"SILENT": true, "DUPLICATE": true, "DONE": true}
	for _, entry := range entries {
		sum := 0
		for _, ground := range entry.Grounds {
			if !kinds[ground.Kind] {
				t.Errorf("%q has ground kind %q, which the screen has no colour for", entry.Title, ground.Kind)
			}
			if ground.Points <= 0 {
				t.Errorf("%q lists a ground worth %d points; a reason that adds nothing is not a reason",
					entry.Title, ground.Points)
			}
			sum += ground.Points
		}
		if sum != entry.Score {
			t.Errorf("%q scores %d but its grounds add up to %d: %+v", entry.Title, entry.Score, sum, entry.Grounds)
		}
	}
}

// A stall somebody explained is not the same item of business as one nobody
// did. The score is identical — the work is equally stopped — but an executive
// reading 진척 정체 goes and asks the owner, while 타 조직 대기 makes them
// connect two teams. Reading the same would send them to the wrong person.
func TestBlockedStallReadsDifferentlyFromAnUnexplainedOne(t *testing.T) {
	stalled := func(id int64) workItemView {
		return workItem(id, "전표 검증 자동화", 10, org(1), []workItemWeek{
			week("2026-06-01", 40, "진행", "이어서", "", ""),
			week("2026-06-08", 40, "진행", "이어서", "", ""),
			week("2026-06-15", 40, "진행", "이어서", "", ""),
			week("2026-06-22", 40, "진행", "이어서", "", ""),
		})
	}
	note := blockedNote{Title: "인증 연동", Organization: "본부 7", Owner: "담당자 7", CrossOrg: true}

	plain := buildDigest([]workItemView{stalled(1)}, nil, nil, defaultRollupConfig())
	explained := buildDigest([]workItemView{stalled(1)}, nil, map[int64]blockedNote{1: note}, defaultRollupConfig())
	if len(plain) != 1 || len(explained) != 1 {
		t.Fatalf("the fixture is meant to produce one entry each: %d and %d", len(plain), len(explained))
	}
	if plain[0].Score != explained[0].Score {
		t.Errorf("a declared cause must not change the score: %d vs %d", plain[0].Score, explained[0].Score)
	}
	if plain[0].Headline != "진척 정체" {
		t.Errorf("unexplained stall headline = %q", plain[0].Headline)
	}
	if explained[0].Headline != "타 조직 대기" {
		t.Errorf("blocked stall headline = %q, want 타 조직 대기", explained[0].Headline)
	}
	kinds := func(entry digestEntry) []string {
		out := []string{}
		for _, ground := range entry.Grounds {
			out = append(out, ground.Kind)
		}
		return out
	}
	if !slices.Contains(kinds(plain[0]), "STALLED") {
		t.Errorf("unexplained stall grounds = %v", kinds(plain[0]))
	}
	if !slices.Contains(kinds(explained[0]), "BLOCKED") {
		t.Errorf("blocked stall grounds = %v", kinds(explained[0]))
	}
	// The blocker has to be named, or the reader still has to go and ask.
	if !strings.Contains(explained[0].Grounds[0].Text, "인증 연동") ||
		!strings.Contains(explained[0].Grounds[0].Text, "본부 7") {
		t.Errorf("the ground does not name what it is waiting for: %q", explained[0].Grounds[0].Text)
	}
	// An internal blocker is a conversation, not a meeting, and says so.
	inside := buildDigest([]workItemView{stalled(1)}, nil,
		map[int64]blockedNote{1: {Title: "설계 확정", Owner: "담당자 2"}}, defaultRollupConfig())
	if inside[0].Headline != "선행 업무 대기" {
		t.Errorf("same-organisation blocker headline = %q, want 선행 업무 대기", inside[0].Headline)
	}
}

// The case the digest could not see before. This work moves every single week,
// so the stall rule is silent; it has no issue, so that rule is silent too; it
// is nobody's decision request. Its own numbers say it misses a date somebody
// agreed to, and until there was a deadline to compare against, it scored zero
// and never reached a briefing.
func TestDigestReportsWorkThatWillMissItsDeadlineWhileLookingHealthy(t *testing.T) {
	weeks := []workItemWeek{}
	for index, progress := range []int{2, 4, 6, 8, 10, 12} {
		weeks = append(weeks, workItemWeek{WeekStart: shiftISOWeek("2026-07-06", index), Progress: progress})
	}
	item := workItemView{ID: 1, Title: "레거시 배치 이관", DisplayName: "담당", Weeks: weeks}
	summarizeWorkItem(&item, defaultRollupConfig())
	if item.Stalled || item.AtRisk || item.SilentWeeks > 0 {
		t.Fatalf("the point of this case is that nothing else reports it: %+v", item)
	}
	if got := buildDigest([]workItemView{item}, nil, nil, defaultRollupConfig()); len(got) != 0 {
		t.Fatalf("without a deadline there is nothing to be late for, got %+v", got)
	}

	item.DueDate = "2026-09-21"
	summarizeWorkItem(&item, defaultRollupConfig())
	if item.DueOutlook.Kind != dueOutlookAtRisk {
		t.Fatalf("outlook=%s want=%s (%s)", item.DueOutlook.Kind, dueOutlookAtRisk, item.DueOutlook.Note)
	}
	entries := buildDigest([]workItemView{item}, nil, nil, defaultRollupConfig())
	if len(entries) != 1 {
		t.Fatalf("entries=%d want=1", len(entries))
	}
	if entries[0].Headline != "기한 초과 예상" {
		t.Errorf("headline=%q want=기한 초과 예상", entries[0].Headline)
	}
	grounds := []string{}
	for _, ground := range entries[0].Grounds {
		grounds = append(grounds, ground.Kind)
		if ground.Kind == "DEADLINE" && !strings.Contains(ground.Text, "%/주") {
			t.Errorf("the projection does not carry the pace it came from: %q", ground.Text)
		}
	}
	if len(grounds) != 1 || grounds[0] != "DEADLINE" {
		t.Errorf("grounds=%v want=[DEADLINE] — nothing else has anything to say about this work", grounds)
	}
}

// An estimate must not outrank what was actually seen. A deadline the pace is
// projected to miss scores a flat figure, while a deadline that has already
// passed escalates with how long it has been true.
func TestAProjectedDeadlineScoresBelowOneThatHasAlreadyPassed(t *testing.T) {
	build := func(due string, progress ...int) digestEntry {
		weeks := []workItemWeek{}
		for index, value := range progress {
			weeks = append(weeks, workItemWeek{WeekStart: shiftISOWeek("2026-07-06", index), Progress: value})
		}
		item := workItemView{ID: 1, Title: "업무", DueDate: due, Weeks: weeks}
		summarizeWorkItem(&item, defaultRollupConfig())
		entries := buildDigest([]workItemView{item}, nil, nil, defaultRollupConfig())
		if len(entries) != 1 {
			t.Fatalf("due %s: entries=%d want=1 (%s)", due, len(entries), item.DueOutlook.Kind)
		}
		return entries[0]
	}
	projected := build("2026-09-21", 2, 4, 6, 8, 10, 12)
	passed := build("2026-07-27", 2, 4, 6, 8, 10, 12)
	if passed.Score <= projected.Score {
		t.Errorf("a passed deadline (%d) must outrank a projected one (%d)", passed.Score, projected.Score)
	}
	if passed.Headline != "기한 초과" {
		t.Errorf("headline=%q want=기한 초과", passed.Headline)
	}
}

// An agenda that prints everything is not an agenda, and one that silently
// prints part of everything is worse. On a 300 person organisation 변경점 came
// back with 2,100 rows and nothing on screen said there were any beyond what it
// drew.
func TestMeetingSectionsSayHowManyTheyLeftOut(t *testing.T) {
	items := []workItemView{}
	for index := 0; index < meetingSectionLimit+60; index++ {
		items = append(items, workItemView{
			ID: int64(index + 1), Title: fmt.Sprintf("업무 %03d", index), DisplayName: "담당",
			Weeks: []workItemWeek{
				{WeekStart: "2026-08-10", Progress: 10, CurrentResult: "진행"},
				{WeekStart: "2026-08-17", Progress: 10 + index%40, CurrentResult: "진행"},
			},
		})
	}
	for index := range items {
		summarizeWorkItem(&items[index], defaultRollupConfig())
	}
	view := buildMeeting(items, "2026-08-17", defaultRollupConfig())

	var change *meetingSection
	for index := range view.Sections {
		if view.Sections[index].Key == "CHANGE" {
			change = &view.Sections[index]
		}
	}
	if change == nil {
		t.Fatal("no 변경점 section was built")
	}
	if len(change.Entries) > meetingSectionLimit {
		t.Errorf("the section carries %d rows, above the %d cap", len(change.Entries), meetingSectionLimit)
	}
	if change.Total <= len(change.Entries) {
		t.Fatalf("total %d does not exceed the page %d, so this proves nothing", change.Total, len(change.Entries))
	}
	if change.Limit != meetingSectionLimit {
		t.Errorf("limit=%d want=%d", change.Limit, meetingSectionLimit)
	}
}

// Cutting the tail has to cut the least important thing. Ordered by arrival the
// survivors were whichever work items had the lowest identifiers, which is not
// a reason to discuss something.
func TestMeetingPutsTheWorstNewsFirst(t *testing.T) {
	entries := []meetingEntry{
		{Title: "조금 진행", ProgressDelta: 5},
		{Title: "크게 진행", ProgressDelta: 40},
		{Title: "뒤로 감", ProgressDelta: -3},
		{Title: "변화 없음", ProgressDelta: 0, Weeks: 9},
		{Title: "많이 뒤로 감", ProgressDelta: -20},
	}
	orderMeetingEntries(entries)
	got := []string{}
	for _, entry := range entries {
		got = append(got, entry.Title)
	}
	want := []string{"많이 뒤로 감", "뒤로 감", "크게 진행", "조금 진행", "변화 없음"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got=%v\nwant=%v", got, want)
	}
}
