package app

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Rollup aggregates approved weekly reporting into month, quarter, half-year and
// year views. Everything in this file is deterministic: the same weekly reports
// always produce the same rollup, so an offline site can reproduce a report
// without an AI gateway.

const (
	periodWeek    = "WEEK"
	periodMonth   = "MONTH"
	periodQuarter = "QUARTER"
	periodHalf    = "HALF"
	periodYear    = "YEAR"
)

type periodRange struct {
	Kind      string    `json:"kind"`
	Period    string    `json:"period"`
	Label     string    `json:"label"`
	Start     string    `json:"start"`
	End       string    `json:"end"`
	StartDate time.Time `json:"-"`
	EndDate   time.Time `json:"-"`
}

// rollupConfig carries the administrator-tunable thresholds so the aggregation
// functions stay pure and unit testable.
type rollupConfig struct {
	MergeSimilarity      int
	StallWeeks           int
	PersistentIssueWeeks int
}

func defaultRollupConfig() rollupConfig {
	return rollupConfig{MergeSimilarity: 80, StallWeeks: 2, PersistentIssueWeeks: 2}
}

// sourceEntry is one weekly report item feeding the rollup.
type sourceEntry struct {
	ReportID int64
	// WorkItemID is the stored identity of the task, including any merge or
	// split its owner made by hand. When it is present it decides the grouping
	// outright; the title is only a fallback for rows that never got one.
	WorkItemID    *int64
	UserID        int64
	DisplayName   string
	WeekStart     string
	Status        string
	Category      string
	Title         string
	CurrentResult string
	NextPlan      string
	Issue         string
	ManagementAsk string
	Progress      int
}

type rollupWeekPoint struct {
	WeekStart       string  `json:"weekStart"`
	Reports         int     `json:"reports"`
	Contributors    int     `json:"contributors"`
	ActiveItems     int     `json:"activeItems"`
	CompletedItems  int     `json:"completedItems"`
	NotStartedItems int     `json:"notStartedItems"`
	IssueItems      int     `json:"issueItems"`
	AverageProgress float64 `json:"averageProgress"`
}

type rollupItemWeek struct {
	WeekStart string `json:"weekStart"`
	Progress  int    `json:"progress"`
	HasIssue  bool   `json:"hasIssue"`
}

type rollupItem struct {
	Key           string `json:"key"`
	Category      string `json:"category"`
	Title         string `json:"title"`
	CurrentResult string `json:"currentResult"`
	NextPlan      string `json:"nextPlan"`
	Issue         string `json:"issue"`
	ManagementAsk string `json:"managementAsk"`
	Progress      int    `json:"progress"`
	StartProgress int    `json:"startProgress"`
	FirstWeek     string `json:"firstWeek"`
	LastWeek      string `json:"lastWeek"`
	WeekCount     int    `json:"weekCount"`
	IssueWeeks    int    `json:"issueWeeks"`
	// IssueRunWeeks is the issue still open now: the unbroken run of weeks
	// carrying one that ends at the last week of the period. IssueWeeks counts
	// every week that ever carried one, scattered or not.
	IssueRunWeeks int              `json:"issueRunWeeks"`
	Owners        []string         `json:"owners"`
	Weeks         []rollupItemWeek `json:"weeks,omitempty"`
	MergedTitles  []string         `json:"mergedTitles"`
	Completed     bool             `json:"completed"`
	Stalled       bool             `json:"stalled"`
	AtRisk        bool             `json:"atRisk"`
	Carryover     bool             `json:"carryover"`
	DuplicatesCut int              `json:"duplicatesCut"`
	// Forecast is arithmetic on Weeks, not a judgement. It carries the paces it
	// was computed from so a reader can dismiss it.
	Forecast completionForecast `json:"forecast"`
	// PeriodOutlook runs the same arithmetic against the boundary this report
	// is already about, so the question needs no deadline typed in anywhere.
	PeriodOutlook periodOutlook `json:"periodOutlook"`
}

type rollupCategory struct {
	Name            string  `json:"name"`
	Items           int     `json:"items"`
	Completed       int     `json:"completed"`
	AverageProgress float64 `json:"averageProgress"`
	Share           float64 `json:"share"`
	IssueItems      int     `json:"issueItems"`
}

type rollupContributor struct {
	UserID          int64   `json:"userId"`
	DisplayName     string  `json:"displayName"`
	Reports         int     `json:"reports"`
	Items           int     `json:"items"`
	Completed       int     `json:"completed"`
	IssueItems      int     `json:"issueItems"`
	AverageProgress float64 `json:"averageProgress"`
}

