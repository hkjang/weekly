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
		return fmt.Sprintf(` AND (w.user_id=$%d OR u.organization_id IN (WITH RECURSIVE orgs AS
			(SELECT id FROM organizations WHERE id=$%d UNION ALL SELECT o.id FROM organizations o JOIN orgs x ON o.parent_id=x.id)
			SELECT id FROM orgs))`, start, start+1), args
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
	result := make([]workItemView, 0, len(order))
	for _, id := range order {
		item := byID[id]
		summarizeWorkItem(item, cfg)
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
	leftTokens, rightTokens := distinctiveTokens(left), distinctiveTokens(right)
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

// weeksInCommon counts the weeks both tasks were reported in. Work that ran at
// the same time is a far stronger duplicate signal than work separated by a
// year, which is more likely a repeat of a finished effort.
func weeksInCommon(left, right workItemView) int {
	weeks := map[string]bool{}
	for _, week := range left.Weeks {
		weeks[week.WeekStart] = true
	}
	count := 0
	for _, week := range right.Weeks {
		if weeks[week.WeekStart] {
			count++
		}
	}
	return count
}

// linkWorkItems finds pairs of related work belonging to different people.
//
// Same owner is excluded on purpose: one person reporting two similar tasks is
// how work is normally broken down, and the period rollup already merges those.
func linkWorkItems(items []workItemView) []workLink {
	links := []workLink{}
	for left := 0; left < len(items); left++ {
		for right := left + 1; right < len(items); right++ {
			first, second := items[left], items[right]
			if first.UserID == second.UserID {
				continue
			}
			similarity := titleSimilarity(first.Title, second.Title)
			if similarity < relatedTitleSimilarity {
				continue
			}
			shared := sharedDistinctive(first.Title, second.Title)
			if len(shared) == 0 {
				// Everything the two titles agree on is boilerplate.
				continue
			}
			crossOrg := !sameOrganization(first, second)
			overlap := weeksInCommon(first, second)
			duplicate := crossOrg && similarity >= duplicateTitleSimilarity &&
				!first.Completed && !second.Completed && overlap > 0
			link := workLink{
				Similarity: similarity, SharedTerms: shared, CrossOrg: crossOrg,
				Duplicate: duplicate, OverlapWeeks: overlap,
				Left: referenceTo(first), Right: referenceTo(second),
			}
			link.Reason = describeLink(link)
			links = append(links, link)
		}
	}
	sort.SliceStable(links, func(x, y int) bool {
		if links[x].Duplicate != links[y].Duplicate {
			return links[x].Duplicate
		}
		if links[x].Similarity != links[y].Similarity {
			return links[x].Similarity > links[y].Similarity
		}
		return links[x].OverlapWeeks > links[y].OverlapWeeks
	})
	return links
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
func collaborationEdges(links []workLink) []collaborationEdge {
	type accumulator struct {
		work   int
		people map[int64]bool
		topics map[string]int
	}
	edges := map[string]*accumulator{}
	names := map[string][2]string{}
	for _, link := range links {
		if !link.CrossOrg {
			continue
		}
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
			entry = &accumulator{people: map[int64]bool{}, topics: map[string]int{}}
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
	sort.SliceStable(result, func(x, y int) bool {
		if result[x].SharedWork != result[y].SharedWork {
			return result[x].SharedWork > result[y].SharedWork
		}
		return result[x].LeftOrganization < result[y].LeftOrganization
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
