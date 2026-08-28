package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Upgrading is the highest-stakes thing an operator does with this product, and
// nothing checked it.
//
// Measured by hand first, because a test that has never seen the real thing is
// a guess: v0.166.0 was started against an empty database, a user, a report,
// an item and a work item were written through its API and a secret was saved,
// then v0.259.0 was started on the same database and the same state volume —
// ninety-three releases and four migrations later. It came up, the rows were
// all there, the secret still decrypted and the new tables existed. That is the
// behaviour this locks in without needing an old binary.
//
// The invariant is stronger than "the rows survive". A deployment that upgrades
// must end up with **the same schema as one installed fresh today**: same
// columns, same types, same nullability. Otherwise two deployments running the
// same version disagree about their own tables, and every later migration is
// written against whichever one its author happened to have.

func emptyScratchDatabase(t *testing.T, dsn string) (string, func()) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse the test DSN: %v", err)
	}
	name := fmt.Sprintf("weekly_u_%d_%d_%d", harnessStamp, harnessRun, harnessAccounts.Add(1))
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to create %s: %v", name, err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create %s: %v", name, err)
	}
	_ = admin.Close(ctx)
	target := *parsed
	target.Path = "/" + name
	drop := func() {
		dropper, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer dropper.Close(context.Background())
		_, _ = dropper.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	}
	return target.String(), drop
}

// migrationVersions lists the versions in the embedded set, in order.
func migrationVersions(t *testing.T) []int {
	t.Helper()
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	versions := make([]int, 0, len(names))
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			t.Fatalf("migration name %s does not start with a version", name)
		}
		versions = append(versions, version)
	}
	return versions
}

