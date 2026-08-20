package app

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// WorkItem is the identity of a task across the weeks it is reported in.
// ReportItem stays the weekly snapshot; everything derived about the task's
// life is computed from those snapshots so the two can never drift apart.

type workItemWeek struct {
	WeekStart string `json:"weekStart"`
	ReportID  int64  `json:"reportId"`
	// ItemIDs are the report item rows behind this week. Usually one, but two
	// when the same task was written twice in a week. Splitting a task apart
	// needs to address those rows, and the week is the only handle a reader of
	// this view has on them.
	ItemIDs       []int64 `json:"itemIds"`
	Progress      int     `json:"progress"`
	CurrentResult string  `json:"currentResult"`
	NextPlan      string  `json:"nextPlan"`
	Issue         string  `json:"issue"`
	ManagementAsk string  `json:"managementAsk"`
}

type workItemView struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	UserID      int64  `json:"userId"`
	DisplayName string `json:"displayName"`
	DueDate     string `json:"dueDate,omitempty"`
	// Organization is carried so cross-team analysis does not need a second
	// query with a different join and a different idea of who owns what.
	OrganizationID   *int64 `json:"organizationId,omitempty"`
	OrganizationName string `json:"organizationName,omitempty"`

	FirstWeek string `json:"firstWeek"`
	LastWeek  string `json:"lastWeek"`
	// ReportedWeeks counts the weeks the task actually appeared in; AgeWeeks is
	// the calendar span. The gap between them is how often it went unreported.
	ReportedWeeks int `json:"reportedWeeks"`
	AgeWeeks      int `json:"ageWeeks"`
	SilentWeeks   int `json:"silentWeeks"`

	Progress      int `json:"progress"`
	StartProgress int `json:"startProgress"`
	ProgressGain  int `json:"progressGain"`
	StalledWeeks  int `json:"stalledWeeks"`
	IssueWeeks    int `json:"issueWeeks"`
	RepeatedPlan  int `json:"repeatedPlan"`

	Completed bool `json:"completed"`
	Stalled   bool `json:"stalled"`
	AtRisk    bool `json:"atRisk"`
	Carryover bool `json:"carryover"`

	LatestIssue         string         `json:"latestIssue"`
	LatestManagementAsk string         `json:"latestManagementAsk"`
	Weeks               []workItemWeek `json:"weeks"`
}

// resolveWorkItem returns the identity for a task title, creating it when the
// owner reports it for the first time. Saving a renamed title renames the task,
// which matches how the period rollup already treats the most recent wording.
//
// A title that resolves to a work item the owner has merged away returns the
// item it was merged into. Without that step the merge would last exactly until
// the next report used the old wording, because identity is re-derived from the
// title on every save.
func resolveWorkItem(ctx context.Context, tx pgx.Tx, userID int64, title, category string) (*int64, error) {
	key := candidateTitleKey(title)
	if key == "" {
		// Nothing normalizable to key on, so the item stays unlinked rather
		// than being merged into an arbitrary neighbour.
		return nil, nil
	}
	var id int64
	var mergedInto *int64
	err := tx.QueryRow(ctx, `INSERT INTO work_items(user_id,title,normalized_key,category)
		VALUES($1,$2,$3,$4)
		ON CONFLICT (user_id, normalized_key)
		DO UPDATE SET title=EXCLUDED.title, category=EXCLUDED.category, updated_at=now()
		RETURNING id, merged_into_id`,
		userID, trimRunes(strings.TrimSpace(title), 240), trimRunes(key, 240), trimRunes(strings.TrimSpace(category), 80)).Scan(&id, &mergedInto)
	if err != nil {
		return nil, err
	}
	if mergedInto != nil {
		// One hop is enough: merging flattens any pointer that aimed at the
		// source, so a chain never forms and a cycle cannot be built.
		return mergedInto, nil
	}
	return &id, nil
}

// backfillBatchSize is how many report items one backfill transaction covers.
// Small enough that a batch commits in well under a second, large enough that
// the corpus does not turn into a transaction per row.
const backfillBatchSize = 500

