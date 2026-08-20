package app

import "strings"
import "testing"

func qualityHistory(title string, weeks ...workItemWeek) workItemView {
	return workItemView{ID: 1, Title: title, Weeks: weeks, ReportedWeeks: len(weeks)}
}

func rules(report qualityReport) []string {
	found := []string{}
	for _, finding := range report.Findings {
		found = append(found, finding.Rule)
	}
	return found
}

func TestCheckReportQuality(t *testing.T) {
	cfg := defaultRollupConfig()
	const now = "2026-08-17"
	cases := []struct {
		name    string
		draft   reportItem
		history []workItemWeek
		want    []string
	}{
		{
			name:    "progress reported lower than last week",
			draft:   reportItem{Title: "인증 연동", CurrentResult: "진행", NextPlan: "다음", Progress: 40},
			history: []workItemWeek{{WeekStart: "2026-08-10", Progress: 70, NextPlan: "이전"}},
			want:    []string{"PROGRESS_REGRESSED"},
		},
		{
			name:  "same plan a third week with no movement",
			draft: reportItem{Title: "인증 연동", CurrentResult: "계속", NextPlan: "서버 연동 구현", Progress: 40},
			history: []workItemWeek{
				{WeekStart: "2026-08-03", Progress: 40, NextPlan: "서버 연동 구현"},
				{WeekStart: "2026-08-10", Progress: 40, NextPlan: "서버 연동 구현"},
			},
			want: []string{"PLAN_REPEATED"},
		},
		{
			// The plan repeats but the work moved, which is an ordinary task
			// spanning several weeks rather than a report being copied forward.
			name:  "same plan but progress moved",
			draft: reportItem{Title: "인증 연동", CurrentResult: "계속", NextPlan: "서버 연동 구현", Progress: 60},
			history: []workItemWeek{
				{WeekStart: "2026-08-03", Progress: 30, NextPlan: "서버 연동 구현"},
				{WeekStart: "2026-08-10", Progress: 40, NextPlan: "서버 연동 구현"},
			},
			want: []string{},
		},
		{
			name:    "planned last week, nothing reported",
			draft:   reportItem{Title: "인증 연동", CurrentResult: "  ", NextPlan: "다음", Progress: 40},
			history: []workItemWeek{{WeekStart: "2026-08-10", Progress: 40, NextPlan: "서버 연동 구현"}},
			want:    []string{"PLAN_WITHOUT_RESULT"},
		},
		{
			name:  "same issue for a third week",
			draft: reportItem{Title: "인증 연동", CurrentResult: "진행", NextPlan: "계속", Issue: "방화벽 정책 대기", Progress: 50},
			history: []workItemWeek{
				{WeekStart: "2026-08-03", Progress: 40, NextPlan: "이전", Issue: "방화벽 정책 대기"},
				{WeekStart: "2026-08-10", Progress: 45, NextPlan: "이전", Issue: "방화벽 정책 대기"},
			},
			want: []string{"ISSUE_PERSISTED"},
		},
		{
			// Nothing to compare against, so nothing to say. A first report must
			// never open with a list of complaints.
			name:    "brand new task",
			draft:   reportItem{Title: "새 업무", CurrentResult: "착수", NextPlan: "다음", Progress: 10},
			history: nil,
			want:    []string{},
		},
		{
			// The saved copy of the week under check is not evidence about the
			// draft of that same week.
			name:    "this week already saved once",
			draft:   reportItem{Title: "인증 연동", CurrentResult: "진행", NextPlan: "다음", Progress: 60},
			history: []workItemWeek{{WeekStart: now, Progress: 90, NextPlan: "다음"}},
			want:    []string{},
		},
	}
	for _, item := range cases {
		history := []workItemView{}
		if item.history != nil {
			history = append(history, qualityHistory("인증 연동", item.history...))
		}
		report := checkReportQuality(now, []reportItem{item.draft}, history, cfg)
		got := rules(report)
		if strings.Join(got, ",") != strings.Join(item.want, ",") {
			t.Errorf("%s: got=%v want=%v", item.name, got, item.want)
		}
	}
}

func TestCheckReportQualityMatchesTitlesByTheSameRuleAsCarryOver(t *testing.T) {
	// Spacing must not decide whether an author gets checked at all.
	report := checkReportQuality("2026-08-17",
		[]reportItem{{Title: "전표검증 자동화", CurrentResult: "진행", Progress: 20}},
		[]workItemView{qualityHistory("전표 검증 자동화", workItemWeek{WeekStart: "2026-08-10", Progress: 60})},
		defaultRollupConfig())
	if got := rules(report); len(got) != 1 || got[0] != "PROGRESS_REGRESSED" {
		t.Fatalf("got=%v want=[PROGRESS_REGRESSED]", got)
	}
}

func TestCheckReportQualitySaysSoWhenAnIssueIsAlreadyEscalated(t *testing.T) {
	history := []workItemView{qualityHistory("인증 연동",
		workItemWeek{WeekStart: "2026-08-03", Progress: 40, Issue: "방화벽 정책 대기"},
		workItemWeek{WeekStart: "2026-08-10", Progress: 45, Issue: "방화벽 정책 대기"})}
	draft := reportItem{Title: "인증 연동", CurrentResult: "진행", Progress: 50,
		Issue: "방화벽 정책 대기", ManagementAsk: "보안팀 정책 예외 승인 요청"}
	report := checkReportQuality("2026-08-17", []reportItem{draft}, history, defaultRollupConfig())
	if len(report.Findings) != 1 {
		t.Fatalf("findings=%d want=1", len(report.Findings))
	}
	if !strings.Contains(report.Findings[0].Message, "회신 여부") {
		t.Fatalf("an escalated issue should not be told to escalate: %q", report.Findings[0].Message)
	}
}