type rollupHighlight struct {
	Severity string `json:"severity"` // RISK | WATCH | GOOD | INFO
	Category string `json:"category"` // DELIVERY | RISK | COVERAGE | PORTFOLIO
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

type rollupInsights struct {
	TotalItems       int     `json:"totalItems"`
	CompletedItems   int     `json:"completedItems"`
	InProgressItems  int     `json:"inProgressItems"`
	NotStartedItems  int     `json:"notStartedItems"`
	CompletionRate   float64 `json:"completionRate"`
	AverageProgress  float64 `json:"averageProgress"`
	ProgressGain     float64 `json:"progressGain"`
	ContinuingItems  int     `json:"continuingItems"`
	OneOffItems      int     `json:"oneOffItems"`
	StalledItems     int     `json:"stalledItems"`
	NoLandingItems   int     `json:"noLandingItems"`
	MissesPeriod     int     `json:"missesPeriod"`
	CarryoverItems   int     `json:"carryoverItems"`
	IssueItems       int     `json:"issueItems"`
	PersistentIssues int     `json:"persistentIssues"`
	AskItems         int     `json:"askItems"`
	ExpectedWeeks    int     `json:"expectedWeeks"`
	ReportedWeeks    int     `json:"reportedWeeks"`
	ReportCoverage   float64 `json:"reportCoverage"`
	SourceReports    int     `json:"sourceReports"`
	SourceItems      int     `json:"sourceItems"`
	DuplicatesCut    int     `json:"duplicatesCut"`
	MergedTitles     int     `json:"mergedTitles"`
	DedupRate        float64 `json:"dedupRate"`
}

type rollupView struct {
	Kind         string              `json:"kind"`
	Period       string              `json:"period"`
	Label        string              `json:"label"`
	Start        string              `json:"start"`
	End          string              `json:"end"`
	Scope        string              `json:"scope"`
	ScopeLabel   string              `json:"scopeLabel"`
	Summary      string              `json:"summary"`
	Insights     rollupInsights      `json:"insights"`
	Highlights   []rollupHighlight   `json:"highlights"`
	Items        []rollupItem        `json:"items"`
	Categories   []rollupCategory    `json:"categories"`
	Contributors []rollupContributor `json:"contributors"`
	Trend        []rollupWeekPoint   `json:"trend"`
	Weeks        []string            `json:"weeks"`
	// Decisions taken inside the period, for the same people the rest of this
	// report covers. Capped, with the total beside it.
	Decisions     []decisionView `json:"decisions"`
	DecisionTotal int            `json:"decisionTotal"`
	OpenDecisions int            `json:"openDecisions"`
	DecisionLimit int            `json:"decisionLimit"`
	// IssueClearance is what the recorded endings say about how long obstacles
	// take to clear. Absent until somebody has answered the question.
	IssueClearance issueClearance `json:"issueClearance"`
	// TimelineItems is how many of Items carry their weekly series. The rest
	// are table rows and arrive without it.
	TimelineItems int       `json:"timelineItems"`
	GeneratedAt   time.Time `json:"generatedAt"`
}

// resolvePeriod turns a kind plus a user supplied period token into an inclusive
// date range. An empty period resolves to the period containing `now`.
// resolvePeriod turns a kind and an identifier into dated boundaries.
//
// weekStart names the day a week begins in this deployment, because WEEK is the
// one kind whose boundary is a setting rather than the calendar's. The others
// ignore it.
func resolvePeriod(kind, period string, now time.Time, weekStart string) (periodRange, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	period = strings.ToUpper(strings.TrimSpace(period))
	location := now.Location()
	year, month := now.Year(), int(now.Month())

	parseYear := func(value string) (int, error) {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1970 || parsed > 2999 {
			return 0, fmt.Errorf("invalid year")
		}
		return parsed, nil
	}

	switch kind {
	case periodWeek:
		// Identified by the Monday itself, in the same yyyy-mm-dd form that
		// weekly_reports.week_start already uses. Inventing an ISO week number
		// here would give the product a second way to name a week, and the two
		// would disagree the first time somebody moved the start day.
		start := currentWeekStart(now, weekStart)
		if period != "" {
			parsed, err := time.ParseInLocation("2006-01-02", period, location)
			if err != nil {
				return periodRange{}, fmt.Errorf("invalid week")
			}
			// Snapped rather than trusted: a caller who passes a Wednesday means
			// the week containing it, and answering with a Wednesday-to-Tuesday
			// window would silently disagree with every report in the database.
			start = currentWeekStart(parsed, weekStart)
		}
		return periodRange{
			Kind: periodWeek, Period: start.Format("2006-01-02"),
			Label:     fmt.Sprintf("%s 주차", start.Format("2006-01-02")),
			StartDate: start, EndDate: start.AddDate(0, 0, 6),
		}.normalized(), nil

	case periodMonth:
		if period != "" {
			parts := strings.Split(period, "-")
			if len(parts) != 2 {
				return periodRange{}, fmt.Errorf("invalid month")
			}
			parsedYear, err := parseYear(parts[0])
			if err != nil {
				return periodRange{}, err
			}
			parsedMonth, err := strconv.Atoi(parts[1])
			if err != nil || parsedMonth < 1 || parsedMonth > 12 {
				return periodRange{}, fmt.Errorf("invalid month")
			}
			year, month = parsedYear, parsedMonth
		}
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, location)
		return periodRange{
			Kind: periodMonth, Period: fmt.Sprintf("%04d-%02d", year, month),
			Label:     fmt.Sprintf("%d년 %d월", year, month),
			StartDate: start, EndDate: start.AddDate(0, 1, -1),
		}.normalized(), nil

	case periodQuarter:
		quarter := (month-1)/3 + 1
		if period != "" {
			parts := strings.Split(period, "-Q")
			if len(parts) != 2 {
				return periodRange{}, fmt.Errorf("invalid quarter")
			}
			parsedYear, err := parseYear(parts[0])
			if err != nil {
				return periodRange{}, err
			}
			parsedQuarter, err := strconv.Atoi(parts[1])
			if err != nil || parsedQuarter < 1 || parsedQuarter > 4 {
				return periodRange{}, fmt.Errorf("invalid quarter")
			}
			year, quarter = parsedYear, parsedQuarter
		}
		start := time.Date(year, time.Month((quarter-1)*3+1), 1, 0, 0, 0, 0, location)
		return periodRange{
			Kind: periodQuarter, Period: fmt.Sprintf("%04d-Q%d", year, quarter),
			Label:     fmt.Sprintf("%d년 %d분기", year, quarter),
			StartDate: start, EndDate: start.AddDate(0, 3, -1),
		}.normalized(), nil

	case periodHalf:
		half := 1
		if month > 6 {
			half = 2
		}
		if period != "" {
			parts := strings.Split(period, "-H")
			if len(parts) != 2 {
				return periodRange{}, fmt.Errorf("invalid half")
			}
			parsedYear, err := parseYear(parts[0])
			if err != nil {
				return periodRange{}, err
			}
			parsedHalf, err := strconv.Atoi(parts[1])
			if err != nil || parsedHalf < 1 || parsedHalf > 2 {
				return periodRange{}, fmt.Errorf("invalid half")
			}
			year, half = parsedYear, parsedHalf
		}
		start := time.Date(year, time.Month((half-1)*6+1), 1, 0, 0, 0, 0, location)
		names := map[int]string{1: "상반기", 2: "하반기"}
		return periodRange{
			Kind: periodHalf, Period: fmt.Sprintf("%04d-H%d", year, half),
			Label:     fmt.Sprintf("%d년 %s", year, names[half]),
			StartDate: start, EndDate: start.AddDate(0, 6, -1),
		}.normalized(), nil

	case periodYear:
		if period != "" {
			parsedYear, err := parseYear(period)
			if err != nil {
				return periodRange{}, err
			}
			year = parsedYear
		}
		start := time.Date(year, time.January, 1, 0, 0, 0, 0, location)
		return periodRange{
			Kind: periodYear, Period: fmt.Sprintf("%04d", year),
			Label:     fmt.Sprintf("%d년", year),
			StartDate: start, EndDate: start.AddDate(1, 0, -1),
		}.normalized(), nil
	}
	return periodRange{}, fmt.Errorf("invalid period kind")
}

