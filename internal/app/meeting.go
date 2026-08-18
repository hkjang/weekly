package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Meeting mode and the executive digest answer the same question at two
// different altitudes: of everything that happened, what actually needs to be
// said out loud?
//
// Both select deterministically from the weekly snapshots and both carry the
// reason for every selection. A meeting agenda that cannot explain why an item
// is on it wastes the room's time, and an executive summary without its basis
// is just an assertion.

const (
	// A meeting looks at the reported week and the one before it, because
	// "what changed" needs something to change from.
	meetingComparisonWeeks = 2
	// The digest is a briefing, not a list. Past ten entries nobody reads it.
	digestMaximumEntries = 10
	digestMinimumScore   = 20
)

type meetingEntry struct {
	WorkItemID       int64  `json:"workItemId"`
	Title            string `json:"title"`
	Category         string `json:"category"`
	DisplayName      string `json:"displayName"`
	OrganizationName string `json:"organizationName"`
	Detail           string `json:"detail"`
	Note             string `json:"note"`
	Progress         int    `json:"progress"`
	ProgressDelta    int    `json:"progressDelta"`
	Weeks            int    `json:"weeks"`
}

type meetingSection struct {
	Key     string         `json:"key"`
	Title   string         `json:"title"`
	Purpose string         `json:"purpose"`
	Entries []meetingEntry `json:"entries"`
}

type meetingView struct {
	Week         string           `json:"week"`
	PreviousWeek string           `json:"previousWeek"`
	Scope        string           `json:"scope"`
	People       int              `json:"people"`
	WorkItems    int              `json:"workItems"`
	Sections     []meetingSection `json:"sections"`
}

// weekBefore returns the ISO date one week earlier.
func weekBefore(week string) string {
	parsed, err := time.Parse("2006-01-02", week)
	if err != nil {
		return ""
	}
	return parsed.AddDate(0, 0, -7).Format("2006-01-02")
}

// snapshotFor returns the task's report for a given week, if it has one.
func snapshotFor(item workItemView, week string) *workItemWeek {
	for index := range item.Weeks {
		if item.Weeks[index].WeekStart == week {
			return &item.Weeks[index]
		}
	}
	return nil
}

// buildMeeting selects the agenda for one week.
//
// The sections are ordered by what a meeting has to resolve first: things only
// this room can decide, then what is newly broken, then what has been broken
// long enough to need a different approach, then what moved, and finally what
// went quiet. Everything that merely continued unchanged is deliberately left
// out — that is what the written report is for.
func buildMeeting(items []workItemView, week string, cfg rollupConfig) meetingView {
	previous := weekBefore(week)
	view := meetingView{Week: week, PreviousWeek: previous}

	decisions := []meetingEntry{}
	newIssues := []meetingEntry{}
	longIssues := []meetingEntry{}
	changes := []meetingEntry{}
	silent := []meetingEntry{}

	people := map[int64]bool{}
	counted := 0
	for _, item := range items {
		current := snapshotFor(item, week)
		prior := snapshotFor(item, previous)
		if current == nil && prior == nil {
			continue
		}
		counted++
		people[item.UserID] = true
		base := meetingEntry{
			WorkItemID: item.ID, Title: item.Title, Category: item.Category,
			DisplayName: item.DisplayName, OrganizationName: item.OrganizationName,
			Progress: item.Progress, Weeks: item.ReportedWeeks,
		}

		if current != nil {
			if ask := strings.TrimSpace(current.ManagementAsk); ask != "" {
				entry := base
				entry.Detail = ask
				entry.Note = "상위 조직의 결정이나 자원이 필요합니다."
				decisions = append(decisions, entry)
			}
			issue := strings.TrimSpace(current.Issue)
			priorIssue := ""
			if prior != nil {
				priorIssue = strings.TrimSpace(prior.Issue)
			}
			if issue != "" {
				entry := base
				entry.Detail = issue
				if priorIssue == "" {
					entry.Note = "이번 주에 새로 보고된 이슈입니다."
					newIssues = append(newIssues, entry)
				} else if item.IssueWeeks >= cfg.PersistentIssueWeeks {
					entry.Note = fmt.Sprintf("%d주째 해소되지 않은 이슈입니다.", item.IssueWeeks)
					longIssues = append(longIssues, entry)
				}
			}
		}

		// What changed, stated as a change rather than as a status.
		switch {
		case current != nil && prior == nil && item.FirstWeek == week:
			entry := base
			entry.Detail = strings.TrimSpace(current.CurrentResult)
			entry.Note = "이번 주에 시작된 업무입니다."
			changes = append(changes, entry)
		case current != nil && current.Progress >= 100 && (prior == nil || prior.Progress < 100):
			entry := base
			entry.Detail = strings.TrimSpace(current.CurrentResult)
			entry.Note = fmt.Sprintf("%d주 만에 완료됐습니다.", item.ReportedWeeks)
			entry.ProgressDelta = 100
			if prior != nil {
				entry.ProgressDelta = 100 - prior.Progress
			}
			changes = append(changes, entry)
		case current != nil && prior != nil && current.Progress != prior.Progress:
			entry := base
			entry.Detail = strings.TrimSpace(current.CurrentResult)
			entry.ProgressDelta = current.Progress - prior.Progress
			if entry.ProgressDelta < 0 {
				entry.Note = "진척도가 지난주보다 낮게 보고됐습니다. 확인이 필요합니다."
			} else {
				entry.Note = fmt.Sprintf("진척 %d%% → %d%%", prior.Progress, current.Progress)
			}
			changes = append(changes, entry)
		case current != nil && prior != nil && item.StalledWeeks >= cfg.StallWeeks && !item.Completed:
			entry := base
			entry.Detail = strings.TrimSpace(current.NextPlan)
			entry.Note = fmt.Sprintf("%d주째 진척이 없습니다.", item.StalledWeeks)
			changes = append(changes, entry)
		case current == nil && prior != nil && !item.Completed:
			entry := base
			entry.Detail = strings.TrimSpace(prior.NextPlan)
			entry.Note = "지난주에는 보고됐으나 이번 주 보고에 없습니다."
			silent = append(silent, entry)
		}
	}

	view.People = len(people)
	view.WorkItems = counted
	view.Sections = []meetingSection{
		{Key: "DECISION", Title: "결정 필요", Purpose: "이 자리에서만 정할 수 있는 사항입니다.", Entries: decisions},
		{Key: "NEW_ISSUE", Title: "신규 이슈", Purpose: "이번 주에 새로 생긴 문제입니다.", Entries: newIssues},
		{Key: "LONG_ISSUE", Title: "지속 이슈", Purpose: "같은 방법으로는 풀리지 않는 문제입니다.", Entries: longIssues},
		{Key: "CHANGE", Title: "변경점", Purpose: "지난주 대비 달라진 것만 담았습니다.", Entries: changes},
		{Key: "SILENT", Title: "보고 누락", Purpose: "지난주에 있었으나 이번 주에 사라진 업무입니다.", Entries: silent},
	}
	return view
}

