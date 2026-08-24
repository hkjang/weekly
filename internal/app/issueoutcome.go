package app

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Recording how an obstacle ended, and reading back how long that kind takes.
//
// The weekly rows already say an issue ran for N weeks. They cannot say whether
// it ended because the problem was solved or because the reporter stopped
// mentioning it, and those are opposite facts. So the editor asks once, at the
// only moment anybody knows the answer — the week the field goes blank — and
// what comes back is a duration the organisation can learn from.

const (
	issueOutcomeResolved = "RESOLVED"
)

func validIssueOutcome(value string) bool {
	return value == issueOutcomeResolved
}

// issueEpisode is the run of consecutive reported weeks that just ended.
type issueEpisode struct {
	StartedWeek string
	Weeks       int
	Text        string
}

// lastIssueEpisode walks back from before, week by week, for as long as this
// work item kept reporting an issue.
//
// Consecutive on purpose. A task that reported a problem in March, went quiet,
// and reported a different one in August has had two obstacles, not a
// five-month one, and counting the gap would inflate every figure built on this.
func lastIssueEpisode(ctx context.Context, tx pgx.Tx, workItemID int64, before string) (issueEpisode, bool, error) {
	rows, err := tx.Query(ctx, `SELECT r.week_start, i.issue
		FROM report_items i JOIN weekly_reports r ON r.id = i.report_id
		WHERE i.work_item_id = $1 AND r.week_start < $2
		ORDER BY r.week_start DESC
		LIMIT 260`, workItemID, before)
	if err != nil {
		return issueEpisode{}, false, err
	}
	defer rows.Close()

	episode := issueEpisode{}
	for rows.Next() {
		var week time.Time
		var issue string
		if err := rows.Scan(&week, &issue); err != nil {
			return issueEpisode{}, false, err
		}
		if strings.TrimSpace(issue) == "" {
			break
		}
		if episode.Weeks == 0 {
			episode.Text = strings.TrimSpace(issue)
		}
		episode.Weeks++
		episode.StartedWeek = week.Format("2006-01-02")
	}
	if err := rows.Err(); err != nil {
		return issueEpisode{}, false, err
	}
	return episode, episode.Weeks > 0, nil
}

// saveIssueOutcome writes one ending, ignoring a repeat of one already stored.
func saveIssueOutcome(ctx context.Context, tx pgx.Tx, workItemID int64, endedWeek string, episode issueEpisode, outcome string, recordedBy int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO work_item_issue_outcomes
			(work_item_id, ended_week, started_week, weeks, outcome, issue_text, recorded_by)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(work_item_id, ended_week) DO UPDATE
		SET outcome = EXCLUDED.outcome, issue_text = EXCLUDED.issue_text,
			started_week = EXCLUDED.started_week, weeks = EXCLUDED.weeks`,
		workItemID, endedWeek, episode.StartedWeek, episode.Weeks, outcome, episode.Text, recordedBy)
	return err
}

// issueClearance is what the recorded endings say about how long obstacles take
// this organisation to clear.
type issueClearance struct {
	// Resolved is how many endings were reported in the period; MedianWeeks and
	// LongestWeeks describe them. Zero means nobody has answered the question
	// yet, which the screen says rather than showing a zero as if it were a
	// measurement.
	Resolved     int    `json:"resolved"`
	MedianWeeks  int    `json:"medianWeeks"`
	LongestWeeks int    `json:"longestWeeks"`
	LongestTitle string `json:"longestTitle,omitempty"`
}

// issueClearanceFor reads the endings recorded inside a period, for the people
// this report covers.
//
// Median rather than mean: one obstacle that ran a year would drag an average
// somewhere no real obstacle sits, and the figure exists to answer "how long
// does this usually take", which is what a median answers.
func (a *App) issueClearanceFor(ctx context.Context, start, end string, ownerWhere string, ownerArgs []any) (issueClearance, error) {
	// start and end first, owner arguments after: ownerWhere is built against
	// $3 onward by the caller, exactly as decisionsInPeriod expects it.
	args := append([]any{start, end}, ownerArgs...)
	statement := `SELECT o.weeks, coalesce(w.title,'')
		FROM work_item_issue_outcomes o
		JOIN work_items w ON w.id = o.work_item_id
		JOIN users u ON u.id = w.user_id
		WHERE o.outcome = '` + issueOutcomeResolved + `'
		  AND o.ended_week BETWEEN $1::date AND $2::date` + ownerWhere + `
		ORDER BY o.weeks DESC, w.title`
	rows, err := a.db.Query(ctx, statement, args...)
	if err != nil {
		return issueClearance{}, err
	}
	defer rows.Close()
	weeks := []int{}
	result := issueClearance{}
	for rows.Next() {
		var span int
		var title string
		if err := rows.Scan(&span, &title); err != nil {
			return issueClearance{}, err
		}
		if len(weeks) == 0 {
			result.LongestWeeks, result.LongestTitle = span, title
		}
		weeks = append(weeks, span)
	}
	if err := rows.Err(); err != nil {
		return issueClearance{}, err
	}
	result.Resolved = len(weeks)
	if result.Resolved > 0 {
		// Sorted descending by the query, so the middle is the median either way.
		result.MedianWeeks = weeks[len(weeks)/2]
	}
	return result, nil
}