func (p periodRange) normalized() periodRange {
	p.Start = p.StartDate.Format("2006-01-02")
	p.End = p.EndDate.Format("2006-01-02")
	return p
}

// expectedWeekStarts lists every reporting week whose 7 day span overlaps the
// period. It is the denominator for reporting coverage.
func expectedWeekStarts(period periodRange, weekday string) []string {
	first := currentWeekStart(period.StartDate, weekday)
	result := []string{}
	for cursor := first; !cursor.After(period.EndDate); cursor = cursor.AddDate(0, 0, 7) {
		if cursor.AddDate(0, 0, 6).Before(period.StartDate) {
			continue
		}
		result = append(result, cursor.Format("2006-01-02"))
	}
	return result
}

// ---------------------------------------------------------------------------
// Text level de-duplication
// ---------------------------------------------------------------------------

// lineKey normalizes a bullet line so that "• API 설계", "- api 설계" and
// "API설계" collapse onto a single entry.
func lineKey(value string) string {
	value = strings.ToLower(stripListMarker(value))
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// textLines splits a stored field into trimmed, marker-free lines.
func textLines(value string) []string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	result := []string{}
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(stripListMarker(line)); line != "" {
			result = append(result, line)
		}
	}
	return result
}

// lineSet accumulates unique lines in insertion order and counts how many
// duplicates it absorbed, which is what the UI reports as "중복 제거".
type lineSet struct {
	seen    map[string]bool
	lines   []string
	dropped int
}

func newLineSet() *lineSet { return &lineSet{seen: map[string]bool{}} }

func (s *lineSet) add(value string) {
	for _, line := range textLines(value) {
		key := lineKey(line)
		if key == "" {
			continue
		}
		if s.seen[key] {
			s.dropped++
			continue
		}
		s.seen[key] = true
		s.lines = append(s.lines, line)
	}
}

func (s *lineSet) has(value string) bool { return s.seen[lineKey(value)] }

// covers reports whether this set already accounts for the given line. A plan
// recorded as "성능 시험" is delivered once a result line reads "성능 시험 1차",
// so containment counts as well as an exact match. Short keys only match
// exactly, otherwise a two letter plan would be swallowed by any longer result.
func (s *lineSet) covers(value string) bool {
	key := lineKey(value)
	if key == "" {
		return false
	}
	if s.seen[key] {
		return true
	}
	if len([]rune(key)) < 4 {
		return false
	}
	for candidate := range s.seen {
		if strings.Contains(candidate, key) {
			return true
		}
	}
	return false
}

func (s *lineSet) render() string {
	if len(s.lines) == 0 {
		return ""
	}
	if len(s.lines) == 1 {
		return s.lines[0]
	}
	return "• " + strings.Join(s.lines, "\n• ")
}

// ---------------------------------------------------------------------------
// Title level de-duplication
// ---------------------------------------------------------------------------

// titleTokens splits a title into comparison tokens. Single character tokens are
// kept: they are often the only thing telling two tasks apart ("서버 A 점검" and
// "서버 B 점검", "업무 1" and "업무 2"), and dropping them merged distinct work.
func titleTokens(value string) map[string]bool {
	result := map[string]bool{}
	for _, field := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		result[field] = true
	}
	return result
}

// titleSimilarity scores two task titles from 0-100. It is the Jaccard overlap
// of the token sets, except that a title fully contained in a slightly longer
// one scores 100: renaming "AI 게이트웨이 PoC" to "AI 게이트웨이 PoC 검증" is the
// same task, but a short generic title must not swallow a much longer one.
func titleSimilarity(left, right string) int {
	return tokenSimilarity(titleTokens(left), titleTokens(right))
}

// tokenSimilarity is the same score over token sets a caller has already built,
// for loops that would otherwise tokenize the same titles over and over.
func tokenSimilarity(leftTokens, rightTokens map[string]bool) int {
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	shared := 0
	for token := range leftTokens {
		if rightTokens[token] {
			shared++
		}
	}
	smaller, larger := len(leftTokens), len(rightTokens)
	if smaller > larger {
		smaller, larger = larger, smaller
	}
	if shared == smaller && smaller >= 2 && larger-smaller <= 2 {
		return 100
	}
	union := len(leftTokens) + len(rightTokens) - shared
	if union == 0 {
		return 0
	}
	return shared * 100 / union
}

// ---------------------------------------------------------------------------
// Aggregation
// ---------------------------------------------------------------------------

