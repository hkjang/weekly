package app

import (
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
	Milestones       []string        `json:"milestones"`
	RelatedWork      []workRef       `json:"relatedWork"`
	Caution          string          `json:"caution"`
}

type handoverView struct {
	UserID      int64          `json:"userId"`
	DisplayName string         `json:"displayName"`
	Active      int            `json:"active"`
	Completed   int            `json:"completed"`
	Items       []handoverItem `json:"items"`
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

// cautionFor states the one thing most likely to surprise the new owner.
func cautionFor(item workItemView) string {
	switch {
	case strings.TrimSpace(item.LatestManagementAsk) != "":
		return "인수 시점에 상위 조직 결정이 대기 중입니다. 요청이 살아 있는지 먼저 확인하세요."
	case item.Stalled:
		return fmt.Sprintf("%d주째 진척이 없습니다. 멈춘 이유가 기록에 없다면 이전 담당자에게 확인하세요.", item.StalledWeeks)
	case item.IssueWeeks >= 2:
		return fmt.Sprintf("같은 이슈가 %d주간 이어졌습니다. 시도했던 방법을 먼저 파악하세요.", item.IssueWeeks)
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
	if target != p.ID && p.Role == "USER" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "다른 담당자의 인수인계 자료는 팀장 이상만 조회할 수 있습니다.")
		return
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
			RelatedWork: related[item.ID], Caution: cautionFor(item),
		}
		if len(item.Weeks) > 0 {
			entry.NextPlan = strings.TrimSpace(item.Weeks[len(item.Weeks)-1].NextPlan)
		}
		if entry.RelatedWork == nil {
			entry.RelatedWork = []workRef{}
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
	// The lists are capped; the totals say by how much, so the screen can state
	// what it is not showing rather than implying it has shown everything.
	Similar        []workLink          `json:"similar"`
	SimilarTotal   int                 `json:"similarTotal"`
	Duplicates     []workLink          `json:"duplicates"`
	DuplicateTotal int                 `json:"duplicateTotal"`
	Collaboration  []collaborationEdge `json:"collaboration"`
	Recurring      []recurringWork     `json:"recurring"`
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
	items, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, false), since)
	if err != nil {
		a.logger.Error("work graph", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무 인사이트를 만들 수 없습니다.")
		return
	}
	graph := linkWorkItems(items, insightLinkLimit)
	if graph.SimilarTotal > len(graph.Similar) || graph.DuplicateTotal > len(graph.Duplicates) {
		a.logger.Info("work graph truncated", "workItems", len(items),
			"similar", graph.SimilarTotal, "duplicates", graph.DuplicateTotal, "limit", insightLinkLimit)
	}
	writeData(w, http.StatusOK, workGraphView{
		Weeks: weeks, Since: since, WorkItems: len(items),
		Similar: graph.Similar, SimilarTotal: graph.SimilarTotal,
		Duplicates: graph.Duplicates, DuplicateTotal: graph.DuplicateTotal,
		Collaboration: graph.Collaboration, Recurring: recurringWorkItems(items),
	})
}
