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

	// What each observable fact is worth. Named rather than written into the
	// calls, because README publishes this table as the product's reasoning —
	// "근거를 볼 수 없는 요약은 읽는 사람이 반박할 수 없다" — and a published
	// number that nothing pairs with the code drifts. Two rows of that table
	// had already gone missing before this was noticed.
	digestWeightDecision       = 40
	digestWeightIssuePerWeek   = 10
	digestWeightStalledPerWeek = 10
	digestWeightOverdue        = 30
	digestWeightOverduePerWeek = 5
	digestWeightDueAtRisk      = 25
	digestWeightSilentPerWeek  = 5
	digestWeightDuplicate      = 25
	digestWeightDone           = 20
	digestWeightDonePerWeek    = 1
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
	// Total is how many belong in this section; Entries holds at most Limit of
	// them. An agenda that prints everything is not an agenda, and one that
	// silently prints part of everything is worse.
	Total int `json:"total"`
	Limit int `json:"limit"`
}

// meetingSectionLimit is how many rows one heading can carry into a room.
//
// The section had no cap and no count. On a 300 person organisation the
// 변경점 heading came back with 2,100 rows — the entire corpus, presented as
// the list of things to discuss. Nobody reads the 2,100th row, and nothing on
// the screen said there were any.
const meetingSectionLimit = 40

// meetingHistoryWeeks is how far back the agenda reads to age its figures.
// Measured on a seeded deployment of 52 weeks: no issue run reached past this
// window, so the bound costs the agenda nothing and saves it reading a year of
// rows to answer about months.
const meetingHistoryWeeks = 26

// orderMeetingEntries puts the rows a room should hear first at the top, so
// that cutting the tail cuts the least important thing rather than the highest
// work item id.
//
// Work that went backwards leads: it is the only kind of change that means
// something has gone wrong since last week. Then the largest movements, because
// a task that jumped 40% and one that moved 1% are not equally worth the
// meeting's time. Title last, so the order is total and the same data does not
// reshuffle between requests.
func orderMeetingEntries(entries []meetingEntry) {
	sort.SliceStable(entries, func(x, y int) bool {
		left, right := entries[x], entries[y]
		if (left.ProgressDelta < 0) != (right.ProgressDelta < 0) {
			return left.ProgressDelta < 0
		}
		leftSize, rightSize := left.ProgressDelta, right.ProgressDelta
		if leftSize < 0 {
			leftSize = -leftSize
		}
		if rightSize < 0 {
			rightSize = -rightSize
		}
		if leftSize != rightSize {
			return leftSize > rightSize
		}
		if left.Weeks != right.Weeks {
			return left.Weeks > right.Weeks
		}
		return left.Title < right.Title
	})
}