type itemAccumulator struct {
	key          string
	category     string
	title        string
	titles       map[string]bool
	mergedTitles []string
	current      *lineSet
	next         *lineSet
	issue        *lineSet
	ask          *lineSet
	owners       []string
	ownerSeen    map[string]bool
	// ownerIDs is what the fuzzy title rule consults. Work item identity is per
	// owner, so a guess is only allowed to reach across people.
	ownerIDs      map[int64]bool
	weeks         []rollupItemWeek
	weekSeen      map[string]int
	firstProgress int
	lastProgress  int
	lastWeek      string
	issueWeeks    int
}

func newAccumulator(key string, entry sourceEntry) *itemAccumulator {
	return &itemAccumulator{
		key: key, category: entry.Category, title: entry.Title,
		titles: map[string]bool{}, current: newLineSet(), next: newLineSet(), issue: newLineSet(), ask: newLineSet(),
		ownerSeen: map[string]bool{}, ownerIDs: map[int64]bool{}, weekSeen: map[string]int{}, firstProgress: entry.Progress,
	}
}

func (acc *itemAccumulator) absorb(entry sourceEntry) {
	acc.ownerIDs[entry.UserID] = true
	// The most recent week wins for the human readable label, so a renamed task
	// shows its current name while still carrying the whole history.
	if entry.WeekStart >= acc.lastWeek {
		if strings.TrimSpace(entry.Title) != "" {
			acc.title = strings.TrimSpace(entry.Title)
		}
		if strings.TrimSpace(entry.Category) != "" {
			acc.category = strings.TrimSpace(entry.Category)
		}
		acc.lastProgress = entry.Progress
		acc.lastWeek = entry.WeekStart
	}
	normalized := strings.TrimSpace(entry.Title)
	if normalized != "" && !acc.titles[normalized] {
		acc.titles[normalized] = true
		acc.mergedTitles = append(acc.mergedTitles, normalized)
	}
	acc.current.add(entry.CurrentResult)
	acc.next.add(entry.NextPlan)
	acc.issue.add(entry.Issue)
	acc.ask.add(entry.ManagementAsk)
	if !acc.ownerSeen[entry.DisplayName] && strings.TrimSpace(entry.DisplayName) != "" {
		acc.ownerSeen[entry.DisplayName] = true
		acc.owners = append(acc.owners, entry.DisplayName)
	}
	hasIssue := strings.TrimSpace(entry.Issue) != ""
	if index, ok := acc.weekSeen[entry.WeekStart]; ok {
		// Same task listed twice in one week: keep the furthest progress.
		if entry.Progress > acc.weeks[index].Progress {
			acc.weeks[index].Progress = entry.Progress
		}
		acc.weeks[index].HasIssue = acc.weeks[index].HasIssue || hasIssue
		return
	}
	acc.weekSeen[entry.WeekStart] = len(acc.weeks)
	acc.weeks = append(acc.weeks, rollupItemWeek{WeekStart: entry.WeekStart, Progress: entry.Progress, HasIssue: hasIssue})
	if hasIssue {
		acc.issueWeeks++
	}
}

// aggregateRollupItems merges weekly entries into de-duplicated period items.
func aggregateRollupItems(entries []sourceEntry, cfg rollupConfig) []rollupItem {
	sorted := make([]sourceEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].WeekStart != sorted[j].WeekStart {
			return sorted[i].WeekStart < sorted[j].WeekStart
		}
		return sorted[i].Title < sorted[j].Title
	})

	canonical, split := workItemKeys(sorted)

	order := []string{}
	byKey := map[string]*itemAccumulator{}
	for _, entry := range sorted {
		key, corrected := rollupKey(entry, canonical, split)
		if key == "" {
			continue
		}
		if _, ok := byKey[key]; !ok {
			// Exact normalization missed it; try a conservative fuzzy match so
			// "AI 게이트웨이 PoC" and "AI 게이트웨이 PoC 검증" become one task.
			// Skipped for a task its owner has split apart, since the fuzzy rule
			// compares titles and the two halves usually still share one.
			if !corrected {
				if merged := fuzzyMatch(order, byKey, entry, cfg.MergeSimilarity); merged != "" {
					key = merged
				}
			}
			if _, ok := byKey[key]; !ok {
				byKey[key] = newAccumulator(key, entry)
				order = append(order, key)
			}
		}
		byKey[key].absorb(entry)
	}

	result := make([]rollupItem, 0, len(order))
	for _, key := range order {
		acc := byKey[key]
		// A plan that later shows up as delivered work is not an outstanding
		// plan any more, so drop it from the period level next steps.
		plans := []string{}
		planDropped := 0
		for _, line := range acc.next.lines {
			if acc.current.covers(line) {
				planDropped++
				continue
			}
			plans = append(plans, line)
		}
		nextPlan := ""
		if len(plans) == 1 {
			nextPlan = plans[0]
		} else if len(plans) > 1 {
			nextPlan = "• " + strings.Join(plans, "\n• ")
		}
		sort.SliceStable(acc.weeks, func(i, j int) bool { return acc.weeks[i].WeekStart < acc.weeks[j].WeekStart })
		item := rollupItem{
			Key: key, Category: acc.category, Title: acc.title,
			CurrentResult: acc.current.render(), NextPlan: nextPlan, Issue: acc.issue.render(), ManagementAsk: acc.ask.render(),
			Progress: acc.lastProgress, StartProgress: acc.firstProgress,
			FirstWeek: acc.weeks[0].WeekStart, LastWeek: acc.weeks[len(acc.weeks)-1].WeekStart,
			WeekCount: len(acc.weeks), IssueWeeks: acc.issueWeeks, Owners: acc.owners, Weeks: acc.weeks,
			MergedTitles:  acc.mergedTitles,
			DuplicatesCut: acc.current.dropped + acc.next.dropped + acc.issue.dropped + acc.ask.dropped + planDropped,
		}
		item.Completed = item.Progress >= 100
		item.Stalled = isStalled(acc.weeks, cfg.StallWeeks)
		// Risk is about work still in flight. Delivered work keeps its issue
		// history in IssueWeeks, but flagging it red would dilute the real
		// risk register that the reporting line acts on.
		//
		// It is the open run that decides, not the history. A task that raised
		// an issue in scattered weeks months ago and has reported clean since
		// is not at risk, and counting the history called it one.
		item.IssueRunWeeks = openIssueRun(acc.weeks)
		item.AtRisk = item.IssueRunWeeks >= cfg.PersistentIssueWeeks && !item.Completed
		item.Carryover = !item.Completed && strings.TrimSpace(nextPlan) != ""
		item.Forecast = forecastCompletion(acc.weeks, item.Progress)
		result = append(result, item)
	}
	// Surface the work that needs a decision first: risk, then stalled, then the
	// longest running tasks, then the least complete.
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.AtRisk != right.AtRisk {
			return left.AtRisk
		}
		if left.Stalled != right.Stalled {
			return left.Stalled
		}
		if left.WeekCount != right.WeekCount {
			return left.WeekCount > right.WeekCount
		}
		if left.Progress != right.Progress {
			return left.Progress < right.Progress
		}
		return left.Title < right.Title
	})
	return result
}

