package app

import (
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
	entries := buildDigest([]workItemView{done}, nil, defaultRollupConfig())
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
	entries := buildDigest(items, nil, defaultRollupConfig())
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
	entries := buildDigest([]workItemView{item}, nil, defaultRollupConfig())
	if len(entries) != 1 {
		t.Fatalf("digest entries = %+v", entries)
	}
	if entries[0].Kind != "PROGRESS" {
		t.Errorf("a finished task is news, not risk: kind = %q, grounds = %v", entries[0].Kind, entries[0].Grounds)
	}
	for _, ground := range entries[0].Grounds {
		if strings.Contains(ground, "이슈가") {
			t.Errorf("resolved issue history must not be presented as an open risk: %q", ground)
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
	if entries := buildDigest([]workItemView{routine}, nil, defaultRollupConfig()); len(entries) != 0 {
		t.Errorf("weekly maintenance is not an executive headline: %+v", entries)
	}
}
