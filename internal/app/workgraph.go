package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The work graph answers questions that span owners and organizations: who else
// is doing something like this, which teams are working on the same thing, and
// which work is routine operation rather than a project.
//
// Everything here is derived from the weekly snapshots with deterministic
// rules. There is no model in the path, so it produces the same answer offline
// as it does with an AI Gateway configured, and the reason for every judgement
// can be shown to the person reading it.
//
// Nothing here is treated as fact. A matched pair is a candidate that a person
// confirms or ignores; the system never merges two organizations' work on its
// own.

const (
	// Two titles are related when half their meaningful words agree. Measured
	// against real report data: "AI 게이트웨이 구축" and "AI 게이트웨이 연동"
	// score 50, which is the loosest pair worth surfacing.
	relatedTitleSimilarity = 50
	// Duplicate investment is a stronger claim than similarity, so it needs a
	// closer match, two different organizations, and both sides still open.
	duplicateTitleSimilarity = 70
	// Work reported for at least this many weeks can be judged as routine.
	// Below it, a run of weeks is indistinguishable from a short project.
	recurringMinimumWeeks = 4
	// Routine work is reported on a regular cadence rather than in bursts.
	recurringCadencePercent = 60
	// Routine work does not advance towards completion the way a project does.
	recurringMaximumGain = 10
)

// workScope is the visibility filter applied to every work graph query. It
// mirrors the report permission rules exactly: a user sees their own work, a
// leader sees their organization subtree, an administrator sees everything.
type workScope struct {
	UserID         int64
	OrganizationID *int64
	Role           string
	// SelfOnly restricts the result to the caller's own work regardless of role.
	SelfOnly bool
}

func scopeForPrincipal(p *principal, selfOnly bool) workScope {
	return workScope{UserID: p.ID, OrganizationID: p.OrganizationID, Role: p.Role, SelfOnly: selfOnly}
}

// where builds the visibility predicate and its arguments starting at $start.
func (s workScope) where(start int) (string, []any) {
	args := []any{}
	if s.SelfOnly {
		args = append(args, s.UserID)
		return fmt.Sprintf(" AND w.user_id=$%d", start), args
	}
	switch s.Role {
	case "ADMIN":
		return "", args
	case "TEAM_LEADER", "ORG_MANAGER":
		if s.OrganizationID == nil {
			args = append(args, s.UserID)
			return fmt.Sprintf(" AND w.user_id=$%d", start), args
		}
		args = append(args, s.UserID, *s.OrganizationID)
		return fmt.Sprintf(` AND (w.user_id=$%d OR u.organization_id IN `, start) + orgSubtree(start+1) + `)`, args
	default:
		args = append(args, s.UserID)
		return fmt.Sprintf(" AND w.user_id=$%d", start), args
	}
}