// ownerTitle is one person's version of one task name.
type ownerTitle struct {
	owner int64
	key   string
}

// workItemKeys decides how stored identity feeds the period grouping.
//
// Identity is per owner, and a period report exists to show one task once for
// the whole organisation rather than once per person, so the work item cannot
// simply become the grouping key. It is used for the two things a title cannot
// express, both of which are corrections its owner made by hand:
//
//   - a merge, where one work item carries two different titles: every entry
//     takes the work item's most recent title, so both spellings land together
//     and other people's reports of the same task join them;
//   - a split, where one owner has one title under two work items: those entries
//     get the work item appended so they stay apart.
//
// Everything else keys on the title exactly as before.
func workItemKeys(sorted []sourceEntry) (map[int64]string, map[ownerTitle]map[int64]bool) {
	canonical := map[int64]string{}
	for _, entry := range sorted {
		if entry.WorkItemID == nil {
			continue
		}
		if key := titleGroupKey(entry.Title); key != "" {
			// sorted runs oldest first, so the last write is the latest wording.
			canonical[*entry.WorkItemID] = key
		}
	}
	split := map[ownerTitle]map[int64]bool{}
	for _, entry := range sorted {
		if entry.WorkItemID == nil {
			continue
		}
		seen := ownerTitle{owner: entry.UserID, key: canonical[*entry.WorkItemID]}
		if split[seen] == nil {
			split[seen] = map[int64]bool{}
		}
		split[seen][*entry.WorkItemID] = true
	}
	return canonical, split
}

// rollupKey returns the grouping key for one entry, and whether that key came
// from a correction its owner made rather than from the title alone.
func rollupKey(entry sourceEntry, canonical map[int64]string, split map[ownerTitle]map[int64]bool) (string, bool) {
	key := titleGroupKey(entry.Title)
	if entry.WorkItemID == nil {
		return key, false
	}
	if stored := canonical[*entry.WorkItemID]; stored != "" {
		key = stored
	}
	if key == "" {
		return "", false
	}
	if len(split[ownerTitle{owner: entry.UserID, key: key}]) > 1 {
		return fmt.Sprintf("%s#%d", key, *entry.WorkItemID), true
	}
	return key, false
}

func titleGroupKey(title string) string {
	if key := candidateTitleKey(title); key != "" {
		return key
	}
	return strings.ToLower(strings.TrimSpace(title))
}

// fuzzyMatch finds a task whose title is close enough to be the same work under
// a slightly different name.
//
// It refuses to merge two tasks the same person reported when both carry a
// stored identity. Within one owner that identity is the answer and is theirs to
// correct; guessing on top of it would let a period report contradict every
// other screen and re-join what they deliberately kept apart. Across owners
// nothing stored spans people, so the title is still the only evidence there is.
func fuzzyMatch(order []string, byKey map[string]*itemAccumulator, entry sourceEntry, threshold int) string {
	if threshold <= 0 || threshold > 100 {
		return ""
	}
	best, bestScore := "", 0
	for _, key := range order {
		accumulator := byKey[key]
		if entry.WorkItemID != nil && accumulator.ownerIDs[entry.UserID] {
			continue
		}
		score := titleSimilarity(accumulator.title, entry.Title)
		if score >= threshold && score > bestScore {
			best, bestScore = key, score
		}
	}
	return best
}

// isStalled reports work that reached the end of the period without moving for
// the configured number of weeks.
//
// Weeks on the calendar, not reports. Requiring N reports at the same figure
// meant the stricter an operator set the rule, the more it missed: a task last
// touched on 6 July and still reading 50% on 10 August has stood five weeks,
// and with the threshold at three it was not stalled — while a task somebody
// wrote up every week for three weeks was. The setting is meant to say "flag
// what has not moved in three weeks", and that is now what it says.
//
// Two reports are still the minimum. One report says nothing about the weeks
// before it, and calling that stalled would be inventing a history.
// openIssueRun measures the issue that is open at the end of the period: the
// unbroken run of weeks carrying one, counted on the calendar so a week nobody
// reported does not reset it. Zero when the last week carries no issue.
func openIssueRun(weeks []rollupItemWeek) int {
	if len(weeks) == 0 || !weeks[len(weeks)-1].HasIssue {
		return 0
	}
	last := weeks[len(weeks)-1]
	openFrom := last.WeekStart
	reports := 0
	for index := len(weeks) - 1; index >= 0; index-- {
		if !weeks[index].HasIssue {
			break
		}
		openFrom = weeks[index].WeekStart
		reports++
	}
	run := reports
	if from, err := time.Parse("2006-01-02", openFrom); err == nil {
		if to, err := time.Parse("2006-01-02", last.WeekStart); err == nil {
			if span := int(to.Sub(from).Hours()/(24*7)) + 1; span > run {
				run = span
			}
		}
	}
	return run
}