func (a *App) meetingMode(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	scope := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = scopeSelf
	}
	if scope != scopeSelf && scope != scopeTeam {
		writeError(w, http.StatusBadRequest, "INVALID_SCOPE", "조회 범위는 SELF 또는 TEAM이어야 합니다.")
		return
	}
	if scope == scopeTeam && p.Role == "USER" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조직 단위 회의 자료는 팀장 이상만 조회할 수 있습니다.")
		return
	}
	week := strings.TrimSpace(r.URL.Query().Get("week"))
	if week == "" {
		week = currentWeekStart(time.Now().In(a.serviceLocation(r.Context())),
			a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", week); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WEEK", "주차는 YYYY-MM-DD 형식이어야 합니다.")
		return
	}
	// Load a little history so ageing figures are right, not just the two weeks
	// the agenda compares.
	since := ""
	if parsed, err := time.Parse("2006-01-02", week); err == nil {
		since = parsed.AddDate(0, 0, -7*26).Format("2006-01-02")
	}
	items, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, scope == scopeSelf), since)
	if err != nil {
		a.logger.Error("meeting mode", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "회의 자료를 만들 수 없습니다.")
		return
	}
	view := buildMeeting(items, week, a.rollupConfig(r.Context()))
	view.Scope = scope
	writeData(w, http.StatusOK, view)
}

// ---------------------------------------------------------------------------
// Executive digest
// ---------------------------------------------------------------------------

type digestEntry struct {
	Kind             string   `json:"kind"`
	Score            int      `json:"score"`
	Title            string   `json:"title"`
	WorkItemID       int64    `json:"workItemId"`
	DisplayName      string   `json:"displayName"`
	OrganizationName string   `json:"organizationName"`
	Headline         string   `json:"headline"`
	Detail           string   `json:"detail"`
	Grounds          []string `json:"grounds"`
}

type digestView struct {
	Weeks      int           `json:"weeks"`
	Since      string        `json:"since"`
	Scope      string        `json:"scope"`
	Considered int           `json:"considered"`
	Entries    []digestEntry `json:"entries"`
}

