package app

import (
	"context"
	"os"
	"testing"
	"time"
)

// The best match a search can have is a work item whose title is the query.
// A newest-first scan that stops at searchScanLimit rows spends its whole
// budget on recent paragraphs, so on a deployment with a year of reports that
// title becomes unreachable — and the screen still presents what survived as
// the top results.
func TestATitleMatchOutranksAFloodOfRecentBodies(t *testing.T) {
	dsn := os.Getenv("WEEKLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEEKLY_TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()
	db, err := openDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const phrase = "격오지 회선 이설"
	var userID, oldReport, newReport int64
	if err := db.QueryRow(ctx, `INSERT INTO users(username,display_name,role,active)
		VALUES('search_ranking_user','검색 순위 시험','USER',true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM report_items WHERE report_id IN
			(SELECT id FROM weekly_reports WHERE user_id=$1)`, userID)
		_, _ = db.Exec(context.Background(), `DELETE FROM weekly_reports WHERE user_id=$1`, userID)
		_, _ = db.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	}()

	monday := currentWeekStart(time.Now(), "MONDAY")
	old := monday.AddDate(0, 0, -7*40)
	if err := db.QueryRow(ctx, `INSERT INTO weekly_reports(user_id,week_start,status,source_type,summary,submitted_at)
		VALUES($1,$2,'SUBMITTED','MANUAL','', now()) RETURNING id`, userID, old).Scan(&oldReport); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO weekly_reports(user_id,week_start,status,source_type,summary,submitted_at)
		VALUES($1,$2,'SUBMITTED','MANUAL','', now()) RETURNING id`, userID, monday).Scan(&newReport); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO report_items(report_id,sort_order,category,title,current_result,next_plan,issue,progress)
		VALUES($1,1,'인프라',$2,'완료','유지','',100)`, oldReport, phrase); err != nil {
		t.Fatal(err)
	}
	// More recent body matches than the broad scan will ever look at.
	if _, err := db.Exec(ctx, `INSERT INTO report_items(report_id,sort_order,category,title,current_result,next_plan,issue,progress)
		SELECT $1, g, '운영', '정기 점검 ' || g, $2 || ' 관련 협조', '', '', 50
		FROM generate_series(1,$3) AS g`, newReport, phrase, searchScanLimit+200); err != nil {
		t.Fatal(err)
	}

	// The date-ordered scan alone: what the search used to see.
	var titleRowsInDateWindow int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT i.title FROM weekly_reports r JOIN report_items i ON i.report_id=r.id
		WHERE r.user_id=$1 ORDER BY r.week_start DESC, r.id DESC, i.sort_order NULLS FIRST
		LIMIT $2) recent WHERE recent.title=$3`, userID, searchScanLimit, phrase).Scan(&titleRowsInDateWindow); err != nil {
		t.Fatal(err)
	}
	if titleRowsInDateWindow != 0 {
		t.Fatalf("the flood did not push the title out of the date window; this test proves nothing (%d)", titleRowsInDateWindow)
	}

	// Now the product's own scan, not a query written here — deleting the pass
	// that saves the title has to fail this.
	app := &App{db: db}
	base := `SELECT r.id,r.user_id,u.display_name,r.week_start,r.status,r.source_type,
		coalesce(r.summary,''),coalesce(i.title,''),coalesce(i.category,''),
		coalesce(i.current_result,''),coalesce(i.next_plan,''),coalesce(i.issue,'')
		FROM weekly_reports r JOIN users u ON u.id=r.user_id
		LEFT JOIN report_items i ON i.report_id=r.id
		WHERE r.user_id=$1
		AND (r.summary ILIKE $2 OR i.title ILIKE $2 OR i.category ILIKE $2
			OR i.current_result ILIKE $2 OR i.next_plan ILIKE $2 OR i.issue ILIKE $2)`
	terms := []string{phrase}
	_, byReport, _, _, err := app.searchScan(ctx, base, []any{userID, "%" + phrase + "%"}, []int{2}, terms)
	if err != nil {
		t.Fatal(err)
	}
	hit, found := byReport[oldReport]
	if !found {
		t.Fatal("the report whose item title is the query was not scanned at all")
	}
	if hit.Score != searchFieldWeights[0].weight {
		t.Errorf("the title match scored %d, want the title weight %d", hit.Score, searchFieldWeights[0].weight)
	}
	if recent, ok := byReport[newReport]; ok && recent.Score >= hit.Score {
		t.Errorf("a body match scored %d, at or above the title's %d", recent.Score, hit.Score)
	}

	// And the weight that makes it rank first has to be the largest one.
	best := 0
	for _, field := range searchFieldWeights {
		if field.weight > best {
			best = field.weight
		}
	}
	if searchFieldWeights[0].field != "title" || searchFieldWeights[0].weight != best {
		t.Errorf("title is not the highest weighted field: %+v", searchFieldWeights)
	}
}