func isStalled(weeks []rollupItemWeek, threshold int) bool {
	if threshold < 2 || len(weeks) < 2 {
		return false
	}
	last := weeks[len(weeks)-1]
	if last.Progress >= 100 {
		return false
	}
	unchangedFrom := last.WeekStart
	for index := len(weeks) - 1; index >= 0; index-- {
		if weeks[index].Progress != last.Progress {
			break
		}
		unchangedFrom = weeks[index].WeekStart
	}
	if unchangedFrom == last.WeekStart {
		return false
	}
	// Inclusive, so three consecutive weeks reads as three — the same number the
	// report count gave for an unbroken run.
	return int(weekSpan(unchangedFrom, last.WeekStart, 0))+1 >= threshold
}

// buildRollup assembles the full period view including the executive insight
// layer that the reporting line reads before the item detail.
func buildRollup(period periodRange, scope, scopeLabel string, entries []sourceEntry, reports []reportListItem, expected []string, cfg rollupConfig) rollupView {
	items := aggregateRollupItems(entries, cfg)
	// Attached here rather than inside the aggregation, which is about merging
	// weekly entries and has no business knowing what window it is merging for.
	for index := range items {
		items[index].PeriodOutlook = outlookForPeriodEnd(period.End,
			items[index].Forecast, items[index].Weeks, items[index].Progress)
	}
	view := rollupView{
		Kind: period.Kind, Period: period.Period, Label: period.Label,
		Start: period.Start, End: period.End, Scope: scope, ScopeLabel: scopeLabel,
		Items: items, Highlights: []rollupHighlight{}, Categories: []rollupCategory{},
		Contributors: []rollupContributor{}, Trend: []rollupWeekPoint{}, Weeks: []string{},
	}

	insights := rollupInsights{
		TotalItems: len(items), SourceItems: len(entries), SourceReports: len(reports),
		ExpectedWeeks: len(expected),
	}
	progressSum, gainSum := 0, 0
	for _, item := range items {
		switch {
		case item.Completed:
			insights.CompletedItems++
		case item.Progress <= 0:
			insights.NotStartedItems++
		default:
			insights.InProgressItems++
		}
		if item.WeekCount > 1 {
			insights.ContinuingItems++
		} else {
			insights.OneOffItems++
		}
		if item.Stalled {
			insights.StalledItems++
		}
		if noLandingDate(item) {
			insights.NoLandingItems++
		}
		if missesThePeriod(item) {
			insights.MissesPeriod++
		}
		if item.Carryover {
			insights.CarryoverItems++
		}
		if strings.TrimSpace(item.Issue) != "" {
			insights.IssueItems++
		}
		if strings.TrimSpace(item.ManagementAsk) != "" {
			insights.AskItems++
		}
		if item.AtRisk {
			insights.PersistentIssues++
		}
		if len(item.MergedTitles) > 1 {
			insights.MergedTitles += len(item.MergedTitles) - 1
		}
		insights.DuplicatesCut += item.DuplicatesCut
		progressSum += item.Progress
		gainSum += item.Progress - item.StartProgress
	}
	if len(items) > 0 {
		insights.AverageProgress = round1(float64(progressSum) / float64(len(items)))
		insights.ProgressGain = round1(float64(gainSum) / float64(len(items)))
		insights.CompletionRate = round1(float64(insights.CompletedItems) * 100 / float64(len(items)))
	}
	if insights.SourceItems > 0 {
		insights.DedupRate = round1(float64(insights.SourceItems-insights.TotalItems) * 100 / float64(insights.SourceItems))
	}

	// Weekly trend, contributors and reported coverage.
	weekSeen := map[string]bool{}
	for _, report := range reports {
		weekSeen[report.WeekStart] = true
	}
	insights.ReportedWeeks = len(weekSeen)
	if insights.ExpectedWeeks > 0 {
		insights.ReportCoverage = round1(float64(insights.ReportedWeeks) * 100 / float64(insights.ExpectedWeeks))
	}
	view.Insights = insights
	view.Weeks = expected

	view.Trend = buildTrend(expected, entries, reports)
	view.Categories = buildCategories(items)
	view.Contributors = buildContributors(entries, reports)
	view.Highlights = buildHighlights(insights, items, cfg)
	view.Summary = buildSummary(period, scopeLabel, insights)
	return view
}

func buildTrend(expected []string, entries []sourceEntry, reports []reportListItem) []rollupWeekPoint {
	index := map[string]int{}
	trend := make([]rollupWeekPoint, 0, len(expected))
	for _, week := range expected {
		index[week] = len(trend)
		trend = append(trend, rollupWeekPoint{WeekStart: week})
	}
	progressSum := make([]int, len(trend))
	contributors := make([]map[int64]bool, len(trend))
	for position := range contributors {
		contributors[position] = map[int64]bool{}
	}
	for _, report := range reports {
		if position, ok := index[report.WeekStart]; ok {
			trend[position].Reports++
		}
	}
	for _, entry := range entries {
		position, ok := index[entry.WeekStart]
		if !ok {
			continue
		}
		trend[position].ActiveItems++
		progressSum[position] += entry.Progress
		contributors[position][entry.UserID] = true
		if entry.Progress >= 100 {
			trend[position].CompletedItems++
		} else if entry.Progress <= 0 {
			trend[position].NotStartedItems++
		}
		if strings.TrimSpace(entry.Issue) != "" {
			trend[position].IssueItems++
		}
	}
	for position := range trend {
		trend[position].Contributors = len(contributors[position])
		if trend[position].ActiveItems > 0 {
			trend[position].AverageProgress = round1(float64(progressSum[position]) / float64(trend[position].ActiveItems))
		}
	}
	return trend
}

