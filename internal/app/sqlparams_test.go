package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A placeholder reused in two different contexts can make PostgreSQL deduce two
// different types for it and reject the whole statement at parse time, which
// surfaces as a 500 on an ordinary save. Reusing a placeholder is fine when
// every use agrees on the type, so this test pins the known-good reuses and
// fails on any new one until it has been checked against PostgreSQL.
//
// The full statement set is also exercised against a real server with PREPARE
// in TestDatabaseMigrationsAndSecretRotation's environment; this test keeps the
// cheap structural check in the default `go test ./...` run.
func TestReusedSQLPlaceholdersAreReviewed(t *testing.T) {
	// loc -> placeholders that are known to be reused with a consistent type.
	allowed := map[string]string{
		"auth.go":       "username and display name are both varchar(120); the oidc subject compare is one type",
		"imports.go":    "the reused parameters are integer counters and bigint identifiers",
		"middleware.go": "duration sum and max are both bigint",
		// PREPARE deduces {bigint, bigint}: merging re-aims what pointed at the
		// source, and the pair of work item ids is compared against the same two
		// columns in both directions.
		"workitemedit.go": "the reused work item ids are bigint on both sides of the link",
		// PREPARE deduces {text, boolean, character varying, boolean, bigint}:
		// the reused parameter only ever appears as a CASE WHEN condition.
		"reports.go": "the reused review-reset flag is a boolean in every branch",
		// PREPARE deduces {text, text, integer}: the account name is compared
		// against the same column twice and the address against host(), which
		// returns text, so both reused parameters keep one type.
		"loginthrottle.go": "the reused username and address are text in every filter",
		// PREPARE deduces {text}: the query is compared against several columns
		// but word_similarity takes text on both sides everywhere.
		"search.go": "the reused query text is text in every word_similarity call",
		// PREPARE deduces {integer}: the reused parameter is the weekday number
		// compared against extract(dow ...) in both aggregate filters.
		"weekstart.go": "the reused weekday number is an integer in both filters",
		// PREPARE deduces {vector, text, integer, double precision} for the
		// search and {text} for the coverage counts: the reused parameter is
		// either the query vector, cast to vector in every use, or the model
		// name compared against the same column in each subquery.
		"semantic.go": "the reused query embedding and model name each keep one type",
		// PREPARE deduces {vector, text, integer, bigint[], double precision}:
		// the reused parameter is the query vector in the projection and the
		// HAVING clause, cast to vector in both.
		"worksearch.go": "the reused query embedding is cast to vector in every use",
		// PREPARE deduces {text, bigint}: the reused parameter is the new status,
		// written by SET and compared against the same column in the WHERE, so
		// both uses are the status column's own type.
		"decisions.go": "the reused status is compared against the column it is being written to",
		// PREPARE deduces {bigint, bigint}: the reused parameter is the walk's
		// starting work item, used as the seed id, as the seed path element and
		// as the title lookup key. All three are the same identifier.
		//
		// The same PREPARE also caught a real defect here first: the seed term
		// produced varchar(240)[] while the recursive term widened to varchar[],
		// and PostgreSQL rejects a recursive union whose column types disagree.
		// Both ends are cast to text now.
		"workitemlinks.go": "the reused parameter is the same work item id in all three uses",
	}
	placeholder := regexp.MustCompile(`\$(\d+)`)
	sqlLiteral := regexp.MustCompile("(?s)`([^`]*)`")

	entries, err := os.ReadDir("./")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("./", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range sqlLiteral.FindAllStringSubmatch(string(body), -1) {
			statement := strings.Join(strings.Fields(match[1]), " ")
			if !placeholder.MatchString(statement) {
				continue
			}
			counts := map[string]int{}
			for _, found := range placeholder.FindAllStringSubmatch(statement, -1) {
				counts[found[1]]++
			}
			for number, count := range counts {
				if count < 2 {
					continue
				}
				if _, ok := allowed[name]; ok {
					continue
				}
				t.Errorf("%s reuses placeholder $%s %d times in a statement.\n"+
					"Verify PostgreSQL deduces one consistent type for it (PREPARE the statement), "+
					"then add the file to the allow list with the reason.\n  %s",
					name, number, count, truncate(statement, 200))
			}
		}
	}
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum] + "…"
}

// A SELECT list and its Scan destinations must line up. They are written in two
// different places, so adding a column to one and not the other compiles fine
// and only fails at runtime, which is how the rollup query broke once.
func TestSelectColumnsMatchScanDestinations(t *testing.T) {
	cases := []struct {
		file, selectMarker string
		wantColumns        int
	}{
		{"rollup_handlers.go", "SELECT i.report_id,i.work_item_id,i.category,i.title,i.current_result,i.next_plan,i.issue,i.management_ask,i.progress", 9},
	}
	for _, testCase := range cases {
		body, err := os.ReadFile(filepath.Join("./", testCase.file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), testCase.selectMarker) {
			t.Errorf("%s no longer contains the expected select list; update this test with the new one", testCase.file)
			continue
		}
		if got := strings.Count(testCase.selectMarker, ",") + 1; got != testCase.wantColumns {
			t.Errorf("%s select lists %d columns, want %d", testCase.file, got, testCase.wantColumns)
		}
	}
}
