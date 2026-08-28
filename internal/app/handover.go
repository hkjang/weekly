package app

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Handover answers the question a new owner actually has: not "what is the
// status" but "what happened, what was decided, and what is still unresolved".
//
// It is assembled from the weekly snapshots rather than written by hand,
// because a handover document written under time pressure is exactly the
// document that omits the awkward parts.

type handoverIssue struct {
	Week     string `json:"week"`
	Text     string `json:"text"`
	Resolved bool   `json:"resolved"`
}

// handoverWeek is one reported week, small on purpose. The weeks between
// firstWeek and lastWeek that are absent from this list are the silent ones,
// and the gap is the thing a new owner needs to see.
type handoverWeek struct {
	Week     string `json:"week"`
	Progress int    `json:"progress"`
	Issue    bool   `json:"issue,omitempty"`
}

type handoverItem struct {
	WorkItemID       int64           `json:"workItemId"`
	Title            string          `json:"title"`
	Category         string          `json:"category"`
	OrganizationName string          `json:"organizationName"`
	FirstWeek        string          `json:"firstWeek"`
	LastWeek         string          `json:"lastWeek"`
	AgeWeeks         int             `json:"ageWeeks"`
	ReportedWeeks    int             `json:"reportedWeeks"`
	Progress         int             `json:"progress"`
	Completed        bool            `json:"completed"`
	Stalled          bool            `json:"stalled"`
	OpenIssue        string          `json:"openIssue"`
	OpenAsk          string          `json:"openAsk"`
	NextPlan         string          `json:"nextPlan"`
	IssueHistory     []handoverIssue `json:"issueHistory"`
	Track            []handoverWeek  `json:"track"`
	// Decisions is why the work went the way it did. A weekly record says what
	// happened; this says what was agreed and by whom, which is the half a new
	// owner cannot reconstruct from the reports.
	Decisions   []decisionView `json:"decisions"`
	Milestones  []string       `json:"milestones"`
	RelatedWork []workRef      `json:"relatedWork"`
	Caution     string         `json:"caution"`
}

type handoverView struct {
	UserID      int64  `json:"userId"`
	DisplayName string `json:"displayName"`
	Active      int    `json:"active"`
	Completed   int    `json:"completed"`
	// OpenDecisions counts follow-ups nobody has closed. It belongs in the
	// header because it is the number the receiving owner inherits.
	OpenDecisions int            `json:"openDecisions"`
	Overdue       int            `json:"overdueDecisions"`
	Items         []handoverItem `json:"items"`
}

// milestonesOf reduces a task's weekly results to the weeks where something
// actually changed. Copying every week back out would reproduce the reports the
// new owner already has and would not tell them where the turning points were.
func milestonesOf(item workItemView) []string {
	result := []string{}
	previousProgress := -1
	for _, week := range item.Weeks {
		current := strings.TrimSpace(week.CurrentResult)
		if current == "" {
			continue
		}
		if previousProgress >= 0 && week.Progress == previousProgress {
			continue
		}
		line := fmt.Sprintf("%s · %d%% · %s", week.WeekStart, week.Progress, openingLine(current))
		result = append(result, line)
		previousProgress = week.Progress
	}
	return result
}

// openingLine keeps a summary to its opening statement, which is where authors
// put the point. It reuses the deck builder's line splitting so a handover and
// an exported slide never disagree about what the first line of an entry is.
func openingLine(value string) string {
	return trimRunes(firstLine(value), 140)
}

// issueHistoryOf lists every issue the task reported and whether it later
// disappeared. An issue that vanished without explanation is the thing a new
// owner most needs to ask about.
func issueHistoryOf(item workItemView) []handoverIssue {
	result := []handoverIssue{}
	for index, week := range item.Weeks {
		issue := strings.TrimSpace(week.Issue)
		if issue == "" {
			continue
		}
		if index > 0 && strings.TrimSpace(item.Weeks[index-1].Issue) == issue {
			// The same wording repeated is one issue, not several.
			continue
		}
		resolved := true
		for _, later := range item.Weeks[index+1:] {
			if strings.TrimSpace(later.Issue) == issue {
				resolved = false
				break
			}
		}
		if index == len(item.Weeks)-1 {
			resolved = false
		}
		result = append(result, handoverIssue{Week: week.WeekStart, Text: openingLine(issue), Resolved: resolved})
	}
	return result
}