func buildCategories(items []rollupItem) []rollupCategory {
	order := []string{}
	byName := map[string]*rollupCategory{}
	progress := map[string]int{}
	for _, item := range items {
		name := strings.TrimSpace(item.Category)
		if name == "" {
			name = "미분류"
		}
		if _, ok := byName[name]; !ok {
			byName[name] = &rollupCategory{Name: name}
			order = append(order, name)
		}
		byName[name].Items++
		progress[name] += item.Progress
		if item.Completed {
			byName[name].Completed++
		}
		if strings.TrimSpace(item.Issue) != "" {
			byName[name].IssueItems++
		}
	}
	result := make([]rollupCategory, 0, len(order))
	for _, name := range order {
		category := *byName[name]
		if category.Items > 0 {
			category.AverageProgress = round1(float64(progress[name]) / float64(category.Items))
			category.Share = round1(float64(category.Items) * 100 / float64(len(items)))
		}
		result = append(result, category)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Items > result[j].Items })
	return result
}

func buildContributors(entries []sourceEntry, reports []reportListItem) []rollupContributor {
	order := []int64{}
	byUser := map[int64]*rollupContributor{}
	progress := map[int64]int{}
	titles := map[int64]map[string]bool{}
	for _, entry := range entries {
		if _, ok := byUser[entry.UserID]; !ok {
			byUser[entry.UserID] = &rollupContributor{UserID: entry.UserID, DisplayName: entry.DisplayName}
			titles[entry.UserID] = map[string]bool{}
			order = append(order, entry.UserID)
		}
		key := candidateTitleKey(entry.Title)
		if !titles[entry.UserID][key] {
			titles[entry.UserID][key] = true
			byUser[entry.UserID].Items++
		}
		progress[entry.UserID] += entry.Progress
		if entry.Progress >= 100 {
			byUser[entry.UserID].Completed++
		}
		if strings.TrimSpace(entry.Issue) != "" {
			byUser[entry.UserID].IssueItems++
		}
	}
	counted := map[int64]int{}
	for _, report := range reports {
		counted[report.UserID]++
	}
	entryCount := map[int64]int{}
	for _, entry := range entries {
		entryCount[entry.UserID]++
	}
	result := make([]rollupContributor, 0, len(order))
	for _, id := range order {
		contributor := *byUser[id]
		contributor.Reports = counted[id]
		if total := entryCount[id]; total > 0 {
			contributor.AverageProgress = round1(float64(progress[id]) / float64(total))
		}
		result = append(result, contributor)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Items > result[j].Items })
	return result
}