// backfillWorkItems gives existing report items an identity using the same
// normalizer the runtime path uses. It only touches rows that have none, so it
// is safe to run on every start and is a no-op once complete.
//
// It commits in batches, in the background, and both matter. As one transaction
// over the whole corpus it took 34 seconds for 50,000 items with the server not
// yet listening — past the point where Kubernetes' default liveness probe
// restarts the pod, and every restart began again from nothing, so a large
// deployment could never finish. Per-batch commits make the progress durable.
func (a *App) backfillWorkItems(ctx context.Context) {
	var pending int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM report_items WHERE work_item_id IS NULL`).Scan(&pending); err != nil || pending == 0 {
		return
	}
	a.logger.Info("work item backfill started", "pending", pending)
	linked, scanned := 0, 0
	for {
		if ctx.Err() != nil {
			a.logger.Info("work item backfill stopped", "linked", linked, "scanned", scanned)
			return
		}
		batchLinked, batchScanned, err := a.backfillWorkItemBatch(ctx, backfillBatchSize)
		if err != nil {
			a.logger.Error("backfill work items", "error", err, "linked", linked, "scanned", scanned)
			return
		}
		linked += batchLinked
		scanned += batchScanned
		if batchScanned < backfillBatchSize {
			break
		}
		a.logger.Info("work item backfill progress", "linked", linked, "scanned", scanned, "pending", pending)
	}
	a.logger.Info("work item backfill complete", "linked", linked, "scanned", scanned)
}

// backfillWorkItemBatch links up to limit items and commits them together.
//
// An item with no normalizable title keeps its empty link, and the batch after
// it starts from the same place, so the loop would never move past one. The
// caller stops when a batch comes back short, which happens as soon as the only
// rows left are those.
func (a *App) backfillWorkItemBatch(ctx context.Context, limit int) (int, int, error) {
	rows, err := a.db.Query(ctx, `SELECT i.id, r.user_id, i.title, i.category
		FROM report_items i JOIN weekly_reports r ON r.id=i.report_id
		WHERE i.work_item_id IS NULL ORDER BY r.week_start, i.id LIMIT $1`, limit)
	if err != nil {
		return 0, 0, err
	}
	type pendingItem struct {
		itemID   int64
		userID   int64
		title    string
		category string
	}
	items := []pendingItem{}
	for rows.Next() {
		var item pendingItem
		if err := rows.Scan(&item.itemID, &item.userID, &item.title, &item.category); err != nil {
			rows.Close()
			return 0, 0, err
		}
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(items) == 0 {
		return 0, 0, nil
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	linked := 0
	for _, item := range items {
		workItemID, resolveErr := resolveWorkItem(ctx, tx, item.userID, item.title, item.category)
		if resolveErr != nil {
			return 0, 0, resolveErr
		}
		if workItemID == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE report_items SET work_item_id=$1 WHERE id=$2 AND work_item_id IS NULL`, *workItemID, item.itemID); err != nil {
			return 0, 0, err
		}
		linked++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return linked, len(items), nil
}

// listWorkItems answers "what has this task been doing, and for how long".
func (a *App) listWorkItems(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조직 단위 조회는 팀장 이상만 가능합니다.")
		return
	}
	result, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, scope == scopeSelf), "")
	if err != nil {
		a.logger.Error("list work items", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 조회할 수 없습니다.")
		return
	}
	// Surface the work that needs attention: open risk, then stalled, then the
	// longest running, then the least complete.
	sort.SliceStable(result, func(left, right int) bool {
		a, b := result[left], result[right]
		if a.AtRisk != b.AtRisk {
			return a.AtRisk
		}
		if a.Stalled != b.Stalled {
			return a.Stalled
		}
		if a.AgeWeeks != b.AgeWeeks {
			return a.AgeWeeks > b.AgeWeeks
		}
		return a.Progress < b.Progress
	})
	writeData(w, http.StatusOK, result)
}

