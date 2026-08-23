package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestResolvePeriodRanges(t *testing.T) {
	now := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		kind, period, wantPeriod, wantStart, wantEnd, wantLabel string
	}{
		{periodMonth, "2026-02", "2026-02", "2026-02-01", "2026-02-28", "2026년 2월"},
		{periodMonth, "2024-02", "2024-02", "2024-02-01", "2024-02-29", "2024년 2월"},
		{periodMonth, "", "2026-08", "2026-08-01", "2026-08-31", "2026년 8월"},
		{periodQuarter, "2026-Q1", "2026-Q1", "2026-01-01", "2026-03-31", "2026년 1분기"},
		{periodQuarter, "", "2026-Q3", "2026-07-01", "2026-09-30", "2026년 3분기"},
		{periodHalf, "2026-H1", "2026-H1", "2026-01-01", "2026-06-30", "2026년 상반기"},
		{periodHalf, "", "2026-H2", "2026-07-01", "2026-12-31", "2026년 하반기"},
		{periodYear, "2025", "2025", "2025-01-01", "2025-12-31", "2025년"},
		{periodYear, "", "2026", "2026-01-01", "2026-12-31", "2026년"},
	}
	for _, testCase := range cases {
		got, err := resolvePeriod(testCase.kind, testCase.period, now)
		if err != nil {
			t.Fatalf("resolvePeriod(%s,%s): %v", testCase.kind, testCase.period, err)
		}
		if got.Period != testCase.wantPeriod || got.Start != testCase.wantStart || got.End != testCase.wantEnd || got.Label != testCase.wantLabel {
			t.Errorf("resolvePeriod(%s,%s) = %+v, want %s %s..%s %s",
				testCase.kind, testCase.period, got, testCase.wantPeriod, testCase.wantStart, testCase.wantEnd, testCase.wantLabel)
		}
	}
}

func TestResolvePeriodRejectsInvalidInput(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	invalid := []struct{ kind, period string }{
		{periodMonth, "2026-13"}, {periodMonth, "2026"}, {periodMonth, "abcd-01"},
		{periodQuarter, "2026-Q5"}, {periodQuarter, "2026-3"},
		{periodHalf, "2026-H3"}, {periodYear, "12"}, {"WEEK", "2026-08"},
	}
	for _, testCase := range invalid {
		if _, err := resolvePeriod(testCase.kind, testCase.period, now); err == nil {
			t.Errorf("resolvePeriod(%s,%s) accepted invalid input", testCase.kind, testCase.period)
		}
	}
}