// buildHighlights encodes the questions a reporting line asks first: did we
// deliver, what is stuck, what is at risk, and can we trust the data.
func buildHighlights(insights rollupInsights, items []rollupItem, cfg rollupConfig) []rollupHighlight {
	result := []rollupHighlight{}
	if insights.TotalItems == 0 {
		return append(result, rollupHighlight{Severity: "INFO", Category: "COVERAGE", Title: "취합할 주간보고가 없습니다",
			Detail: "이 기간에 등록된 주간보고가 없어 집계할 업무가 없습니다."})
	}

	switch {
	case insights.CompletionRate >= 70:
		result = append(result, rollupHighlight{Severity: "GOOD", Category: "DELIVERY", Title: fmt.Sprintf("완료율 %.1f%%", insights.CompletionRate),
			Detail: fmt.Sprintf("업무 %d건 중 %d건을 완료했습니다. 평균 진척도는 %.1f%%입니다.", insights.TotalItems, insights.CompletedItems, insights.AverageProgress)})
	case insights.CompletionRate < 30:
		result = append(result, rollupHighlight{Severity: "WATCH", Category: "DELIVERY", Title: fmt.Sprintf("완료율 %.1f%%", insights.CompletionRate),
			Detail: fmt.Sprintf("완료 %d건, 진행 %d건, 미착수 %d건입니다. 기간 내 마감 가능한 범위를 재조정할 시점입니다.", insights.CompletedItems, insights.InProgressItems, insights.NotStartedItems)})
	default:
		result = append(result, rollupHighlight{Severity: "INFO", Category: "DELIVERY", Title: fmt.Sprintf("완료율 %.1f%%", insights.CompletionRate),
			Detail: fmt.Sprintf("완료 %d건, 진행 %d건, 미착수 %d건입니다.", insights.CompletedItems, insights.InProgressItems, insights.NotStartedItems)})
	}

	if insights.PersistentIssues > 0 {
		names := topTitles(items, func(item rollupItem) bool { return item.AtRisk }, 3)
		result = append(result, rollupHighlight{Severity: "RISK", Category: "RISK",
			Title:  fmt.Sprintf("%d주 이상 이슈가 지속된 업무 %d건", cfg.PersistentIssueWeeks, insights.PersistentIssues),
			Detail: fmt.Sprintf("%s. 반복 보고된 이슈는 담당 조직만으로 해소되지 않으므로 상위 의사결정이 필요합니다.", strings.Join(names, ", "))})
	}
	if insights.StalledItems > 0 {
		names := topTitles(items, func(item rollupItem) bool { return item.Stalled }, 3)
		result = append(result, rollupHighlight{Severity: "WATCH", Category: "RISK",
			Title:  fmt.Sprintf("%d주 이상 진척이 멈춘 업무 %d건", cfg.StallWeeks, insights.StalledItems),
			Detail: fmt.Sprintf("%s. 진척도가 연속으로 변하지 않아 일정 재계획 또는 리소스 재배치 검토가 필요합니다.", strings.Join(names, ", "))})
	}
	// Work that finishes, but not inside the window this report covers.
	//
	// This is the question a period report is already asking, answered without
	// anybody having entered a deadline: the boundary comes from the request.
	// It is not a missed commitment — nobody promised this quarter — so it is
	// stated as what the numbers say and left for the reader to weigh.
	if insights.MissesPeriod > 0 {
		names := topTitles(items, missesThePeriod, 3)
		result = append(result, rollupHighlight{Severity: "WATCH", Category: "DELIVERY",
			Title:  fmt.Sprintf("이 속도로는 기간 안에 끝나지 않는 업무 %d건", insights.MissesPeriod),
			Detail: fmt.Sprintf("%s. 보고된 속도를 기간 말까지 이어 붙인 결과이며 약속된 마감과는 무관합니다. 각 업무의 예상 진척과 속도를 함께 확인하십시오.", strings.Join(names, ", "))})
	}
	// Work that is moving and still does not land. The stalled rule above only
	// catches progress that stopped, so a task creeping up a point a week reads
	// as healthy on every status board while its own numbers say it needs a
	// year. Only the cases where the arithmetic itself declined to name a
	// finishing week are listed, so there is no threshold invented here.
	if insights.NoLandingItems > 0 {
		names := topTitles(items, noLandingDate, 3)
		result = append(result, rollupHighlight{Severity: "WATCH", Category: "RISK",
			Title:  fmt.Sprintf("진행 중이지만 끝나는 시점이 보이지 않는 업무 %d건", insights.NoLandingItems),
			Detail: fmt.Sprintf("%s. 진척은 늘고 있으나 보고된 속도로는 완료 시점을 계산할 수 없습니다. 각 업무의 주차별 속도를 함께 확인하십시오.", strings.Join(names, ", "))})
	}
	if insights.AskItems > 0 {
		names := topTitles(items, func(item rollupItem) bool { return strings.TrimSpace(item.ManagementAsk) != "" }, 3)
		result = append(result, rollupHighlight{Severity: "RISK", Category: "RISK",
			Title:  fmt.Sprintf("상위 조직 결정·지원 요청 %d건", insights.AskItems),
			Detail: fmt.Sprintf("%s. 담당 조직이 스스로 해결할 수 없어 상위 결정이나 자원 배정이 필요한 항목입니다.", strings.Join(names, ", "))})
	}
	if insights.CarryoverItems > 0 {
		result = append(result, rollupHighlight{Severity: "INFO", Category: "DELIVERY",
			Title:  fmt.Sprintf("다음 기간 이월 업무 %d건", insights.CarryoverItems),
			Detail: "완료되지 않았고 후속 계획이 남아 있는 업무입니다. 다음 기간 계획의 기준선으로 사용하십시오."})
	}
	if insights.ExpectedWeeks > 0 {
		severity := "GOOD"
		if insights.ReportCoverage < 60 {
			severity = "RISK"
		} else if insights.ReportCoverage < 90 {
			severity = "WATCH"
		}
		result = append(result, rollupHighlight{Severity: severity, Category: "COVERAGE",
			Title:  fmt.Sprintf("보고 커버리지 %.1f%%", insights.ReportCoverage),
			Detail: fmt.Sprintf("기간 내 %d개 보고 주차 중 %d개 주차에 보고가 등록되었습니다. 커버리지가 낮으면 집계 결과의 신뢰도도 함께 낮아집니다.", insights.ExpectedWeeks, insights.ReportedWeeks)})
	}
	if insights.ContinuingItems > 0 {
		result = append(result, rollupHighlight{Severity: "INFO", Category: "PORTFOLIO",
			Title:  fmt.Sprintf("연속 수행 업무 %d건 · 단발 업무 %d건", insights.ContinuingItems, insights.OneOffItems),
			Detail: "연속 수행 업무는 기간 과제, 단발 업무는 운영성 대응으로 구분해 다음 기간 리소스 배분에 활용하십시오."})
	}
	if insights.DuplicatesCut > 0 || insights.MergedTitles > 0 {
		result = append(result, rollupHighlight{Severity: "INFO", Category: "PORTFOLIO",
			Title:  fmt.Sprintf("중복 %d건 제거 · 동일 업무 %d건 병합", insights.DuplicatesCut, insights.MergedTitles),
			Detail: fmt.Sprintf("주간보고 업무 %d건을 %d건으로 정리했습니다(중복률 %.1f%%).", insights.SourceItems, insights.TotalItems, insights.DedupRate)})
	}
	return result
}

func topTitles(items []rollupItem, match func(rollupItem) bool, limit int) []string {
	result := []string{}
	for _, item := range items {
		if !match(item) {
			continue
		}
		result = append(result, item.Title)
		if len(result) == limit {
			break
		}
	}
	return result
}

func buildSummary(period periodRange, scopeLabel string, insights rollupInsights) string {
	if insights.TotalItems == 0 {
		return fmt.Sprintf("%s %s 기간에 등록된 주간보고가 없습니다.", period.Label, scopeLabel)
	}
	return fmt.Sprintf("%s %s 주간보고 %d건을 취합해 업무 %d건으로 정리했습니다. 완료 %d건(완료율 %.1f%%), 평균 진척도 %.1f%%, 이슈 %d건, 이월 %d건입니다.",
		period.Label, scopeLabel, insights.SourceReports, insights.TotalItems,
		insights.CompletedItems, insights.CompletionRate, insights.AverageProgress,
		insights.IssueItems, insights.CarryoverItems)
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
}

// noLandingDate is work still in flight whose own reported pace does not yield
// a finishing week: either the projection ran past a year, or the overall and
// recent paces disagree so completely that one of them says never.
//
// Stalled work is deliberately excluded. It is already reported, by a rule that
// says something more specific, and naming the same task twice under two
// headings is how a risk list stops being read.
func noLandingDate(item rollupItem) bool {
	if item.Completed || item.Stalled {
		return false
	}
	return item.Forecast.Kind == forecastDistant ||
		(item.Forecast.Kind == forecastProjected && item.Forecast.LatestWeeks == 0)
}