// summarizeWorkItem derives the ageing figures from the weekly snapshots.
func summarizeWorkItem(item *workItemView, cfg rollupConfig) {
	if len(item.Weeks) == 0 {
		return
	}
	// One task can appear twice in a week if the author split it; keep the
	// furthest progress for that week so the history stays monotonic in intent.
	merged := []workItemWeek{}
	for _, week := range item.Weeks {
		if len(merged) > 0 && merged[len(merged)-1].WeekStart == week.WeekStart {
			last := &merged[len(merged)-1]
			if week.Progress > last.Progress {
				last.Progress = week.Progress
			}
			last.CurrentResult = mergeUniqueLines(last.CurrentResult, week.CurrentResult)
			last.NextPlan = mergeUniqueLines(last.NextPlan, week.NextPlan)
			last.Issue = mergeUniqueLines(last.Issue, week.Issue)
			last.ManagementAsk = mergeUniqueLines(last.ManagementAsk, week.ManagementAsk)
			last.ItemIDs = append(last.ItemIDs, week.ItemIDs...)
			continue
		}
		merged = append(merged, week)
	}
	item.Weeks = merged

	first, last := merged[0], merged[len(merged)-1]
	item.FirstWeek, item.LastWeek = first.WeekStart, last.WeekStart
	item.ReportedWeeks = len(merged)
	item.Progress, item.StartProgress = last.Progress, first.Progress
	item.ProgressGain = last.Progress - first.Progress
	item.Completed = last.Progress >= 100
	item.LatestIssue = strings.TrimSpace(last.Issue)
	item.LatestManagementAsk = strings.TrimSpace(last.ManagementAsk)
	item.Carryover = !item.Completed && strings.TrimSpace(last.NextPlan) != ""

	if start, err := time.Parse("2006-01-02", first.WeekStart); err == nil {
		if end, err := time.Parse("2006-01-02", last.WeekStart); err == nil {
			item.AgeWeeks = int(end.Sub(start).Hours()/(24*7)) + 1
		}
	}
	if item.AgeWeeks < item.ReportedWeeks {
		item.AgeWeeks = item.ReportedWeeks
	}
	item.SilentWeeks = item.AgeWeeks - item.ReportedWeeks

	for _, week := range merged {
		if strings.TrimSpace(week.Issue) != "" {
			item.IssueWeeks++
		}
	}
	// How many consecutive recent weeks repeated the same plan verbatim, which
	// is the strongest signal that a task is being restated rather than moved.
	planKey := lineKey(last.NextPlan)
	if planKey != "" {
		for index := len(merged) - 1; index >= 0; index-- {
			if lineKey(merged[index].NextPlan) != planKey {
				break
			}
			item.RepeatedPlan++
		}
	}
	// Consecutive recent weeks at an unchanged progress figure.
	if !item.Completed {
		for index := len(merged) - 1; index >= 0; index-- {
			if merged[index].Progress != last.Progress {
				break
			}
			item.StalledWeeks++
		}
	}
	item.Stalled = !item.Completed && item.StalledWeeks >= cfg.StallWeeks
	item.AtRisk = !item.Completed && item.IssueWeeks >= cfg.PersistentIssueWeeks
}

// persistReportItems reconciles the stored rows of a report with the submitted
// list. Rows the caller still references are updated in place, new rows are
// inserted, and rows that disappeared are removed.
//
// The previous implementation deleted every row and re-inserted, which issued
// fresh identifiers on each save. That is what makes a persistent work item
// link impossible: the column would be wiped by the next edit.
func (a *App) persistReportItems(ctx context.Context, tx pgx.Tx, reportID, ownerID int64, items []reportItem) error {
	type storedItem struct {
		title      string
		workItemID *int64
		pinned     bool
	}
	existing := map[int64]storedItem{}
	rows, err := tx.Query(ctx, `SELECT id, title, work_item_id, work_item_pinned FROM report_items WHERE report_id=$1`, reportID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var stored storedItem
		if err := rows.Scan(&id, &stored.title, &stored.workItemID, &stored.pinned); err != nil {
			rows.Close()
			return err
		}
		existing[id] = stored
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	kept := map[int64]bool{}
	for index, item := range items {
		stored, isExisting := existing[item.ID]
		// A pinned snapshot carries an identity its author chose by hand, so the
		// title normalizer does not get to overrule it. The pin only holds while
		// the title stands: rewriting the title is a statement about what the
		// task is, and re-deriving is the right answer again.
		pinned := item.ID > 0 && isExisting && stored.pinned &&
			strings.TrimSpace(stored.title) == strings.TrimSpace(item.Title)
		var workItemID *int64
		if pinned {
			workItemID = stored.workItemID
		} else {
			resolved, resolveErr := resolveWorkItem(ctx, tx, ownerID, item.Title, item.Category)
			if resolveErr != nil {
				return resolveErr
			}
			workItemID = resolved
		}
		if item.ID > 0 && isExisting {
			if _, err := tx.Exec(ctx, `UPDATE report_items SET work_item_id=$1,category=$2,title=$3,
				current_result=$4,next_plan=$5,issue=$6,management_ask=$7,progress=$8,sort_order=$9,
				work_item_pinned=$10,updated_at=now()
				WHERE id=$11 AND report_id=$12`,
				workItemID, item.Category, item.Title, item.CurrentResult, item.NextPlan,
				item.Issue, item.ManagementAsk, item.Progress, index, pinned, item.ID, reportID); err != nil {
				return err
			}
			kept[item.ID] = true
			continue
		}
		var inserted int64
		if err := tx.QueryRow(ctx, `INSERT INTO report_items(report_id,work_item_id,category,title,
			current_result,next_plan,issue,management_ask,progress,sort_order)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
			reportID, workItemID, item.Category, item.Title, item.CurrentResult, item.NextPlan,
			item.Issue, item.ManagementAsk, item.Progress, index).Scan(&inserted); err != nil {
			return err
		}
		kept[inserted] = true
	}

	for id := range existing {
		if kept[id] {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM report_items WHERE id=$1 AND report_id=$2`, id, reportID); err != nil {
			return err
		}
	}
	return nil
}