// applyMigrationsUpTo brings a database to the state an older release left it
// in: the first `count` migrations and no more.
func applyMigrationsUpTo(t *testing.T, pool *pgxpool.Pool, count int) {
	t.Helper()
	ctx := context.Background()
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for index, name := range names {
		if index >= count {
			return
		}
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		version, _ := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO schema_migrations(version) VALUES($1) ON CONFLICT DO NOTHING`, version); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
}

// describeSchema reads back the shape of every table, as the database itself
// reports it. Column order is not compared: PostgreSQL assigns ordinals by the
// order a column was added, so an upgraded database legitimately differs there
// from a fresh one, and comparing it would fail on every correct upgrade.
func describeSchema(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT table_name || '.' || column_name || ' ' || data_type
		       || ' null=' || is_nullable
		       || ' default=' || coalesce(column_default, '-')
		FROM information_schema.columns
		WHERE table_schema = 'public'
		ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	shape := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		shape = append(shape, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return shape
}

// guards: migrate
func TestAnUpgradedDeploymentEndsUpWithTheSchemaAFreshInstallHas(t *testing.T) {
	dsn := os.Getenv("WEEKLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEEKLY_TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()

	freshDSN, dropFresh := emptyScratchDatabase(t, dsn)
	defer dropFresh()
	fresh, err := pgxpool.New(ctx, freshDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := migrate(ctx, fresh); err != nil {
		t.Fatalf("a fresh install does not migrate: %v", err)
	}
	want := describeSchema(t, fresh)
	if len(want) == 0 {
		t.Fatal("a fresh install produced no columns")
	}

	versions := migrationVersions(t)
	for count := 1; count < len(versions); count++ {
		t.Run(fmt.Sprintf("from_%03d", versions[count-1]), func(t *testing.T) {
			oldDSN, drop := emptyScratchDatabase(t, dsn)
			defer drop()
			pool, err := pgxpool.New(ctx, oldDSN)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			applyMigrationsUpTo(t, pool, count)

			// Rows an older deployment would have. Only the tables and columns
			// that exist in the very first migration, so one fixture serves
			// every stopping point.
			var orgID, userID, reportID int64
			if err := pool.QueryRow(ctx,
				`INSERT INTO organizations(name, code) VALUES('업그레이드본부','UPG') RETURNING id`).Scan(&orgID); err != nil {
				t.Fatalf("seed organisation: %v", err)
			}
			if err := pool.QueryRow(ctx,
				`INSERT INTO users(username, display_name, password_hash, role, organization_id)
				 VALUES('upgradewriter','업그레이드 작성자','x','USER',$1) RETURNING id`, orgID).Scan(&userID); err != nil {
				t.Fatalf("seed user: %v", err)
			}
			if err := pool.QueryRow(ctx,
				`INSERT INTO weekly_reports(user_id, week_start, summary, status)
				 VALUES($1,'2026-08-24','업그레이드 전에 쓴 요약입니다','CLOSED') RETURNING id`, userID).Scan(&reportID); err != nil {
				t.Fatalf("seed report: %v", err)
			}
			if _, err := pool.Exec(ctx,
				`INSERT INTO report_items(report_id, title, category, current_result, next_plan, issue, progress, sort_order)
				 VALUES($1,'업그레이드 시험 업무','운영','이번 주에 한 일','다음 주 계획','막힌 것',42,0)`, reportID); err != nil {
				t.Fatalf("seed item: %v", err)
			}

			if err := migrate(ctx, pool); err != nil {
				t.Fatalf("upgrading from %d does not migrate: %v", versions[count-1], err)
			}

			var head int
			if err := pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&head); err != nil {
				t.Fatal(err)
			}
			if head != versions[len(versions)-1] {
				t.Errorf("스키마가 %d 에서 멈췄습니다, %d 를 기대합니다", head, versions[len(versions)-1])
			}

			// The rows an operator would notice losing.
			var summary, status, title, issue string
			var progress int
			if err := pool.QueryRow(ctx,
				`SELECT r.summary, r.status, i.title, i.issue, i.progress
				 FROM weekly_reports r JOIN report_items i ON i.report_id = r.id WHERE r.id=$1`, reportID).
				Scan(&summary, &status, &title, &issue, &progress); err != nil {
				t.Fatalf("업그레이드 뒤 보고서를 읽을 수 없습니다: %v", err)
			}
			if summary != "업그레이드 전에 쓴 요약입니다" || status != "CLOSED" ||
				title != "업그레이드 시험 업무" || issue != "막힌 것" || progress != 42 {
				t.Errorf("업그레이드가 내용을 바꿨습니다: %q %q %q %q %d", summary, status, title, issue, progress)
			}

			got := describeSchema(t, pool)
			if difference := schemaDifference(want, got); difference != "" {
				t.Errorf("버전 %d 에서 올린 스키마가 새로 설치한 것과 다릅니다:\n%s", versions[count-1], difference)
			}
		})
	}
}

// schemaDifference names what one side has and the other does not.
func schemaDifference(want, got []string) string {
	index := map[string]int{}
	for _, line := range want {
		index[line]++
	}
	for _, line := range got {
		index[line]--
	}
	missing, extra := []string{}, []string{}
	for line, count := range index {
		for ; count > 0; count-- {
			missing = append(missing, line)
		}
		for ; count < 0; count++ {
			extra = append(extra, line)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var out strings.Builder
	for _, line := range missing {
		fmt.Fprintf(&out, "  새로 설치한 쪽에만 있음: %s\n", line)
	}
	for _, line := range extra {
		fmt.Fprintf(&out, "  올린 쪽에만 있음:       %s\n", line)
	}
	return out.String()
}

// A migration that has shipped is history, and history does not change.
//
// The upgrade test above replays today's files to build "an old database", so
// it is blind to exactly one mistake: editing a migration that has already run
// somewhere. Both sides of its comparison get the edit, and both agree. Tried
// as a mutation — a column added to 001_initial.sql with no new migration —
// and it passed, which is how this ledger came to exist.
//
// The damage is real and quiet. Deployments that already applied 001 never see
// the new column; deployments installed after the edit have it. Two servers on
// the same version then disagree about their own tables, and the next migration
// is written against whichever one its author happened to be looking at.
//
// Adding a migration means adding its line here, deliberately. Changing one
// means this test failing, which is the point.
//
// guards: migrations
func TestAMigrationThatHasShippedIsNeverEdited(t *testing.T) {
	recorded := map[string]string{}
	ledger, err := os.ReadFile("migrations.sums")
	if err != nil {
		t.Fatalf("원장을 읽을 수 없습니다: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(ledger)), "\n") {
		sum, name, found := strings.Cut(strings.TrimSpace(line), "  ")
		if !found {
			t.Fatalf("원장의 줄 모양이 다릅니다: %q", line)
		}
		recorded[name] = sum
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		seen[entry.Name()] = true
		sum := fmt.Sprintf("%x", sha256.Sum256(body))
		switch recorded[entry.Name()] {
		case sum:
		case "":
			t.Errorf("%s 가 원장에 없습니다. 새 마이그레이션이라면 이 줄을 migrations.sums 에 넣으세요:\n%s  %s",
				entry.Name(), sum, entry.Name())
		default:
			t.Errorf("%s 가 나간 뒤에 바뀌었습니다.\n  원장 %s\n  지금 %s\n"+
				"  이미 적용한 배포는 이 변경을 보지 못합니다. 새 마이그레이션으로 나누세요.",
				entry.Name(), recorded[entry.Name()], sum)
		}
	}
	for name := range recorded {
		if !seen[name] {
			t.Errorf("%s 가 원장에는 있는데 파일이 없습니다. 나간 마이그레이션은 지울 수 없습니다.", name)
		}
	}
}