// buildDigest picks the few things worth an executive's attention.
//
// The score is a sum of independently observable facts, never a model output,
// and every contribution is listed back to the reader as grounds. An executive
// who cannot see why an item was selected has no way to disagree with it, and
// an unfalsifiable summary is worse than none.
func buildDigest(items []workItemView, links []workLink, cfg rollupConfig) []digestEntry {
	duplicateByItem := map[int64]workLink{}
	for _, link := range links {
		if !link.Duplicate {
			continue
		}
		if _, exists := duplicateByItem[link.Left.WorkItemID]; !exists {
			duplicateByItem[link.Left.WorkItemID] = link
		}
	}

	entries := []digestEntry{}
	for _, item := range items {
		score := 0
		grounds := []string{}
		kind := ""
		headline := ""

		if ask := strings.TrimSpace(item.LatestManagementAsk); ask != "" {
			score += 40
			grounds = append(grounds, "상위 조직 결정·자원 요청이 열려 있습니다.")
			kind, headline = "DECISION", "결정 대기"
		}
		// Issue weeks are historical. Counting them for finished work reported
		// the completed task as an open risk, which is the opposite of true.
		if item.IssueWeeks >= cfg.PersistentIssueWeeks && !item.Completed {
			score += 10 * item.IssueWeeks
			grounds = append(grounds, fmt.Sprintf("이슈가 %d주째 지속되고 있습니다.", item.IssueWeeks))
			if kind == "" {
				kind, headline = "RISK", "장기 이슈"
			}
		}
		if item.Stalled && !item.Completed {
			score += 10 * item.StalledWeeks
			grounds = append(grounds, fmt.Sprintf("진척이 %d주째 멈춰 있습니다.", item.StalledWeeks))
			if kind == "" {
				kind, headline = "RISK", "진척 정체"
			}
		}
		if item.SilentWeeks > 0 && !item.Completed {
			score += 5 * item.SilentWeeks
			grounds = append(grounds, fmt.Sprintf("%d주간 보고가 누락됐습니다.", item.SilentWeeks))
		}
		if link, duplicated := duplicateByItem[item.ID]; duplicated {
			score += 25
			grounds = append(grounds, fmt.Sprintf("%s의 '%s'와(과) 중복 가능성이 있습니다.",
				link.Right.OrganizationName, link.Right.Title))
			if kind == "" {
				kind, headline = "DUPLICATE", "중복 투자 의심"
			}
		}
		// Completion of long running work is news too. A digest that only ever
		// reports problems trains its readers to distrust it.
		// Routine operation sits at 100% every week and never "completes", so
		// requiring actual movement keeps weekly maintenance out of a briefing
		// that is supposed to carry news.
		if item.Completed && item.ReportedWeeks >= 4 && item.ProgressGain > 0 {
			// The base clears digestMinimumScore on its own: work that ran the
			// minimum four weeks and finished is worth reporting, and scoring it
			// just below the cut-off would have silently dropped exactly the
			// case this rule exists for.
			score += 20 + item.ReportedWeeks
			grounds = append(grounds, fmt.Sprintf("%d주간 진행한 업무가 완료됐습니다.", item.ReportedWeeks))
			if kind == "" {
				kind, headline = "PROGRESS", "주요 업무 완료"
			}
		}
		if kind == "" || score < digestMinimumScore {
			continue
		}
		detail := strings.TrimSpace(item.LatestManagementAsk)
		if detail == "" {
			detail = strings.TrimSpace(item.LatestIssue)
		}
		entries = append(entries, digestEntry{
			Kind: kind, Score: score, Title: item.Title, WorkItemID: item.ID,
			DisplayName: item.DisplayName, OrganizationName: item.OrganizationName,
			Headline: headline, Detail: detail, Grounds: grounds,
		})
	}
	sort.SliceStable(entries, func(x, y int) bool {
		if entries[x].Score != entries[y].Score {
			return entries[x].Score > entries[y].Score
		}
		return entries[x].Title < entries[y].Title
	})
	if len(entries) > digestMaximumEntries {
		entries = entries[:digestMaximumEntries]
	}
	return entries
}

func (a *App) executiveDigest(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	weeks := 8
	if value := a.settingIntFromQuery(r, "weeks"); value > 0 {
		weeks = value
	}
	if weeks > 52 {
		weeks = 52
	}
	since := time.Now().In(a.serviceLocation(r.Context())).AddDate(0, 0, -7*weeks).Format("2006-01-02")
	items, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, false), since)
	if err != nil {
		a.logger.Error("executive digest", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "경영 요약을 만들 수 없습니다.")
		return
	}
	links := linkWorkItems(items)
	view := digestView{Weeks: weeks, Since: since, Scope: scopeTeam, Considered: len(items),
		Entries: buildDigest(items, links, a.rollupConfig(r.Context()))}
	writeData(w, http.StatusOK, view)
}
