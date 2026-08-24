package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The "open week" flag is computed in Go while the missing-week count is
// computed in SQL, from the same rule. If the two ever drift, the screen says
// nobody is late while the list names people, or the reverse. This pins them to
// each other against a real PostgreSQL rather than to a literal either could
// wander away from.
func TestDeadlineInstantMatchesTheSQLItGuards(t *testing.T) {
	dsn := os.Getenv("WEEKLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEEKLY_TEST_POSTGRES_DSN is not configured")
	}
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	ctx := context.Background()
	db, err := openDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	weeks := []time.Time{
		time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC),
	}
	rules := []deadlineRule{{Days: 7, Hour: 24}, {Days: 4, Hour: 18}, {Days: 1, Hour: 9}}
	for _, week := range weeks {
		for _, rule := range rules {
			var fromSQL time.Time
			if err := db.QueryRow(ctx,
				`SELECT ($1::date + make_interval(days => $2, hours => $3)) AT TIME ZONE $4`,
				week.Format("2006-01-02"), rule.Days, rule.Hour, "Asia/Seoul").Scan(&fromSQL); err != nil {
				t.Fatal(err)
			}
			if fromGo := rule.instant(week, seoul); !fromGo.Equal(fromSQL) {
				t.Errorf("week %s rule %+v: Go says %s, SQL says %s",
					week.Format("2006-01-02"), rule, fromGo, fromSQL)
			}
		}
	}
}

// The week in progress must be marked, because its submission rate is a count
// so far. Reading it beside finished weeks turns every Monday into a collapse.
func TestWeekInProgressIsMarkedOpen(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	rule := deadlineRule{Days: 7, Hour: 24}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	tuesday := time.Date(2026, 8, 25, 10, 0, 0, 0, seoul)

	if !rule.instant(monday, seoul).After(tuesday) {
		t.Error("the current week is already being judged on the Tuesday inside it")
	}
	// Two weeks back is closed by now, or nobody is ever counted late.
	if rule.instant(monday.AddDate(0, 0, -14), seoul).After(tuesday) {
		t.Error("a fortnight-old week is still open, so the list can never name anybody")
	}
}

// countOwedWeeks runs the very predicate the participation screen runs, so this
// test cannot pass against a rule the product does not use.
func countOwedWeeks(t *testing.T, db *pgxpool.Pool, userID int64, start, end time.Time) int {
	t.Helper()
	var owed int
	query := `SELECT (SELECT count(*) FROM generate_series($1::date, $2::date, interval '7 day') AS week(day)
		WHERE ` + weekIsOwed + `) FROM users u WHERE u.id=$6`
	if err := db.QueryRow(context.Background(), query,
		start, end, "Asia/Seoul", 7, 24, userID).Scan(&owed); err != nil {
		t.Fatal(err)
	}
	return owed
}

// The failure this guards against was introduced while fixing a different one:
// bounding missed weeks by created_at alone. Any deployment that imported its
// past reports stamps every account with the go-live date, so every historical
// week precedes it and the participation figure silently reads zero missing.
func TestImportedHistoryStillCountsAgainstAccountsCreatedLater(t *testing.T) {
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

	// Weeks far enough back that their deadlines have certainly passed.
	monday := currentWeekStart(time.Now(), "MONDAY")
	oldest := monday.AddDate(0, 0, -7*8)
	start, end := oldest, monday

	var migrated, fresh int64
	if err := db.QueryRow(ctx, `INSERT INTO users(username,display_name,role,active,created_at)
		VALUES('participation_migrated','이관된 사용자','USER',true, now()) RETURNING id`).Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO users(username,display_name,role,active,created_at)
		VALUES('participation_newhire','신입 사원','USER',true, now()) RETURNING id`).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM weekly_reports WHERE user_id IN ($1,$2)`, migrated, fresh)
		_, _ = db.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, migrated, fresh)
	}()

	// Imported history: filed eight weeks ago, nothing since. The account row is
	// younger than every week it is answerable for.
	if _, err := db.Exec(ctx, `INSERT INTO weekly_reports(user_id,week_start,status,source_type,summary,submitted_at)
		VALUES($1,$2,'SUBMITTED','PPTX_IMPORT','과거 보고서', now())`, migrated, oldest); err != nil {
		t.Fatal(err)
	}

	owed := countOwedWeeks(t, db, migrated, start, end)
	if owed == 0 {
		t.Fatal("a user with eight weeks of silence after an imported report owes nothing; created_at has erased the window")
	}
	if owed < 6 {
		t.Errorf("owed weeks = %d; the seven closed weeks after the imported one should nearly all count", owed)
	}

	// The new hire filed nothing and existed for none of those weeks. Holding
	// them to any of it is what put them at the head of the list before.
	if owedFresh := countOwedWeeks(t, db, fresh, start, end); owedFresh != 0 {
		t.Errorf("a brand-new account owes %d weeks it was not here for", owedFresh)
	}
}