// loadWorkItems reads every visible work item with its weekly history already
// summarised. Every feature in this file starts here, so they cannot disagree
// about what a task's history is.
//
// since is inclusive and may be empty to load the full history.
func (a *App) loadWorkItems(ctx context.Context, scope workScope, since string) ([]workItemView, error) {
	statement := `SELECT w.id, w.title, w.category, w.user_id, u.display_name, w.due_date,
			u.organization_id, coalesce(o.name,''),
			r.week_start, r.id, i.id, i.progress, i.current_result, i.next_plan, i.issue, i.management_ask
		FROM work_items w
		JOIN users u ON u.id = w.user_id
		LEFT JOIN organizations o ON o.id = u.organization_id
		JOIN report_items i ON i.work_item_id = w.id
		JOIN weekly_reports r ON r.id = i.report_id
		WHERE 1=1`
	predicate, args := scope.where(1)
	statement += predicate
	if since != "" {
		args = append(args, since)
		statement += fmt.Sprintf(" AND r.week_start >= $%d", len(args))
	}
	statement += " ORDER BY w.id, r.week_start, i.id"

	rows, err := a.db.Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	order := []int64{}
	byID := map[int64]*workItemView{}
	for rows.Next() {
		var id, userID, reportID, itemID int64
		var title, category, displayName, organizationName string
		var organizationID *int64
		var dueDate, week time.Time
		var duePointer *time.Time
		var progress int
		var current, next, issue, ask string
		if err := rows.Scan(&id, &title, &category, &userID, &displayName, &duePointer,
			&organizationID, &organizationName,
			&week, &reportID, &itemID, &progress, &current, &next, &issue, &ask); err != nil {
			return nil, err
		}
		item, exists := byID[id]
		if !exists {
			item = &workItemView{ID: id, Title: title, Category: category, UserID: userID,
				DisplayName: displayName, OrganizationID: organizationID,
				OrganizationName: organizationName, Weeks: []workItemWeek{}}
			if duePointer != nil {
				dueDate = *duePointer
				item.DueDate = dueDate.Format("2006-01-02")
			}
			byID[id] = item
			order = append(order, id)
		}
		item.Weeks = append(item.Weeks, workItemWeek{
			WeekStart: week.Format("2006-01-02"), ReportID: reportID, ItemIDs: []int64{itemID}, Progress: progress,
			CurrentResult: current, NextPlan: next, Issue: issue, ManagementAsk: ask,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cfg := a.rollupConfig(ctx)
	// The week the reader is in, so a task can be measured against now and not
	// only against itself.
	currentWeek := currentWeekStart(time.Now().In(a.serviceLocation(ctx)),
		a.setting(ctx, "workflow.week_start", "MONDAY")).Format("2006-01-02")
	result := make([]workItemView, 0, len(order))
	for _, id := range order {
		item := byID[id]
		summarizeWorkItem(item, cfg)
		item.StaleWeeks = weeksSince(item.LastWeek, currentWeek)
		result = append(result, *item)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Similar and duplicated work
// ---------------------------------------------------------------------------

// distinctiveTokens drops the words that appear in almost every report title.
// Without this, "운영 업무 점검" in two teams looks like the same project.
func distinctiveTokens(title string) map[string]bool {
	result := map[string]bool{}
	for token := range titleTokens(title) {
		if analysisStopwords[token] || len([]rune(token)) < 2 {
			continue
		}
		result[token] = true
	}
	return result
}

// sharedDistinctive returns the meaningful words two titles have in common.
func sharedDistinctive(left, right string) []string {
	return sharedTokens(distinctiveTokens(left), distinctiveTokens(right))
}

func sharedTokens(leftTokens, rightTokens map[string]bool) []string {
	shared := []string{}
	for token := range leftTokens {
		if rightTokens[token] {
			shared = append(shared, token)
		}
	}
	sort.Strings(shared)
	return shared
}

type workLink struct {
	Similarity   int      `json:"similarity"`
	SharedTerms  []string `json:"sharedTerms"`
	CrossOrg     bool     `json:"crossOrganization"`
	Duplicate    bool     `json:"duplicateCandidate"`
	OverlapWeeks int      `json:"overlapWeeks"`
	Left         workRef  `json:"left"`
	Right        workRef  `json:"right"`
	Reason       string   `json:"reason"`
}

type workRef struct {
	WorkItemID       int64  `json:"workItemId"`
	Title            string `json:"title"`
	Category         string `json:"category"`
	UserID           int64  `json:"userId"`
	DisplayName      string `json:"displayName"`
	OrganizationID   *int64 `json:"organizationId,omitempty"`
	OrganizationName string `json:"organizationName"`
	Progress         int    `json:"progress"`
	LastWeek         string `json:"lastWeek"`
	Completed        bool   `json:"completed"`
}

func referenceTo(item workItemView) workRef {
	return workRef{WorkItemID: item.ID, Title: item.Title, Category: item.Category,
		UserID: item.UserID, DisplayName: item.DisplayName,
		OrganizationID: item.OrganizationID, OrganizationName: item.OrganizationName,
		Progress: item.Progress, LastWeek: item.LastWeek, Completed: item.Completed}
}

func sameOrganization(left, right workItemView) bool {
	if left.OrganizationID == nil || right.OrganizationID == nil {
		return left.OrganizationID == right.OrganizationID
	}
	return *left.OrganizationID == *right.OrganizationID
}

// linkWorkItems finds pairs of related work belonging to different people.
//
// Same owner is excluded on purpose: one person reporting two similar tasks is
// how work is normally broken down, and the period rollup already merges those.
// linkWorkItems finds related work across owners, keeping only the strongest
// links of each kind.
//
// Every qualifying pair used to be returned. That is quadratic in the number of
// tasks and unbounded in the response: measured on 1,805 work items it produced
// 1,606,500 links and a 911MB body in 8.8 seconds, and the screen rendered all
// of them. The pairs still have to be compared — that is what finding related
// work means — but only the ones anyone can act on are carried out of here, and
// the totals go with them so the screen never implies it is showing everything.
//
// Duplicates and merely-similar links are ranked separately: a handful of
// cross-organisation duplicates is the point of the feature, and they must not
// be crowded out by thousands of loosely similar titles.
type workLinkResult struct {
	// Ranked and capped, for screens that list pairs.
	Duplicates []workLink
	Similar    []workLink
	// How many qualified in total, so a screen can say what it is not showing.
	DuplicateTotal int
	SimilarTotal   int
	// Aggregated over every qualifying pair, not over the capped lists.
	Collaboration []collaborationEdge
	// One duplicate link per work item, for callers that score tasks rather
	// than list pairs. Bounded by the number of tasks, so it can cover every
	// duplicate even when the ranked list cannot.
	DuplicateByItem map[int64]workLink
}

func linkWorkItems(items []workItemView, limit int) workLinkResult {
	var duplicateTotal, similarTotal int
	var duplicateByItem map[int64]workLink
	topDuplicates := newLinkTop(limit)
	topSimilar := newLinkTop(limit)
	// Titles are tokenized once each rather than twice per pair. The comparison
	// itself is unavoidably quadratic, but the tokenizing was too: 1,800 tasks
	// meant 3.2 million passes over the same few hundred strings.
	tokens := make([]map[string]bool, len(items))
	distinctive := make([]map[string]bool, len(items))
	weeks := make([]map[string]bool, len(items))
	for index := range items {
		tokens[index] = titleTokens(items[index].Title)
		distinctive[index] = distinctiveTokens(items[index].Title)
		weeks[index] = weekSet(items[index])
	}
	// Collaboration is aggregated over every qualifying pair, not over the
	// ranked survivors: it groups by organisation pair, so it is small however
	// many links there are, and building it from a top-200 sample would drop
	// whole organisation pairs from the map while presenting it as complete.
	collaboration := newCollaboration()
	duplicateByItem = map[int64]workLink{}

	// Only pairs that share a distinctive word are compared.
	//
	// A pair with nothing meaningful in common is rejected a few lines below
	// whatever its token similarity is, so it was never a candidate — but the
	// similarity was computed first, for every pair. At 2,100 tasks that is 2.2
	// million comparisons to reach the same answer, and it measured at 1.09s of
	// a 1.17s executive digest: 93% of the request spent proving pairs wrong.
	//
	// An inverted index over the distinctive words produces exactly the pairs
	// that can survive. The set of links is unchanged; only the work to find
	// them is. The comparison itself is still quadratic in the worst case — a
	// word every title shares would rebuild the full set — and that is the
	// right trade, because narrowing it further would start losing links.
	byToken := map[string][]int{}
	for index := range items {
		for token := range distinctive[index] {
			byToken[token] = append(byToken[token], index)
		}
	}
	// Stamped rather than allocated per row: a map per left index would trade
	// the comparisons saved for garbage collection.
	stamp := make([]int, len(items))
	for index := range stamp {
		stamp[index] = -1
	}
	candidates := make([]int, 0, 64)

	for left := 0; left < len(items); left++ {
		candidates = candidates[:0]
		for token := range distinctive[left] {
			for _, right := range byToken[token] {
				if right > left && stamp[right] != left {
					stamp[right] = left
					candidates = append(candidates, right)
				}
			}
		}
		// Sorted because the token map is walked in random order, and the
		// ranked lists below break ties by arrival. Without this the same data
		// would produce a different top-200 on every request.
		sort.Ints(candidates)
		for _, right := range candidates {
			first, second := &items[left], &items[right]
			if first.UserID == second.UserID {
				continue
			}
			similarity := tokenSimilarity(tokens[left], tokens[right])
			if similarity < relatedTitleSimilarity {
				continue
			}
			shared := sharedTokens(distinctive[left], distinctive[right])
			if len(shared) == 0 {
				// Everything the two titles agree on is boilerplate.
				continue
			}
			crossOrg := !sameOrganization(*first, *second)
			overlap := weeksOverlap(weeks[left], weeks[right])
			duplicate := crossOrg && similarity >= duplicateTitleSimilarity &&
				!first.Completed && !second.Completed && overlap > 0
			link := workLink{
				Similarity: similarity, SharedTerms: shared, CrossOrg: crossOrg,
				Duplicate: duplicate, OverlapWeeks: overlap,
				Left: referenceTo(*first), Right: referenceTo(*second),
			}
			link.Reason = describeLink(link)
			collaboration.add(link)
			if link.Duplicate {
				duplicateTotal++
				// Recorded for every duplicate, not just the ranked ones: the
				// executive digest scores each task on whether it has a
				// duplicate, and a task whose link fell outside the cap would
				// silently lose that ground.
				if _, seen := duplicateByItem[first.ID]; !seen {
					duplicateByItem[first.ID] = link
				}
				if _, seen := duplicateByItem[second.ID]; !seen {
					// Stored from the other task's point of view, so the entry
					// always names the counterpart.
					mirrored := link
					mirrored.Left, mirrored.Right = link.Right, link.Left
					duplicateByItem[second.ID] = mirrored
				}
				topDuplicates.offer(link)
				continue
			}
			similarTotal++
			topSimilar.offer(link)
		}
	}
	return workLinkResult{
		Duplicates: topDuplicates.sorted(), Similar: topSimilar.sorted(),
		DuplicateTotal: duplicateTotal, SimilarTotal: similarTotal,
		Collaboration: collaboration.edges(), DuplicateByItem: duplicateByItem,
	}
}

// weekSet is the set of weeks a task was reported in, built once per task
// rather than twice per pair.
func weekSet(item workItemView) map[string]bool {
	weeks := make(map[string]bool, len(item.Weeks))
	for _, week := range item.Weeks {
		weeks[week.WeekStart] = true
	}
	return weeks
}

func weeksOverlap(left, right map[string]bool) int {
	// Walk the smaller set, so the cost follows the shorter history.
	if len(right) < len(left) {
		left, right = right, left
	}
	count := 0
	for week := range left {
		if right[week] {
			count++
		}
	}
	return count
}

// linkTop keeps the highest ranked links seen so far, bounded.
//
// A slice of everything would defeat the purpose: the memory is the problem, not
// only the response size.
type linkTop struct {
	limit int
	links []workLink
}

func newLinkTop(limit int) *linkTop {
	if limit < 1 {
		limit = 1
	}
	return &linkTop{limit: limit, links: make([]workLink, 0, limit)}
}

// linkRank orders by how much a reader would want to see the pair: how alike
// the titles are, and then how long the two ran at the same time.
func linkRank(link workLink) int {
	return link.Similarity*1000 + min(link.OverlapWeeks, 999)
}

// linkRecency is the later of the pair's two last reported weeks.
func linkRecency(link workLink) string {
	return max(link.Left.LastWeek, link.Right.LastWeek)
}

// linkBetter decides which of two links keeps its place. Rank decides first,
// and ties go to the more recent pair. Ties are not rare: an exact title match
// scores 100 for every pair, so without this a page of perfect scores would be
// whichever pairs the scan reached first, which is the lowest work item ids,
// which is the oldest work. Reviving that page every week is the point.
func linkBetter(left, right workLink) bool {
	leftRank, rightRank := linkRank(left), linkRank(right)
	if leftRank != rightRank {
		return leftRank > rightRank
	}
	return linkRecency(left) > linkRecency(right)
}

func (top *linkTop) offer(link workLink) {
	if len(top.links) < top.limit {
		top.links = append(top.links, link)
		if len(top.links) == top.limit {
			top.reorder()
		}
		return
	}
	// The slice is kept weakest-first once full, so one comparison against the
	// front decides whether the newcomer belongs at all.
	if !linkBetter(link, top.links[0]) {
		return
	}
	top.links[0] = link
	top.reorder()
}

// reorder puts the weakest link at the front.
func (top *linkTop) reorder() {
	sort.Slice(top.links, func(i, j int) bool { return linkBetter(top.links[j], top.links[i]) })
}

func (top *linkTop) sorted() []workLink {
	result := append([]workLink{}, top.links...)
	sort.Slice(result, func(i, j int) bool { return linkBetter(result[i], result[j]) })
	return result
}

// describeLink states why the pair was surfaced, in the words a reader needs to
// decide whether it is real. A score on its own is not actionable.
func describeLink(link workLink) string {
	terms := strings.Join(link.SharedTerms, ", ")
	switch {
	case link.Duplicate:
		return fmt.Sprintf("서로 다른 조직에서 %d주 동안 함께 진행 중이며 핵심 용어(%s)가 일치합니다. 중복 투자 여부를 확인하세요.",
			link.OverlapWeeks, terms)
	case link.CrossOrg && link.OverlapWeeks > 0:
		return fmt.Sprintf("다른 조직에서 같은 기간에 %s 관련 업무를 진행하고 있습니다.", terms)
	case link.CrossOrg:
		return fmt.Sprintf("다른 조직에 %s 관련 업무 이력이 있습니다. 참고할 선행 사례일 수 있습니다.", terms)
	default:
		return fmt.Sprintf("같은 조직의 다른 담당자가 %s 관련 업무를 맡고 있습니다.", terms)
	}
}

// ---------------------------------------------------------------------------
// Collaboration between organizations
// ---------------------------------------------------------------------------

type collaborationEdge struct {
	LeftOrganization  string   `json:"leftOrganization"`
	RightOrganization string   `json:"rightOrganization"`
	SharedWork        int      `json:"sharedWork"`
	People            int      `json:"people"`
	Topics            []string `json:"topics"`
}

// collaborationEdges turns cross-organization links into a team level map: which
// organizations are connected, by how much work, and on what subjects.
//
// This is a map of shared subject matter, not of communication. Two teams
// working on the same topic may never have spoken, which is exactly what makes
// the map worth looking at.
// collaboration accumulates the organisation-pair map one link at a time.
//
// It used to be built from a finished list of links. Once that list became a
// ranked top-200 the map turned into a biased sample of itself: whole
// organisation pairs disappeared while the screen still presented it as the
// complete picture. Aggregation is cheap and bounded by the number of
// organisation pairs, so it now sees every qualifying link.
type collaborationAccumulator struct {
	work   int
	people map[int64]bool
	topics map[string]int
}

type collaboration struct {
	pairs map[string]*collaborationAccumulator
	names map[string][2]string
}

func newCollaboration() *collaboration {
	return &collaboration{pairs: map[string]*collaborationAccumulator{}, names: map[string][2]string{}}
}

func collaborationEdges(links []workLink) []collaborationEdge {
	built := newCollaboration()
	for _, link := range links {
		built.add(link)
	}
	return built.edges()
}

func (c *collaboration) add(link workLink) {
	if !link.CrossOrg {
		return
	}
	edges, names := c.pairs, c.names
	{
		left, right := link.Left.OrganizationName, link.Right.OrganizationName
		if left == "" {
			left = "조직 미지정"
		}
		if right == "" {
			right = "조직 미지정"
		}
		if left > right {
			left, right = right, left
		}
		key := left + "\x00" + right
		entry := edges[key]
		if entry == nil {
			entry = &collaborationAccumulator{people: map[int64]bool{}, topics: map[string]int{}}
			edges[key] = entry
			names[key] = [2]string{left, right}
		}
		entry.work++
		entry.people[link.Left.UserID] = true
		entry.people[link.Right.UserID] = true
		for _, term := range link.SharedTerms {
			entry.topics[term]++
		}
	}
}

func (c *collaboration) edges() []collaborationEdge {
	edges, names := c.pairs, c.names
	result := make([]collaborationEdge, 0, len(edges))
	for key, entry := range edges {
		topics := make([]string, 0, len(entry.topics))
		for topic := range entry.topics {
			topics = append(topics, topic)
		}
		// Most connecting subject first, so the label says what the link is
		// about. Equally common terms are ordered by length: "게이트웨이" tells a
		// reader more about the shared work than "ai" does.
		sort.SliceStable(topics, func(x, y int) bool {
			if entry.topics[topics[x]] != entry.topics[topics[y]] {
				return entry.topics[topics[x]] > entry.topics[topics[y]]
			}
			if len([]rune(topics[x])) != len([]rune(topics[y])) {
				return len([]rune(topics[x])) > len([]rune(topics[y]))
			}
			return topics[x] < topics[y]
		})
		if len(topics) > 5 {
			topics = topics[:5]
		}
		result = append(result, collaborationEdge{
			LeftOrganization: names[key][0], RightOrganization: names[key][1],
			SharedWork: entry.work, People: len(entry.people), Topics: topics,
		})
	}
	// The comparison has to be total, because the rows above came out of a map
	// and arrive in a different order every call. Ordering by shared work and
	// the left organisation alone left every pair of one organisation tied, so
	// 팀 10 was shown against 팀 9 on one request and 팀 2 on the next with
	// nothing having changed — a ranked list that reshuffles on refresh.
	sort.SliceStable(result, func(x, y int) bool {
		if result[x].SharedWork != result[y].SharedWork {
			return result[x].SharedWork > result[y].SharedWork
		}
		if result[x].LeftOrganization != result[y].LeftOrganization {
			return result[x].LeftOrganization < result[y].LeftOrganization
		}
		return result[x].RightOrganization < result[y].RightOrganization
	})
	return result
}

// ---------------------------------------------------------------------------
// Routine work
// ---------------------------------------------------------------------------

type recurringWork struct {
	workRef
	ReportedWeeks int    `json:"reportedWeeks"`
	AgeWeeks      int    `json:"ageWeeks"`
	CadencePct    int    `json:"cadencePercent"`
	ProgressGain  int    `json:"progressGain"`
	IssueWeeks    int    `json:"issueWeeks"`
	Reason        string `json:"reason"`
}

// recurringWorkItems separates routine operation from project work.
//
// The distinction is behavioural, not lexical: routine work is reported on a
// steady cadence over a long span and does not advance towards completion,
// because there is nothing to complete. Keyword lists were rejected — "운영"
// appears in plenty of project titles and routine work is often named after the
// system it maintains, with no shared vocabulary at all.
func recurringWorkItems(items []workItemView) []recurringWork {
	result := []recurringWork{}
	for _, item := range items {
		if item.ReportedWeeks < recurringMinimumWeeks || item.AgeWeeks == 0 {
			continue
		}
		cadence := item.ReportedWeeks * 100 / item.AgeWeeks
		if cadence < recurringCadencePercent {
			continue
		}
		// Work that climbed towards completion is a project, even a long one.
		if item.ProgressGain > recurringMaximumGain {
			continue
		}
		// A task sitting at 100 that keeps being reported is maintenance; a task
		// that finished and stopped is not, and it has already left the window.
		entry := recurringWork{
			workRef: referenceTo(item), ReportedWeeks: item.ReportedWeeks, AgeWeeks: item.AgeWeeks,
			CadencePct: cadence, ProgressGain: item.ProgressGain, IssueWeeks: item.IssueWeeks,
		}
		entry.Reason = fmt.Sprintf("%d주 중 %d주 보고(%d%%), 진척 변화 %+d%%로 완료를 향해 움직이지 않습니다.",
			item.AgeWeeks, item.ReportedWeeks, cadence, item.ProgressGain)
		result = append(result, entry)
	}
	sort.SliceStable(result, func(x, y int) bool {
		if result[x].ReportedWeeks != result[y].ReportedWeeks {
			return result[x].ReportedWeeks > result[y].ReportedWeeks
		}
		return result[x].Title < result[y].Title
	})
	return result
}

// relatedForUser lists, per task belonging to one person, the closest work other
// people are doing.
//
// A handover asks a narrow question — who else is near *these* tasks — so it
// compares only pairs that touch them. Ranking the whole organisation and then
// filtering, as this used to do, meant that on a large tenant the global top
// slice could contain none of the target's tasks and the screen showed no
// related work at all, precisely where it would have been most useful.
func relatedForUser(items []workItemView, userID int64, perItem int) map[int64][]workRef {
	if perItem < 1 {
		perItem = 1
	}
	tokens := make([]map[string]bool, len(items))
	distinctive := make([]map[string]bool, len(items))
	for index := range items {
		tokens[index] = titleTokens(items[index].Title)
		distinctive[index] = distinctiveTokens(items[index].Title)
	}
	type scored struct {
		rank int
		ref  workRef
	}
	found := map[int64][]scored{}
	for mine := range items {
		if items[mine].UserID != userID {
			continue
		}
		for other := range items {
			if items[other].UserID == userID {
				continue
			}
			similarity := tokenSimilarity(tokens[mine], tokens[other])
			if similarity < relatedTitleSimilarity {
				continue
			}
			if len(sharedTokens(distinctive[mine], distinctive[other])) == 0 {
				continue
			}
			id := items[mine].ID
			found[id] = append(found[id], scored{rank: similarity, ref: referenceTo(items[other])})
		}
	}
	related := map[int64][]workRef{}
	for id, candidates := range found {
		sort.SliceStable(candidates, func(x, y int) bool { return candidates[x].rank > candidates[y].rank })
		if len(candidates) > perItem {
			candidates = candidates[:perItem]
		}
		for _, candidate := range candidates {
			related[id] = append(related[id], candidate.ref)
		}
	}
	return related
}