// section builds one heading, ordered and capped, saying how many it left out.
func section(key, title, purpose string, entries []meetingEntry) meetingSection {
	orderMeetingEntries(entries)
	total := len(entries)
	if len(entries) > meetingSectionLimit {
		entries = entries[:meetingSectionLimit]
	}
	return meetingSection{Key: key, Title: title, Purpose: purpose,
		Entries: entries, Total: total, Limit: meetingSectionLimit}
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
				} else if item.IssueRunWeeks >= cfg.PersistentIssueWeeks {
					entry.Note = fmt.Sprintf("%d주째 해소되지 않은 이슈입니다.", item.IssueRunWeeks)
					longIssues = append(longIssues, entry)
				}
			}
		}

		// What changed, stated as a change rather than as a status. The
		// classification is shared with the change summary so the two screens
		// can never disagree about the same task in the same week.
		change := classifyWeeklyChange(item, week, previous, cfg)
		entry := base
		entry.Detail = change.Detail
		entry.Note = change.Note
		entry.ProgressDelta = change.ProgressDelta
		switch change.Kind {
		case changeSilent:
			silent = append(silent, entry)
		case changeNew, changeResumed, changeCompleted, changeProgressed, changeRegressed, changeStalled:
			changes = append(changes, entry)
		}
	}

	view.People = len(people)
	view.WorkItems = counted
	view.Sections = []meetingSection{
		section("DECISION", "결정 필요", "이 자리에서만 정할 수 있는 사항입니다.", decisions),
		section("NEW_ISSUE", "신규 이슈", "이번 주에 새로 생긴 문제입니다.", newIssues),
		section("LONG_ISSUE", "지속 이슈", "같은 방법으로는 풀리지 않는 문제입니다.", longIssues),
		section("CHANGE", "변경점", "지난주 대비 달라진 것만 담았습니다.", changes),
		section("SILENT", "보고 누락", "지난주에 있었으나 이번 주에 사라진 업무입니다.", silent),
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
	parsed, err := time.Parse("2006-01-02", week)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WEEK", "주차는 YYYY-MM-DD 형식이어야 합니다.")
		return
	}
	// Load a little history so ageing figures are right, not just the two weeks
	// the agenda compares — and no more than that, because the whole corpus is
	// a lot to read for figures that only look back months.
	//
	// The week was parsed twice, once to reject a bad one and once to find this
	// bound, and the second parse could not fail after the first had returned.
	// A mutation sweep found that: flipping its `err == nil` to `err != nil`
	// dropped the bound entirely and no test noticed, because a branch that
	// cannot be taken is not a branch anybody wrote a test for. One parse, used
	// for both.
	since := parsed.AddDate(0, 0, -7*meetingHistoryWeeks).Format("2006-01-02")
	// Whole history, then the window — the same reason as the digest below.
	// The agenda writes "%d주째 해소되지 않은 이슈입니다" from this number, and
	// a window that also bounds the count turns that sentence into a statement
	// about meetingHistoryWeeks. Twenty-six weeks hides it longer than the
	// digest's eight did, which makes it harder to notice, not less wrong.
	loaded, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, scope == scopeSelf), "")
	if err != nil {
		a.logger.Error("meeting mode", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "회의 자료를 만들 수 없습니다.")
		return
	}
	items := make([]workItemView, 0, len(loaded))
	for _, item := range loaded {
		if item.LastWeek >= since {
			items = append(items, item)
		}
	}
	view := buildMeeting(items, week, a.rollupConfig(r.Context()))
	// A dependency inside one team is settled by the two people in it. One that
	// crosses an organisation needs somebody in the room who can talk to both,
	// which is what the meeting is for — so it goes on the agenda rather than
	// staying on the work item screen where only its two ends look.
	blocked, err := a.crossOrgBlocked(r.Context(), scopeForPrincipal(p, scope == scopeSelf))
	if err != nil {
		a.logger.Error("meeting dependencies", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "회의 자료를 만들 수 없습니다.")
		return
	}
	if len(blocked) > 0 {
		// Placed after 지속 이슈 and before 변경점: it is something the room has
		// to act on, not something it only has to hear.
		waiting := section("DEPENDENCY", "타 조직 대기",
			"다른 조직의 업무가 끝나야 진행되는 업무입니다. 이 자리에서 담당자를 연결하십시오.", blocked)
		at := len(view.Sections)
		for index, existing := range view.Sections {
			if existing.Key == "CHANGE" {
				at = index
				break
			}
		}
		view.Sections = append(view.Sections[:at], append([]meetingSection{waiting}, view.Sections[at:]...)...)
	}
	view.Scope = scope
	writeData(w, http.StatusOK, view)
}

// ---------------------------------------------------------------------------
// Executive digest
// ---------------------------------------------------------------------------

// digestGround is one reason an item is in the digest, and what that reason
// contributed to its score.
//
// The points used to stay behind: the response carried a total and a list of
// sentences, so "왜 이게 1위인가" meant reading six sentences per entry and
// comparing them by hand. Carrying the split lets the screen draw the score as
// what it is — a sum of named parts — and keeps the claim falsifiable, which is
// the whole reason this digest is arithmetic and not a model output.
type digestGround struct {
	// Kind is DECISION, ISSUE, STALLED, BLOCKED, SILENT, DUPLICATE or DONE. The
	// screen colours by it, so the same reason is the same colour in every
	// entry. BLOCKED is a stall whose cause somebody declared; it scores the
	// same as STALLED because the work is equally stopped, and reads
	// differently because the action is not.
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Points int    `json:"points"`
}

type digestEntry struct {
	Kind             string         `json:"kind"`
	Score            int            `json:"score"`
	Title            string         `json:"title"`
	WorkItemID       int64          `json:"workItemId"`
	DisplayName      string         `json:"displayName"`
	OrganizationName string         `json:"organizationName"`
	Headline         string         `json:"headline"`
	Detail           string         `json:"detail"`
	Grounds          []digestGround `json:"grounds"`
}

