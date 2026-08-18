package app

import (
	"strings"
	"testing"
)

func org(id int64) *int64 { return &id }

func workItem(id int64, title string, userID int64, organizationID *int64, weeks []workItemWeek) workItemView {
	item := workItemView{ID: id, Title: title, UserID: userID, OrganizationID: organizationID,
		OrganizationName: "조직", DisplayName: "담당자", Weeks: weeks}
	summarizeWorkItem(&item, defaultRollupConfig())
	return item
}

// Two teams building the same thing at the same time is the finding worth
// having; the same person splitting their own work is not.
func TestLinkWorkItemsFindsCrossTeamDuplicates(t *testing.T) {
	items := []workItemView{
		workItem(1, "AI 게이트웨이 구축", 10, org(1), []workItemWeek{
			week("2026-08-03", 40, "인증 연동", "부하 시험", "", ""),
			week("2026-08-10", 50, "인증 연동", "부하 시험", "", "")}),
		workItem(2, "AI 게이트웨이 구축", 20, org(2), []workItemWeek{
			week("2026-08-03", 30, "설계", "구현", "", ""),
			week("2026-08-10", 35, "설계", "구현", "", "")}),
	}
	links := linkWorkItems(items)
	if len(links) != 1 {
		t.Fatalf("want one link, got %d", len(links))
	}
	if !links[0].Duplicate {
		t.Errorf("identical open work in two organizations should be a duplicate candidate: %+v", links[0])
	}
	if links[0].OverlapWeeks != 2 {
		t.Errorf("overlap weeks = %d, want 2", links[0].OverlapWeeks)
	}
	if links[0].Reason == "" {
		t.Error("a link must carry the reason it was surfaced")
	}
}

func TestLinkWorkItemsIgnoresSameOwner(t *testing.T) {
	items := []workItemView{
		workItem(1, "결산 자동화 구축", 10, org(1), []workItemWeek{week("2026-08-03", 10, "a", "", "", "")}),
		workItem(2, "결산 자동화 구축", 10, org(1), []workItemWeek{week("2026-08-03", 20, "b", "", "", "")}),
	}
	if links := linkWorkItems(items); len(links) != 0 {
		t.Errorf("one person's own two tasks are not a cross-team finding: %+v", links)
	}
}

// Boilerplate agreement is not a finding. Without this, every team's "주간 업무
// 보고 정리" links to every other team's.
func TestLinkWorkItemsIgnoresBoilerplateOnlyOverlap(t *testing.T) {
	items := []workItemView{
		workItem(1, "주간 업무 보고", 10, org(1), []workItemWeek{week("2026-08-03", 10, "a", "", "", "")}),
		workItem(2, "주간 업무 계획", 20, org(2), []workItemWeek{week("2026-08-03", 10, "b", "", "", "")}),
	}
	for _, link := range linkWorkItems(items) {
		t.Errorf("titles sharing only boilerplate must not link: %+v", link)
	}
}

// Completed work is history, not duplicated investment.
func TestCompletedWorkIsNotADuplicateCandidate(t *testing.T) {
	items := []workItemView{
		workItem(1, "전표 검증 자동화", 10, org(1), []workItemWeek{week("2026-08-03", 100, "완료", "", "", "")}),
		workItem(2, "전표 검증 자동화", 20, org(2), []workItemWeek{week("2026-08-03", 40, "진행", "", "", "")}),
	}
	links := linkWorkItems(items)
	if len(links) != 1 {
		t.Fatalf("want one link, got %d", len(links))
	}
	if links[0].Duplicate {
		t.Error("a finished task is not duplicated investment")
	}
}

func TestCollaborationEdgesGroupByOrganizationPair(t *testing.T) {
	items := []workItemView{
		workItem(1, "AI 게이트웨이 구축", 10, org(1), []workItemWeek{week("2026-08-03", 10, "a", "", "", "")}),
		workItem(2, "AI 게이트웨이 연동", 20, org(2), []workItemWeek{week("2026-08-03", 10, "b", "", "", "")}),
		workItem(3, "AI 게이트웨이 검증", 30, org(2), []workItemWeek{week("2026-08-03", 10, "c", "", "", "")}),
	}
	edges := collaborationEdges(linkWorkItems(items))
	if len(edges) != 1 {
		t.Fatalf("two organizations connected by one subject is one edge, got %d: %+v", len(edges), edges)
	}
	if edges[0].SharedWork != 2 {
		t.Errorf("shared work = %d, want 2", edges[0].SharedWork)
	}
	if len(edges[0].Topics) == 0 || edges[0].Topics[0] != "게이트웨이" {
		t.Errorf("the connecting subject should lead the topics, got %v", edges[0].Topics)
	}
}

// Routine operation is recognised by behaviour, not by keywords: reported every
// week for a long time, never advancing towards completion.
func TestRecurringWorkIsSteadyAndDoesNotProgress(t *testing.T) {
	routine := workItem(1, "결산 마감 지원", 10, org(1), []workItemWeek{
		week("2026-07-06", 100, "마감 지원", "다음 마감", "", ""),
		week("2026-07-13", 100, "마감 지원", "다음 마감", "", ""),
		week("2026-07-20", 100, "마감 지원", "다음 마감", "", ""),
		week("2026-07-27", 100, "마감 지원", "다음 마감", "", ""),
		week("2026-08-03", 100, "마감 지원", "다음 마감", "", "")})
	project := workItem(2, "신규 포털 구축", 20, org(1), []workItemWeek{
		week("2026-07-06", 10, "설계", "", "", ""),
		week("2026-07-13", 30, "구현", "", "", ""),
		week("2026-07-20", 55, "구현", "", "", ""),
		week("2026-07-27", 80, "시험", "", "", ""),
		week("2026-08-03", 95, "안정화", "", "", "")})
	found := recurringWorkItems([]workItemView{routine, project})
	if len(found) != 1 {
		t.Fatalf("want only the routine task, got %d: %+v", len(found), found)
	}
	if found[0].WorkItemID != 1 {
		t.Errorf("classified the project as routine: %+v", found[0])
	}
	if !strings.Contains(found[0].Reason, "%") {
		t.Errorf("the classification must state its cadence, got %q", found[0].Reason)
	}
}

// A burst of reports in one month is a short project, not a weekly routine.
func TestIrregularCadenceIsNotRoutine(t *testing.T) {
	bursty := workItem(1, "서버 이관 지원", 10, org(1), []workItemWeek{
		week("2026-05-04", 20, "a", "", "", ""),
		week("2026-05-11", 20, "b", "", "", ""),
		week("2026-07-27", 25, "c", "", "", ""),
		week("2026-08-03", 25, "d", "", "", "")})
	if found := recurringWorkItems([]workItemView{bursty}); len(found) != 0 {
		t.Errorf("gaps of months are not a weekly routine: %+v", found)
	}
}
