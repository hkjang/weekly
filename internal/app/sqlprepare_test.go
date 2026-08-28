package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every statement this product ships has to be one PostgreSQL will accept.
//
// sqlparams_test.go's own comment says the statement set "is also exercised
// against a real server with PREPARE" — it is not, and never was. Those
// PREPAREs were run by hand while the deduced types were being written down.
// So a statement with a typo, a column that a migration renamed, or two
// placeholders PostgreSQL cannot reconcile ships and is found by whoever first
// takes that branch. The two retention sweeps fixed in v0.243.0 were a milder
// form of the same gap: they parsed, and still never ran.
//
// A statement built with fmt.Sprintf or string concatenation is not this
// check's to judge — it is not a statement until runtime — so those are counted
// and named rather than silently passed over.
var (
	sqlLiteralPattern = regexp.MustCompile("(?s)`\\s*(?:SELECT|INSERT|UPDATE|DELETE|WITH)[^`]*`")
	sqlPlaceholder    = regexp.MustCompile(`\$\d+`)
)

func TestEverySQLStatementPreparesAgainstPostgres(t *testing.T) {
	server := newTestServer(t)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Skipf("이 디렉터리를 읽을 수 없습니다: %v", err)
	}

	prepared, skipped := 0, map[string]int{}
	failures := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(".", name))
		if readErr != nil {
			continue
		}
		text := string(body)
		for index, span := range sqlLiteralPattern.FindAllStringIndex(text, -1) {
			literal := text[span[0]:span[1]]
			statement := strings.TrimSpace(strings.Trim(literal, "`"))
			// A literal joined to something else is a fragment, not a
			// statement: the rest arrives at runtime. Judged from the source
			// around it, because the join is outside the quotes.
			before := strings.TrimSpace(text[max(0, span[0]-2):span[0]])
			after := strings.TrimSpace(text[span[1]:min(len(text), span[1]+2)])
			switch {
			case strings.HasSuffix(before, "+") || strings.HasPrefix(after, "+"):
				skipped[name]++
				continue
			case appendedLater(text, span):
				// Held in a variable and grown with += further down: the scope
				// predicate, the ordering, sometimes the GROUP BY. What is in
				// the quotes is half a statement.
				skipped[name]++
				continue
			case strings.Contains(statement, "%"):
				// Sprintf builds the real statement; this is a fragment.
				skipped[name]++
				continue
			case strings.Contains(statement, ";"):
				// PREPARE takes one statement. The multi-statement Exec calls
				// are all fixed text with no placeholders.
				skipped[name]++
				continue
			}
			label := fmt.Sprintf("chk_%s_%d", strings.NewReplacer(".go", "", "_", "").Replace(name), index)
			if _, execErr := server.app.db.Exec(server.ctx(), "PREPARE "+label+" AS "+statement); execErr != nil {
				failures = append(failures, fmt.Sprintf("%s: %v\n     %s", name, execErr,
					strings.Join(strings.Fields(statement), " ")))
				continue
			}
			prepared++
			_, _ = server.app.db.Exec(server.ctx(), "DEALLOCATE "+label)
		}
	}

	if prepared < 80 {
		t.Fatalf("PREPARE 한 문장이 %d개뿐입니다. 훑기가 깨졌습니다", prepared)
	}
	sort.Strings(failures)
	for _, failure := range failures {
		t.Errorf("PostgreSQL 이 받지 않는 문장입니다.\n  %s", failure)
	}
	// Said out loud: a check that quietly passes over a third of the statements
	// is not the check its name claims to be.
	names := make([]string, 0, len(skipped))
	for name := range skipped {
		names = append(names, fmt.Sprintf("%s %d", name, skipped[name]))
	}
	sort.Strings(names)
	t.Logf("PREPARE 통과 %d개 · 런타임에 조립되어 건너뛴 조각: %s", prepared, strings.Join(names, ", "))
}

// appendedLater reports whether the literal at span is assigned to a variable
// that the same function grows with +=.
func appendedLater(text string, span []int) bool {
	head := text[max(0, span[0]-80):span[0]]
	assign := regexp.MustCompile(`(\w+)\s*(?::=|=)\s*$`).FindStringSubmatch(strings.TrimRight(head, " \t\n"))
	if assign == nil {
		return false
	}
	tail := text[span[1]:min(len(text), span[1]+2500)]
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(assign[1]) + `\s*\+=`).MatchString(tail)
}