type digestView struct {
	Weeks      int    `json:"weeks"`
	Since      string `json:"since"`
	Scope      string `json:"scope"`
	Considered int    `json:"considered"`
	// Considered is how many tasks were looked at. EquallyUrgent is how many
	// scored at least as high as the lowest entry shown — when it exceeds the
	// number of entries, the cut fell through a group of equals and the reader
	// is seeing some of them rather than all. Entries carries at most Limit.
	EquallyUrgent int           `json:"equallyUrgent"`
	Limit         int           `json:"limit"`
	Entries       []digestEntry `json:"entries"`
}

// buildDigest scores every task worth an executive's attention and orders them.
// It does not cut the list — capDigest does, so that the number it cut can be
// reported beside what survived.
//
// The score is a sum of independently observable facts, never a model output,
// and every contribution is listed back to the reader as grounds. An executive
// who cannot see why an item was selected has no way to disagree with it, and
// an unfalsifiable summary is worse than none.
func buildDigest(items []workItemView, duplicateByItem map[int64]workLink, blocked map[int64]blockedNote, cfg rollupConfig) []digestEntry {
	entries := []digestEntry{}
	for _, item := range items {
		score := 0
		grounds := []digestGround{}
		kind := ""
		headline := ""
		// Points and their reason are added together and cannot drift apart.
		// The screen draws the score as a bar made of these parts, so a total
		// its grounds do not add up to would be a bar that lies.
		add := func(points int, groundKind, text string) {
			score += points
			grounds = append(grounds, digestGround{Kind: groundKind, Text: text, Points: points})
		}

		if ask := strings.TrimSpace(item.LatestManagementAsk); ask != "" {
			add(digestWeightDecision, "DECISION", "상위 조직 결정·자원 요청이 열려 있습니다.")
			kind, headline = "DECISION", "결정 대기"
		}
		// Issue weeks are historical. Counting them for finished work reported
		// the completed task as an open risk, which is the opposite of true.
		if item.IssueRunWeeks >= cfg.PersistentIssueWeeks && !item.Completed {
			add(digestWeightIssuePerWeek*item.IssueRunWeeks, "ISSUE", fmt.Sprintf("이슈가 %d주째 지속되고 있습니다.", item.IssueRunWeeks))
			if kind == "" {
				kind, headline = "RISK", "장기 이슈"
			}
		}
		if item.Stalled && !item.Completed {
			// A stall with a declared cause is a different item of business. An
			// executive reading 진척 정체 goes and asks the owner; reading
			// 타 조직 대기 they connect two teams instead. Same stopped work,
			// different action, so it must not read the same.
			if note, waiting := blocked[item.ID]; waiting {
				add(digestWeightStalledPerWeek*item.StalledWeeks, "BLOCKED",
					fmt.Sprintf("진척이 %d주째 멈춰 있고, %s", item.StalledWeeks, note.sentence()))
				if kind == "" {
					if note.CrossOrg {
						kind, headline = "RISK", "타 조직 대기"
					} else {
						kind, headline = "RISK", "선행 업무 대기"
					}
				}
			} else {
				add(digestWeightStalledPerWeek*item.StalledWeeks, "STALLED", fmt.Sprintf("진척이 %d주째 멈춰 있습니다.", item.StalledWeeks))
				if kind == "" {
					kind, headline = "RISK", "진척 정체"
				}
			}
		}
		// A deadline that has arrived, or one the reported pace does not reach.
		//
		// This is the only ground here that is partly a projection, and the two
		// halves are scored differently on purpose. OVERDUE is observed — the
		// date passed and the work is not finished — so it escalates with how
		// long it has been true. AT_RISK is arithmetic on a pace, so it gets a
		// flat figure: letting an estimate's size drive the ranking would put
		// a projection above the observations it sits beside.
		//
		// SPLIT is deliberately absent. "one pace makes it and the other does
		// not" is a real finding on the tracking screen, where the reader can
		// look at both. In a briefing it is a maybe, and a briefing full of
		// maybes stops being read.
		switch item.DueOutlook.Kind {
		case dueOutlookOverdue:
			late := -item.DueOutlook.WeeksLeft
			if late < 0 {
				late = 0
			}
			add(digestWeightOverdue+digestWeightOverduePerWeek*late, "DEADLINE", fmt.Sprintf("마감일 %s이(가) 지났고 진척은 %d%%입니다.", item.DueOutlook.DueDate, item.Progress))
			if kind == "" {
				kind, headline = "RISK", "기한 초과"
			}
		case dueOutlookAtRisk:
			add(digestWeightDueAtRisk, "DEADLINE", fmt.Sprintf("마감일은 %s입니다. %s", item.DueOutlook.DueDate, item.DueOutlook.Note))
			if kind == "" {
				kind, headline = "RISK", "기한 초과 예상"
			}
		}
		if item.SilentWeeks > 0 && !item.Completed {
			add(digestWeightSilentPerWeek*item.SilentWeeks, "SILENT", fmt.Sprintf("%d주간 보고가 누락됐습니다.", item.SilentWeeks))
		}
		if link, duplicated := duplicateByItem[item.ID]; duplicated {
			add(digestWeightDuplicate, "DUPLICATE", fmt.Sprintf("%s의 '%s'와(과) 중복 가능성이 있습니다.",
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
			add(digestWeightDone+digestWeightDonePerWeek*item.ReportedWeeks, "DONE", fmt.Sprintf("%d주간 진행한 업무가 완료됐습니다.", item.ReportedWeeks))
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
	return entries
}

// capDigest keeps what a briefing can carry and says how many were its equal.
//
// The cap used to be the last line of buildDigest and what it cut was thrown
// away, so the screen said 핵심 10건 · 업무 543건 검토 whether ten items had been
// picked out of eleven or out of five hundred. Every sibling screen already
// says when it truncated — the agenda, the change summary, the insight lists —
// and this is the one an executive reads.
//
// The number reported is not how many scored at all: on a real deployment 468
// of 543 tasks carry some ground, most of them a single +25, and "468건 중
// 10건" would alarm in the wrong direction. It is how many scored at least as
// high as the lowest one shown — the ones the cut could just as well have kept
// instead. That is the only part of the tail a reader can act on, and when the
// list is not cut at all it equals the number of entries.
func capDigest(entries []digestEntry) (kept []digestEntry, equallyUrgent int) {
	if len(entries) <= digestMaximumEntries {
		return entries, len(entries)
	}
	kept = entries[:digestMaximumEntries]
	lowest := kept[len(kept)-1].Score
	equallyUrgent = len(kept)
	for _, entry := range entries[digestMaximumEntries:] {
		if entry.Score < lowest {
			// Sorted by score, so the first one below the line ends it.
			break
		}
		equallyUrgent++
	}
	return kept, equallyUrgent
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
	// The window says which tasks to consider — what has moved lately — and it
	// was also cutting the history the durations are measured in. So the two
	// sentences a reader ranks by reported the window back to them: choose four
	// weeks and every long-standing issue "8주째" became "4주째". Measured on a
	// deployment, work item 1622 read 이슈 22주 · 정체 32주 on the 업무 추적
	// screen and "이슈가 8주째 지속되고 있습니다" in the digest, on the same
	// day, for the same task. The score is built from those numbers too, so a
	// 32-week stall ranked level with an 8-week one.
	//
	// Loaded whole and filtered to the window afterwards: the candidate set is
	// the same tasks it always was, and the durations are the real ones. The
	// 업무 추적 screen already loads this much at this scope — measured at 77ms
	// against 75ms for the windowed load, on 300 people.
	loaded, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, false), "")
	if err != nil {
		a.logger.Error("executive digest", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "경영 요약을 만들 수 없습니다.")
		return
	}
	items := make([]workItemView, 0, len(loaded))
	for _, item := range loaded {
		if item.LastWeek >= since {
			items = append(items, item)
		}
	}
	// Every duplicate counts here, not only the ones a ranked list would show:
	// the digest scores each task, so a task whose link fell outside a cap
	// would quietly drop out of the top ten.
	graph := linkWorkItems(items, insightLinkLimit)
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	blocked, err := a.blockedNotes(r.Context(), ids)
	if err != nil {
		a.logger.Error("digest dependencies", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "경영 요약을 만들 수 없습니다.")
		return
	}
	entries, equallyUrgent := capDigest(buildDigest(items, graph.DuplicateByItem, blocked, a.rollupConfig(r.Context())))
	view := digestView{Weeks: weeks, Since: since, Scope: scopeTeam, Considered: len(items),
		Entries: entries, EquallyUrgent: equallyUrgent, Limit: digestMaximumEntries}
	writeData(w, http.StatusOK, view)
}