// trackOf is the week-by-week record the handover screen draws. milestonesOf
// deliberately drops the weeks where nothing changed, which is right for a
// reading list and wrong for a picture: the weeks a task sat still, and the
// weeks it was not reported at all, are exactly what the picture is for.
func trackOf(item workItemView) []handoverWeek {
	track := make([]handoverWeek, 0, len(item.Weeks))
	for _, week := range item.Weeks {
		track = append(track, handoverWeek{
			Week: week.WeekStart, Progress: week.Progress,
			Issue: strings.TrimSpace(week.Issue) != "",
		})
	}
	return track
}

// canViewPerson reports whether this principal may open that person's work.
//
// It answers the same question /team/members answers in bulk, and the two are
// deliberately the same shape: a picker that offers someone the handover screen
// would then refuse is a picker that lies.
func (a *App) canViewPerson(ctx context.Context, p *principal, target int64) (bool, error) {
	if p == nil {
		return false, nil
	}
	if p.ID == target || p.Role == "ADMIN" {
		return true, nil
	}
	if p.Role != "TEAM_LEADER" && p.Role != "ORG_MANAGER" {
		return false, nil
	}
	if p.OrganizationID == nil {
		return false, nil
	}
	var visible bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users u WHERE u.id=$1
		AND u.organization_id IN `+orgSubtree(2)+`)`, target, *p.OrganizationID).Scan(&visible)
	return visible, err
}

// abandonedAfterWeeks is how long a task can go unmentioned before the person
// receiving it has to be told that nobody has touched it.
//
// A month is the shortest gap that cannot be a holiday or a quiet fortnight.
const abandonedAfterWeeks = 4

// weeksSince is how many weeks separate a reported week from the current one.
// Zero when the week is the current one, unparseable or in the future.
func weeksSince(lastWeek, currentWeek string) int {
	last, err := time.Parse("2006-01-02", lastWeek)
	if err != nil {
		return 0
	}
	current, err := time.Parse("2006-01-02", currentWeek)
	if err != nil {
		return 0
	}
	weeks := int(current.Sub(last).Hours() / (24 * 7))
	if weeks < 0 {
		return 0
	}
	return weeks
}

// cautionFor states the one thing most likely to surprise the new owner.
//
// currentWeek is needed because every other measure here is computed inside the
// task's own reported span, and a task that simply stopped has nothing wrong
// inside it. Measured on a deployment: an item last mentioned seven months ago
// at 49% came through with 멈춤 false, silentWeeks 0 and no caution at all,
// while a task reported this week carried "32주째 진척이 없습니다" and one that
// missed a single week said so. The screen warned about a one-week gap and said
// nothing about a seven-month disappearance — on the one screen whose whole
// question is "what does this person still owe?".
func cautionFor(item workItemView, currentWeek string) string {
	silentSince := item.StaleWeeks
	if silentSince == 0 {
		silentSince = weeksSince(item.LastWeek, currentWeek)
	}
	switch {
	case silentSince >= abandonedAfterWeeks && !item.Completed:
		return fmt.Sprintf("%d주째 이 업무를 언급한 보고가 없습니다. 마지막 기록은 %s 주차이며, 그 뒤의 상황은 아무 데도 없습니다.",
			silentSince, item.LastWeek)
	case strings.TrimSpace(item.LatestManagementAsk) != "":
		return "인수 시점에 상위 조직 결정이 대기 중입니다. 요청이 살아 있는지 먼저 확인하세요."
	case item.Stalled:
		return fmt.Sprintf("%d주째 진척이 없습니다. 멈춘 이유가 기록에 없다면 이전 담당자에게 확인하세요.", item.StalledWeeks)
	case item.IssueRunWeeks >= 2:
		return fmt.Sprintf("같은 이슈가 %d주간 이어졌습니다. 시도했던 방법을 먼저 파악하세요.", item.IssueRunWeeks)
	case item.SilentWeeks > 0:
		return fmt.Sprintf("보고가 %d주 누락됐습니다. 그 기간의 진행 상황은 기록에 없습니다.", item.SilentWeeks)
	case item.Completed:
		return "완료로 보고된 업무입니다. 후속 운영 책임이 남아 있는지 확인하세요."
	default:
		return ""
	}
}

func (a *App) handover(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	target := p.ID
	if value := strings.TrimSpace(r.URL.Query().Get("userId")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "INVALID_USER", "담당자 식별자가 올바르지 않습니다.")
			return
		}
		target = parsed
	}
	// Reading someone else's work follows the same rule as reading their
	// reports: only a leader of their organization, or an administrator.
	//
	// Checking the role was not enough. loadWorkItems scopes the work, so no
	// task ever leaked, but the handler then filled in the name from the users
	// table for whatever id it was handed — so any leader could walk the id
	// range and read display names right across the company. Worse for the
	// people who are allowed to be here: an out-of-scope person came back as a
	// perfectly ordinary empty handover, indistinguishable from someone who
	// genuinely has no open work.
	if target != p.ID {
		visible, err := a.canViewPerson(r.Context(), p, target)
		if err != nil {
			a.logger.Error("handover scope", "error", err, "trace", traceIDFromContext(r.Context()))
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "인수인계 자료를 만들 수 없습니다.")
			return
		}
		if !visible {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "조회 권한 범위 밖의 담당자입니다.")
			return
		}
	}
	scope := scopeForPrincipal(p, false)
	if target == p.ID {
		scope = scopeForPrincipal(p, true)
	}
	items, err := a.loadWorkItems(r.Context(), scope, "")
	if err != nil {
		a.logger.Error("handover", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "인수인계 자료를 만들 수 없습니다.")
		return
	}
	// Only pairs touching this person's tasks, ranked per task. The handover
	// answers "who else is near this work", which is a question about a few
	// dozen tasks, not about the whole organisation.
	related := relatedForUser(items, target, handoverRelatedPerItem)

	// Every decision on this person's tasks, in one query rather than one per
	// task on the screen.
	owned := []int64{}
	for _, item := range items {
		if item.UserID == target {
			owned = append(owned, item.ID)
		}
	}
	decisions, err := a.decisionsForWorkItems(r.Context(), owned)
	if err != nil {
		a.logger.Error("handover decisions", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "인수인계 자료를 만들 수 없습니다.")
		return
	}
	today := time.Now().In(a.serviceLocation(r.Context())).Format("2006-01-02")
	// Week starts, not today: every reported week is a week start, and
	// subtracting today from one would round by whatever day it happens to be.
	currentWeek := currentWeekStart(time.Now().In(a.serviceLocation(r.Context())),
		a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format("2006-01-02")

	view := handoverView{UserID: target, Items: []handoverItem{}}
	for _, item := range items {
		if item.UserID != target {
			continue
		}
		view.DisplayName = item.DisplayName
		if item.Completed {
			view.Completed++
		} else {
			view.Active++
		}
		entry := handoverItem{
			WorkItemID: item.ID, Title: item.Title, Category: item.Category,
			OrganizationName: item.OrganizationName,
			FirstWeek:        item.FirstWeek, LastWeek: item.LastWeek,
			AgeWeeks: item.AgeWeeks, ReportedWeeks: item.ReportedWeeks,
			Progress: item.Progress, Completed: item.Completed, Stalled: item.Stalled,
			OpenIssue: strings.TrimSpace(item.LatestIssue), OpenAsk: strings.TrimSpace(item.LatestManagementAsk),
			Milestones: milestonesOf(item), IssueHistory: issueHistoryOf(item),
			Track:       trackOf(item),
			RelatedWork: related[item.ID], Caution: cautionFor(item, currentWeek),
			Decisions: decisions[item.ID],
		}
		if len(item.Weeks) > 0 {
			entry.NextPlan = strings.TrimSpace(item.Weeks[len(item.Weeks)-1].NextPlan)
		}
		if entry.RelatedWork == nil {
			entry.RelatedWork = []workRef{}
		}
		if entry.Decisions == nil {
			entry.Decisions = []decisionView{}
		}
		for _, decision := range entry.Decisions {
			if decision.Status == decisionOpen {
				view.OpenDecisions++
			}
		}
		// An agreement whose date has passed outranks everything else this
		// screen might warn about. The other cautions describe what the record
		// shows; this one names a commitment somebody made and did not keep.
		if late := overdueDecision(entry.Decisions, today); late != nil {
			view.Overdue++
			entry.Caution = fmt.Sprintf("%s의 결정 '%s'에 딸린 후속 조치가 %s 기한을 넘겼습니다. 인수 전에 상태를 확인하세요.",
				late.DecidedBy, late.Title, late.DueDate)
		}
		view.Items = append(view.Items, entry)
	}
	if view.DisplayName == "" {
		_ = a.db.QueryRow(r.Context(), `SELECT display_name FROM users WHERE id=$1`, target).Scan(&view.DisplayName)
	}
	// Unfinished work first: that is what is actually being handed over.
	sort.SliceStable(view.Items, func(x, y int) bool {
		if view.Items[x].Completed != view.Items[y].Completed {
			return !view.Items[x].Completed
		}
		if view.Items[x].Stalled != view.Items[y].Stalled {
			return view.Items[x].Stalled
		}
		return view.Items[x].AgeWeeks > view.Items[y].AgeWeeks
	})
	writeData(w, http.StatusOK, view)
}

// ---------------------------------------------------------------------------
// Work graph endpoint
// ---------------------------------------------------------------------------

// How many links each list carries. Enough that a reader scrolling the screen
// runs out of attention before the list runs out of entries, and few enough
// that the response stays something a browser can render.
const (
	insightLinkLimit = 200
	// Per task, not per handover: a reader scanning a colleague's work wants a
	// few names beside each item, not a ranked list of the whole company.
	handoverRelatedPerItem = 5
)

type workGraphView struct {
	Weeks     int    `json:"weeks"`
	Since     string `json:"since"`
	WorkItems int    `json:"workItems"`
	// How many organisations the reader can actually see. Two of these views
	// are about work crossing between organisations, and for a team leader
	// whose scope is one team they can never hold anything. Saying "there is no
	// cross-organisation duplication" to somebody who was shown one
	// organisation asserts an absence that could not have been a presence — the
	// same mistake this screen already avoids for a failed load, one level in.
	Organizations int `json:"organizations"`
	// The lists are capped; the totals say by how much, so the screen can state
	// what it is not showing rather than implying it has shown everything.
	Similar        []workLink          `json:"similar"`
	SimilarTotal   int                 `json:"similarTotal"`
	Duplicates     []workLink          `json:"duplicates"`
	DuplicateTotal int                 `json:"duplicateTotal"`
	Collaboration  []collaborationEdge `json:"collaboration"`
	Recurring      []recurringWork     `json:"recurring"`
	// Its siblings above have carried a total since they were capped; this one
	// did not, and grew to 900 rows and 332 KB of a 527 KB response on a 300
	// person organisation. A list nobody counted is a list nobody can tell is
	// complete.
	RecurringTotal int `json:"recurringTotal"`
	// Blockers holding several unfinished tasks up. Counted from the blocker's
	// end, which is the only end where the pattern is visible.
	Bottlenecks []bottleneck `json:"bottlenecks"`
}

func (a *App) workGraph(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	if p.Role == "USER" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "업무 인사이트는 팀장 이상만 조회할 수 있습니다.")
		return
	}
	weeks := 12
	if value := a.settingIntFromQuery(r, "weeks"); value > 0 {
		weeks = value
	}
	if weeks > 104 {
		weeks = 104
	}
	since := time.Now().In(a.serviceLocation(r.Context())).AddDate(0, 0, -7*weeks).Format("2006-01-02")
	scope := scopeForPrincipal(p, false)
	// Whole history, then the window. The pair sentence says "서로 다른 조직에서
	// %d주 동안 함께 진행 중" and asks the reader to judge duplicated
	// investment from it; overlap counted inside the window is the window.
	// Measured on a deployment: one pair read 4주 at weeks=4 and 52주 at
	// weeks=104, and the default is 12. linkRank breaks ties between equally
	// similar pairs by that same number, so among the many exact-title matches
	// the ordering collapsed as soon as they all reached the edge.
	//
	// The window still chooses the pairs — it is the item count that drives the
	// pairwise comparison, and that is unchanged.
	loaded, err := a.loadWorkItems(r.Context(), scope, "")
	if err != nil {
		a.logger.Error("work graph", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무 인사이트를 만들 수 없습니다.")
		return
	}
	items := make([]workItemView, 0, len(loaded))
	for _, item := range loaded {
		if item.LastWeek >= since {
			items = append(items, item)
		}
	}
	graph := linkWorkItems(items, insightLinkLimit)
	if graph.SimilarTotal > len(graph.Similar) || graph.DuplicateTotal > len(graph.Duplicates) {
		a.conditions.once("work-graph-truncated", "work graph truncated", "workItems", len(items),
			"similar", graph.SimilarTotal, "duplicates", graph.DuplicateTotal, "limit", insightLinkLimit)
	}
	// Declared dependencies, not inferred ones: everything else on this screen
	// is a candidate a person still has to confirm, and this is the one section
	// somebody already asserted.
	blockers, err := a.bottlenecks(r.Context(), scope)
	if err != nil {
		a.logger.Error("bottlenecks", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무 인사이트를 만들 수 없습니다.")
		return
	}
	// Capped like the two lists beside it, and by the same figure: routine work
	// is read to recognise a pattern, and the pattern is visible in the first
	// screenful. The count says what is not shown.
	recurring := recurringWorkItems(items)
	recurringTotal := len(recurring)
	if len(recurring) > insightLinkLimit {
		recurring = recurring[:insightLinkLimit]
	}
	seenOrganizations := map[string]bool{}
	for _, item := range items {
		if name := strings.TrimSpace(item.OrganizationName); name != "" {
			seenOrganizations[name] = true
		}
	}
	writeData(w, http.StatusOK, workGraphView{
		Weeks: weeks, Since: since, WorkItems: len(items),
		Organizations: len(seenOrganizations),
		Similar:       graph.Similar, SimilarTotal: graph.SimilarTotal,
		Duplicates: graph.Duplicates, DuplicateTotal: graph.DuplicateTotal,
		Collaboration: graph.Collaboration,
		Recurring:     recurring, RecurringTotal: recurringTotal,
		Bottlenecks: blockers,
	})
}

// ---------------------------------------------------------------------------
// Who a leader can hand work over for
// ---------------------------------------------------------------------------

type teamMember struct {
	ID               int64  `json:"id"`
	DisplayName      string `json:"displayName"`
	OrganizationName string `json:"organizationName"`
	Active           bool   `json:"active"`
	LastWeek         string `json:"lastWeek"`
}

// teamMembers lists the people whose work this leader can open.
//
// The handover screen used to build this list out of 팀 주간보고, which returns
// a page of reports. Measured on 120 people over 26 weeks that page held the
// most recent five weeks, so anyone who had stopped reporting before then was
// simply not in the list — and the person who stopped reporting is exactly the
// person a handover is for. Someone who left six weeks ago was unreachable on
// the screen built to hand over their work.
//
// People come from the people table. Inactive accounts are included and marked,
// because they are the ones being handed over.
func (a *App) teamMembers(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	query := `SELECT u.id,u.display_name,COALESCE(o.name,''),u.active,
			COALESCE(MAX(rep.week_start)::text,'')
		FROM users u
		LEFT JOIN organizations o ON o.id=u.organization_id
		LEFT JOIN weekly_reports rep ON rep.user_id=u.id
		WHERE 1=1`
	args := []any{}
	if p.Role != "ADMIN" {
		if p.OrganizationID == nil {
			args = append(args, p.ID)
			query += fmt.Sprintf(" AND u.id=$%d", len(args))
		} else {
			args = append(args, p.ID)
			query += fmt.Sprintf(" AND (u.id=$%d", len(args))
			args = append(args, *p.OrganizationID)
			query += ` OR u.organization_id IN ` + orgSubtree(len(args)) + `)`
		}
	}
	// Active people first, then by name: a picker is read top down, and the
	// accounts still filing reports are the ones opened most often.
	query += ` GROUP BY u.id,u.display_name,o.name,u.active ORDER BY u.active DESC,u.display_name`
	rows, err := a.db.Query(r.Context(), query, args...)
	if err != nil {
		a.logger.Error("team members", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "담당자 목록을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	members := []teamMember{}
	for rows.Next() {
		var member teamMember
		if err := rows.Scan(&member.ID, &member.DisplayName, &member.OrganizationName, &member.Active, &member.LastWeek); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "담당자 목록을 조회할 수 없습니다.")
			return
		}
		members = append(members, member)
	}
	writeData(w, http.StatusOK, members)
}