func TestExpectedWeekStartsCoversOverlappingWeeks(t *testing.T) {
	period, err := resolvePeriod(periodMonth, "2026-08", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	weeks := expectedWeekStarts(period, "MONDAY")
	// 2026-08-01 is a Saturday, so the first overlapping Monday week starts 2026-07-27.
	if weeks[0] != "2026-07-27" {
		t.Errorf("first week = %s, want 2026-07-27", weeks[0])
	}
	if last := weeks[len(weeks)-1]; last != "2026-08-31" {
		t.Errorf("last week = %s, want 2026-08-31", last)
	}
	if len(weeks) != 6 {
		t.Errorf("week count = %d, want 6 (%v)", len(weeks), weeks)
	}
}

func TestLineSetRemovesDuplicateLines(t *testing.T) {
	set := newLineSet()
	set.add("• API 설계 완료\n• 스키마 정의")
	set.add("- api 설계 완료") // same line, different marker and case
	set.add("API설계 완료")    // same line, whitespace removed
	set.add("성능 시험 착수")
	if set.dropped != 2 {
		t.Errorf("dropped = %d, want 2", set.dropped)
	}
	rendered := set.render()
	want := "• API 설계 완료\n• 스키마 정의\n• 성능 시험 착수"
	if rendered != want {
		t.Errorf("render() = %q, want %q", rendered, want)
	}
}

func TestLineSetRendersSingleLineWithoutMarker(t *testing.T) {
	set := newLineSet()
	set.add("단일 실적")
	if got := set.render(); got != "단일 실적" {
		t.Errorf("render() = %q, want %q", got, "단일 실적")
	}
	if got := newLineSet().render(); got != "" {
		t.Errorf("empty render() = %q, want empty", got)
	}
}

func TestTitleSimilarity(t *testing.T) {
	if score := titleSimilarity("AI 게이트웨이 PoC", "AI 게이트웨이 PoC"); score != 100 {
		t.Errorf("identical titles scored %d, want 100", score)
	}
	if score := titleSimilarity("AI 게이트웨이 PoC", "결산 시스템 이관"); score != 0 {
		t.Errorf("unrelated titles scored %d, want 0", score)
	}
	// A contained title extended by one word is the same task renamed.
	if score := titleSimilarity("AI 게이트웨이 PoC", "AI 게이트웨이 PoC 검증"); score != 100 {
		t.Errorf("renamed title scored %d, want 100", score)
	}
	// A short generic title must not absorb a much longer, different one.
	if score := titleSimilarity("운영 지원", "운영 지원 시스템 신규 구축 착수"); score >= 80 {
		t.Errorf("generic title scored %d against a longer title, want below 80", score)
	}
	// Sharing a prefix is not enough when the tails differ.
	if score := titleSimilarity("AI 게이트웨이 구축", "AI 게이트웨이 폐기"); score >= 80 {
		t.Errorf("divergent titles scored %d, want below 80", score)
	}
	// A single character is often the only distinguisher between real tasks and
	// must not be discarded during tokenization.
	if score := titleSimilarity("서버 A 점검", "서버 B 점검"); score >= 80 {
		t.Errorf("titles differing only by one letter scored %d, want below 80", score)
	}
	if score := titleSimilarity("업무 항목 1", "업무 항목 2"); score >= 80 {
		t.Errorf("numbered titles scored %d, want below 80", score)
	}
}

func TestAggregateKeepsNumberedTasksApart(t *testing.T) {
	entries := []sourceEntry{
		entry("2026-08-03", "점검", "서버 A 점검", "완료", "", "", 100),
		entry("2026-08-03", "점검", "서버 B 점검", "진행", "", "", 40),
		entry("2026-08-03", "운영", "업무 항목 1", "a", "", "", 10),
		entry("2026-08-03", "운영", "업무 항목 2", "b", "", "", 20),
	}
	if items := aggregateRollupItems(entries, defaultRollupConfig()); len(items) != 4 {
		t.Fatalf("item count = %d, want 4 distinct tasks: %+v", len(items), items)
	}
}

func entry(week, category, title, current, next, issue string, progress int) sourceEntry {
	return sourceEntry{
		ReportID: 1, UserID: 7, DisplayName: "장현경", WeekStart: week, Status: "CLOSED",
		Category: category, Title: title, CurrentResult: current, NextPlan: next, Issue: issue, Progress: progress,
	}
}

func TestAggregateMergesSameTaskAcrossWeeks(t *testing.T) {
	entries := []sourceEntry{
		entry("2026-08-03", "플랫폼", "AI 게이트웨이 구축", "설계 초안 작성", "PoC 환경 구성", "", 20),
		entry("2026-08-10", "플랫폼", "AI 게이트웨이 구축", "PoC 환경 구성\n설계 초안 작성", "성능 시험", "GPU 자원 부족", 50),
		entry("2026-08-17", "플랫폼", "AI 게이트웨이 구축", "성능 시험 완료", "", "GPU 자원 부족", 100),
	}
	items := aggregateRollupItems(entries, defaultRollupConfig())
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	item := items[0]
	if item.WeekCount != 3 {
		t.Errorf("weekCount = %d, want 3", item.WeekCount)
	}
	if item.Progress != 100 || !item.Completed {
		t.Errorf("progress = %d completed = %v, want 100/true", item.Progress, item.Completed)
	}
	if item.StartProgress != 20 {
		t.Errorf("startProgress = %d, want 20", item.StartProgress)
	}
	// "설계 초안 작성" repeated in week two must collapse.
	if strings.Count(item.CurrentResult, "설계 초안 작성") != 1 {
		t.Errorf("currentResult repeated a line: %q", item.CurrentResult)
	}
	// "PoC 환경 구성" was planned then delivered, so it is not an open plan.
	if strings.Contains(item.NextPlan, "PoC 환경 구성") {
		t.Errorf("delivered plan survived in nextPlan: %q", item.NextPlan)
	}
	// The same issue reported twice collapses to one line but counts two weeks.
	if strings.Count(item.Issue, "GPU 자원 부족") != 1 {
		t.Errorf("issue repeated: %q", item.Issue)
	}
	// The issue history is kept, but delivered work is not an open risk.
	if item.IssueWeeks != 2 {
		t.Errorf("issueWeeks = %d, want 2", item.IssueWeeks)
	}
	if item.AtRisk {
		t.Error("a completed task must not be reported as an open risk")
	}
	if item.DuplicatesCut == 0 {
		t.Error("duplicatesCut = 0, want the merged duplicate lines to be counted")
	}
}

func TestPlanDroppedWhenResultSupersedesIt(t *testing.T) {
	entries := []sourceEntry{
		entry("2026-08-03", "플랫폼", "게이트웨이", "환경 구성", "성능 시험", "", 40),
		entry("2026-08-10", "플랫폼", "게이트웨이", "성능 시험 1차 완료", "전사 확산", "", 100),
	}
	items := aggregateRollupItems(entries, defaultRollupConfig())
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	// "성능 시험" was planned and the result line extends it, so it is delivered.
	if strings.Contains(items[0].NextPlan, "성능 시험") {
		t.Errorf("superseded plan survived: %q", items[0].NextPlan)
	}
	// "전사 확산" is genuinely still ahead.
	if !strings.Contains(items[0].NextPlan, "전사 확산") {
		t.Errorf("open plan was dropped: %q", items[0].NextPlan)
	}
}

func TestCoversRequiresExactMatchForShortLines(t *testing.T) {
	set := newLineSet()
	set.add("배포 자동화 파이프라인 구축")
	// A short plan must not be swallowed just because its letters appear inside
	// a much longer result line.
	if set.covers("배포") {
		t.Error("short plan was absorbed by a longer result line")
	}
	if !set.covers("배포 자동화 파이프라인") {
		t.Error("a contained longer plan should count as delivered")
	}
}

func TestAggregateFuzzyMergesRenamedTask(t *testing.T) {
	entries := []sourceEntry{
		entry("2026-08-03", "플랫폼", "AI 게이트웨이 PoC", "환경 구성", "", "", 30),
		entry("2026-08-10", "플랫폼", "AI 게이트웨이 PoC 검증", "성능 측정", "", "", 60),
	}
	items := aggregateRollupItems(entries, defaultRollupConfig())
	if len(items) != 1 {
		t.Fatalf("fuzzy merge produced %d items, want 1: %+v", len(items), items)
	}
	if items[0].Title != "AI 게이트웨이 PoC 검증" {
		t.Errorf("title = %q, want the latest wording", items[0].Title)
	}
	if len(items[0].MergedTitles) != 2 {
		t.Errorf("mergedTitles = %v, want both source titles", items[0].MergedTitles)
	}

	// Turning the threshold off must keep the two spellings separate.
	strict := defaultRollupConfig()
	strict.MergeSimilarity = 0
	if got := aggregateRollupItems(entries, strict); len(got) != 2 {
		t.Errorf("with fuzzy merge disabled item count = %d, want 2", len(got))
	}
}

func TestAggregateKeepsDistinctTasksSeparate(t *testing.T) {
	entries := []sourceEntry{
		entry("2026-08-03", "플랫폼", "AI 게이트웨이 구축", "설계", "", "", 20),
		entry("2026-08-03", "결산", "월결산 자동화", "요건 정의", "", "", 40),
	}
	if items := aggregateRollupItems(entries, defaultRollupConfig()); len(items) != 2 {
		t.Fatalf("item count = %d, want 2", len(items))
	}
}

func TestIsStalledDetectsFrozenProgress(t *testing.T) {
	frozen := []rollupItemWeek{{WeekStart: "2026-08-03", Progress: 40}, {WeekStart: "2026-08-10", Progress: 60}, {WeekStart: "2026-08-17", Progress: 60}}
	if !isStalled(frozen, 2) {
		t.Error("expected frozen progress to be stalled")
	}
	moving := []rollupItemWeek{{WeekStart: "2026-08-03", Progress: 40}, {WeekStart: "2026-08-10", Progress: 60}, {WeekStart: "2026-08-17", Progress: 80}}
	if isStalled(moving, 2) {
		t.Error("expected moving progress not to be stalled")
	}
	done := []rollupItemWeek{{WeekStart: "2026-08-10", Progress: 100}, {WeekStart: "2026-08-17", Progress: 100}}
	if isStalled(done, 2) {
		t.Error("completed work must never be reported as stalled")
	}
	if isStalled(frozen[:1], 2) {
		t.Error("a single week cannot be stalled")
	}
}

func TestBuildRollupInsightsAndHighlights(t *testing.T) {
	period, err := resolvePeriod(periodMonth, "2026-08", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	expected := expectedWeekStarts(period, "MONDAY")
	entries := []sourceEntry{
		entry("2026-08-03", "플랫폼", "AI 게이트웨이 구축", "설계", "PoC", "GPU 부족", 40),
		entry("2026-08-10", "플랫폼", "AI 게이트웨이 구축", "PoC", "성능 시험", "GPU 부족", 40),
		entry("2026-08-17", "플랫폼", "AI 게이트웨이 구축", "성능 시험", "확산", "GPU 부족", 40),
		entry("2026-08-03", "결산", "월결산 자동화", "요건 정의", "", "", 100),
	}
	reports := []reportListItem{
		{ID: 1, UserID: 7, DisplayName: "장현경", WeekStart: "2026-08-03"},
		{ID: 2, UserID: 7, DisplayName: "장현경", WeekStart: "2026-08-10"},
		{ID: 3, UserID: 7, DisplayName: "장현경", WeekStart: "2026-08-17"},
	}
	view := buildRollup(period, scopeSelf, "장현경", entries, reports, expected, defaultRollupConfig())

	if view.Insights.TotalItems != 2 {
		t.Fatalf("totalItems = %d, want 2", view.Insights.TotalItems)
	}
	if view.Insights.SourceItems != 4 {
		t.Errorf("sourceItems = %d, want 4", view.Insights.SourceItems)
	}
	if view.Insights.CompletedItems != 1 || view.Insights.CompletionRate != 50 {
		t.Errorf("completed = %d rate = %.1f, want 1/50.0", view.Insights.CompletedItems, view.Insights.CompletionRate)
	}
	if view.Insights.StalledItems != 1 {
		t.Errorf("stalledItems = %d, want 1", view.Insights.StalledItems)
	}
	if view.Insights.PersistentIssues != 1 {
		t.Errorf("persistentIssues = %d, want 1", view.Insights.PersistentIssues)
	}
	if view.Insights.ReportedWeeks != 3 || view.Insights.ExpectedWeeks != len(expected) {
		t.Errorf("coverage = %d/%d, want 3/%d", view.Insights.ReportedWeeks, view.Insights.ExpectedWeeks, len(expected))
	}
	// The at-risk item must sort ahead of the completed one.
	if view.Items[0].Title != "AI 게이트웨이 구축" {
		t.Errorf("first item = %q, want the at-risk task first", view.Items[0].Title)
	}
	if len(view.Trend) != len(expected) {
		t.Errorf("trend length = %d, want %d", len(view.Trend), len(expected))
	}
	if len(view.Categories) != 2 {
		t.Errorf("categories = %d, want 2", len(view.Categories))
	}
	if len(view.Contributors) != 1 || view.Contributors[0].Items != 2 {
		t.Errorf("contributors = %+v, want one contributor with 2 items", view.Contributors)
	}

	var risk, watch bool
	for _, highlight := range view.Highlights {
		if highlight.Severity == "RISK" {
			risk = true
		}
		if highlight.Severity == "WATCH" {
			watch = true
		}
	}
	if !risk || !watch {
		t.Errorf("highlights missing severities: %+v", view.Highlights)
	}
	if view.Summary == "" {
		t.Error("summary must never be empty")
	}
}

func TestBuildRollupHandlesEmptyPeriod(t *testing.T) {
	period, err := resolvePeriod(periodQuarter, "2026-Q1", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	view := buildRollup(period, scopeTeam, "AI엔지니어링", nil, nil, expectedWeekStarts(period, "MONDAY"), defaultRollupConfig())
	if view.Insights.TotalItems != 0 {
		t.Errorf("totalItems = %d, want 0", view.Insights.TotalItems)
	}
	if len(view.Highlights) != 1 || view.Highlights[0].Category != "COVERAGE" {
		t.Errorf("highlights = %+v, want a single coverage note", view.Highlights)
	}
	if view.Items == nil || view.Categories == nil || view.Trend == nil {
		t.Error("empty rollup must still serialize as empty arrays, not null")
	}
}

func TestAggregateMergesSameTaskAcrossOwners(t *testing.T) {
	first := entry("2026-08-03", "플랫폼", "공통 인증 이관", "설계", "", "", 30)
	second := entry("2026-08-03", "플랫폼", "공통 인증 이관", "구현", "", "", 50)
	second.UserID = 9
	second.DisplayName = "김민수"
	items := aggregateRollupItems([]sourceEntry{first, second}, defaultRollupConfig())
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	if len(items[0].Owners) != 2 {
		t.Errorf("owners = %v, want both contributors", items[0].Owners)
	}
	// Two entries in the same week must not inflate the week count.
	if items[0].WeekCount != 1 {
		t.Errorf("weekCount = %d, want 1", items[0].WeekCount)
	}
	if items[0].Progress != 50 {
		t.Errorf("progress = %d, want the furthest progress 50", items[0].Progress)
	}
}

func workEntry(week, title string, workItemID int64, progress int) sourceEntry {
	item := entry(week, "플랫폼", title, "진행", "", "", progress)
	item.WorkItemID = &workItemID
	return item
}

// The period rollup used to re-guess task identity from titles, which meant it
// could re-merge tasks their owner had just split and could disagree with every
// other screen about what counts as one task.
func TestAggregateGroupsByStoredWorkItemIdentity(t *testing.T) {
	// Same title, two identities: the owner split this task apart by hand.
	split := aggregateRollupItems([]sourceEntry{
		workEntry("2026-08-03", "통합 인증 개선", 1, 30),
		workEntry("2026-08-10", "통합 인증 개선", 2, 40),
	}, defaultRollupConfig())
	if len(split) != 2 {
		t.Fatalf("a hand split was re-merged: got %d items, want 2", len(split))
	}

	// Different titles, one identity: the owner merged them.
	merged := aggregateRollupItems([]sourceEntry{
		workEntry("2026-08-03", "통합 인증 개선", 5, 30),
		workEntry("2026-08-10", "인증 연동 2단계", 5, 60),
	}, defaultRollupConfig())
	if len(merged) != 1 {
		t.Fatalf("a hand merge was ignored: got %d items, want 1", len(merged))
	}
	if merged[0].Progress != 60 {
		t.Fatalf("merged progress=%d want=60", merged[0].Progress)
	}

	// Titles similar enough for the old fuzzy rule, but distinct identities.
	// The stored answer wins over the guess.
	contained := aggregateRollupItems([]sourceEntry{
		workEntry("2026-08-03", "AI 게이트웨이 PoC", 8, 40),
		workEntry("2026-08-10", "AI 게이트웨이 PoC 검증", 9, 20),
	}, defaultRollupConfig())
	if len(contained) != 2 {
		t.Fatalf("similar titles with different identities were merged: got %d, want 2", len(contained))
	}
}

// Rows with no stored identity still fall back to the title rules, so a corpus
// that predates work items keeps aggregating the way it always did.
func TestAggregateFallsBackToTitlesWithoutWorkItems(t *testing.T) {
	items := aggregateRollupItems([]sourceEntry{
		entry("2026-08-03", "플랫폼", "AI 게이트웨이 PoC", "설계", "", "", 20),
		entry("2026-08-10", "플랫폼", "AI 게이트웨이 PoC 검증", "시험", "", "", 50),
	}, defaultRollupConfig())
	if len(items) != 1 {
		t.Fatalf("title fallback stopped merging: got %d items, want 1", len(items))
	}
}

// The period report exists to show one organisation-wide task once, and work
// item identity is per owner, so a guess is still allowed to reach across
// people even when both sides carry a stored identity.
func TestAggregateStillMergesSimilarTitlesAcrossOwners(t *testing.T) {
	first := workEntry("2026-08-03", "AI 게이트웨이 구축", 11, 30)
	second := workEntry("2026-08-10", "AI 게이트웨이 구축 지원", 22, 50)
	second.UserID = 8
	second.DisplayName = "김담당"
	items := aggregateRollupItems([]sourceEntry{first, second}, defaultRollupConfig())
	if len(items) != 1 {
		t.Fatalf("two people's version of one task split apart: got %d items, want 1", len(items))
	}
	if len(items[0].Owners) != 2 {
		t.Fatalf("owners=%v want two contributors", items[0].Owners)
	}
}

// The same two titles under one person stay apart, because that owner has a
// stored answer and a tool to change it.
func TestAggregateDoesNotGuessWithinOneOwner(t *testing.T) {
	items := aggregateRollupItems([]sourceEntry{
		workEntry("2026-08-03", "AI 게이트웨이 구축", 11, 30),
		workEntry("2026-08-10", "AI 게이트웨이 구축 지원", 22, 50),
	}, defaultRollupConfig())
	if len(items) != 2 {
		t.Fatalf("one owner's two tasks were guessed into one: got %d items, want 2", len(items))
	}
}

// The weekly series travels only for the rows a chart can draw. On a year-wide
// organisation view it was 522 KB of an 885 KB response, all of it per-week
// text for tasks the timeline never plots — the table below reads none of it.
func TestRollupSendsTheWeeklySeriesOnlyForTheRowsThatAreDrawn(t *testing.T) {
	items := make([]rollupItem, rollupTimelineItems+5)
	for index := range items {
		items[index] = rollupItem{
			Key: fmt.Sprintf("k%d", index), Title: fmt.Sprintf("업무 %d", index),
			Weeks: []rollupItemWeek{{WeekStart: "2026-06-01", Progress: 10}, {WeekStart: "2026-06-08", Progress: 20}},
		}
	}
	view := rollupView{Items: items, TimelineItems: rollupTimelineItems}
	for index := range view.Items {
		if index >= rollupTimelineItems {
			view.Items[index].Weeks = nil
		}
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	carrying := 0
	for _, item := range view.Items {
		if len(item.Weeks) > 0 {
			carrying++
		}
	}
	if carrying != rollupTimelineItems {
		t.Errorf("%d rows carry a series, want %d", carrying, rollupTimelineItems)
	}
	if len(view.Items) != rollupTimelineItems+5 {
		t.Errorf("rows were dropped, not trimmed: %d", len(view.Items))
	}
	// The count has to travel too: a screen that guessed how many rows carry
	// data would plot empty rows the moment the two disagreed.
	if !strings.Contains(string(body), fmt.Sprintf(`"timelineItems":%d`, rollupTimelineItems)) {
		t.Error("the response does not say how many rows carry a series")
	}
}
