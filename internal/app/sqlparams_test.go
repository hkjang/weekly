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
		// PREPARE deduces {text, boolean, character varying, boolean, bigint}:
		// the reused parameter only ever appears as a CASE WHEN condition.
		"reports.go": "the reused review-reset flag is a boolean in every branch",
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
