package app

import (
	"fmt"
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
	links := allLinks(items)
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
	if links := allLinks(items); len(links) != 0 {
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
	for _, link := range allLinks(items) {
		t.Errorf("titles sharing only boilerplate must not link: %+v", link)
	}
}

// Completed work is history, not duplicated investment.
func TestCompletedWorkIsNotADuplicateCandidate(t *testing.T) {
	items := []workItemView{
		workItem(1, "전표 검증 자동화", 10, org(1), []workItemWeek{week("2026-08-03", 100, "완료", "", "", "")}),
		workItem(2, "전표 검증 자동화", 20, org(2), []workItemWeek{week("2026-08-03", 40, "진행", "", "", "")}),
	}
	links := allLinks(items)
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
	edges := linkWorkItems(items, insightLinkLimit).Collaboration
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

// allLinks is the flattened ranking, for tests that care about the set rather
// than the split between duplicates and merely similar pairs.
func allLinks(items []workItemView) []workLink {
	graph := linkWorkItems(items, insightLinkLimit)
	return append(append([]workLink{}, graph.Duplicates...), graph.Similar...)
}

// The response used to carry every qualifying pair. On 1,805 work items that
// was 1,606,500 links and a 911MB body, and the screen rendered all of them.
func TestLinkWorkItemsCapsWhatItReturnsAndReportsTheTotal(t *testing.T) {
	items := []workItemView{}
	for index := 0; index < 60; index++ {
		// One owner each, so every pair is eligible, with a shared distinctive
		// term so nothing is filtered out as boilerplate.
		items = append(items, workItem(int64(index+1), "결산 자동화 구축", int64(index+1), org(int64(index%2+1)),
			[]workItemWeek{week("2026-08-03", 10, "진행", "", "", "")}))
	}
	limit := 25
	graph := linkWorkItems(items, limit)
	duplicates, similar := graph.Duplicates, graph.Similar
	duplicateTotal, similarTotal := graph.DuplicateTotal, graph.SimilarTotal
	if len(similar)+len(duplicates) > 2*limit {
		t.Fatalf("returned %d links for a limit of %d", len(similar)+len(duplicates), limit)
	}
	total := duplicateTotal + similarTotal
	pairs := len(items) * (len(items) - 1) / 2
	if total != pairs {
		t.Fatalf("total=%d want=%d: the count must cover every qualifying pair, not just the ones returned", total, pairs)
	}
	if len(duplicates) > limit || len(similar) > limit {
		t.Fatalf("a list exceeded the limit: duplicates=%d similar=%d limit=%d", len(duplicates), len(similar), limit)
	}
	// Ranked, so the cap keeps the pairs a reader would want rather than
	// whichever ones happened to be compared first.
	for index := 1; index < len(similar); index++ {
		if linkRank(similar[index-1]) < linkRank(similar[index]) {
			t.Fatalf("similar links are not ordered strongest first at %d", index)
		}
	}
}

// The collaboration map is aggregated over every qualifying pair. Building it
// from the ranked survivors would drop whole organisation pairs from a screen
// that presents itself as the complete picture.
func TestCollaborationSurvivesTheLinkCap(t *testing.T) {
	items := []workItemView{}
	for index := 0; index < 40; index++ {
		// Twenty owners in org 1 all working on one subject, twenty in org 2 on
		// another, plus one pair that connects a third organisation.
		subject := "결산 자동화 구축"
		organization := int64(1)
		if index >= 20 {
			organization = 2
		}
		items = append(items, workItem(int64(index+1), subject, int64(index+1), org(organization),
			[]workItemWeek{week("2026-08-03", 10, "진행", "", "", "")}))
	}
	items = append(items,
		workItem(900, "인사 평가 개편", 900, org(3), []workItemWeek{week("2026-08-03", 10, "진행", "", "", "")}),
		workItem(901, "인사 평가 개편", 901, org(4), []workItemWeek{week("2026-08-03", 10, "진행", "", "", "")}))

	limit := 5
	graph := linkWorkItems(items, limit)
	if len(graph.Similar) > limit {
		t.Fatalf("the cap did not apply: %d links", len(graph.Similar))
	}
	// The fixture gives every organisation the same display name, so the map
	// collapses to one edge. What matters is the count behind it: built from
	// the capped list it could never exceed the cap.
	shared := 0
	for _, edge := range graph.Collaboration {
		shared += edge.SharedWork
	}
	if shared <= limit {
		t.Fatalf("collaboration counted %d links for a cap of %d; it was built from the capped list", shared, limit)
	}
	// Twenty owners in one organisation against twenty in another is 400 pairs,
	// plus the single pair connecting the other two organisations. Same
	// organisation pairs are not collaboration and are not counted.
	if shared != 401 {
		t.Fatalf("collaboration counted %d cross-organisation links, want 401", shared)
	}
}

// Every duplicate has to reach the digest, which scores tasks one at a time.
func TestDuplicateByItemCoversMoreThanTheCappedList(t *testing.T) {
	items := []workItemView{}
	for index := 0; index < 30; index++ {
		items = append(items, workItem(int64(index+1), "전표 검증 자동화", int64(index+1), org(int64(index%2+1)),
			[]workItemWeek{week("2026-08-03", 40, "진행", "", "", "")}))
	}
	graph := linkWorkItems(items, 3)
	if len(graph.Duplicates) > 3 {
		t.Fatalf("the cap did not apply: %d duplicates", len(graph.Duplicates))
	}
	if len(graph.DuplicateByItem) <= len(graph.Duplicates)*2 {
		t.Fatalf("only the capped pairs reached the per-item map: %d entries", len(graph.DuplicateByItem))
	}
	for id, link := range graph.DuplicateByItem {
		if link.Left.WorkItemID != id {
			t.Fatalf("entry for %d names %d on the left; it must be stated from that task's side", id, link.Left.WorkItemID)
		}
	}
}

// An exact title match scores the same for every pair, so the cap is decided
// entirely by the tie-break. Without one it keeps whatever the scan reached
// first, which is the lowest ids, which is the oldest work.
func TestTiedLinksKeepTheMostRecentPairs(t *testing.T) {
	const count = 30
	items := []workItemView{}
	for index := 0; index < count; index++ {
		// The lowest id is the oldest work, and no two items share a week, so
		// every pair ties on both similarity and overlap.
		day := fmt.Sprintf("2026-08-%02d", index+1)
		items = append(items, workItem(int64(index+1), "전표 검증 자동화", int64(index+1), org(int64(index%2+1)),
			[]workItemWeek{week(day, 10, "진행", "", "", "")}))
	}

	graph := linkWorkItems(items, 4)
	if len(graph.Similar) == 0 {
		t.Fatal("no links survived")
	}
	for _, link := range graph.Similar {
		if link.Similarity != 100 || link.OverlapWeeks != 0 {
			t.Fatalf("the fixture is meant to tie: similarity=%d overlap=%d", link.Similarity, link.OverlapWeeks)
		}
		// The newest item is in every best pair. The scan reaches it last, so
		// without the tie-break none of these would have survived.
		if link.Left.WorkItemID != count && link.Right.WorkItemID != count {
			t.Fatalf("a tie kept an older pair over the newest work: %d and %d (%s, %s)",
				link.Left.WorkItemID, link.Right.WorkItemID, link.Left.LastWeek, link.Right.LastWeek)
		}
	}
}
