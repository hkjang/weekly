package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// docs/CHECKS.md exists because nine checks with nine different purposes and
// nine different costs could only be told apart by reading nine scripts. A page
// like that decays the moment somebody adds a tenth and forgets it, and a
// maintainer who trusts the page then does not know the check exists.
//
// So the page is checked against the directory rather than against memory. Not
// a `guards:` marker: this pairs a document with a file listing, the same shape
// as the contract and README number tests, and it executes no subject.
func TestEveryCheckScriptIsInTheChecksPage(t *testing.T) {
	page, err := os.ReadFile("../../docs/CHECKS.md")
	if err != nil {
		t.Skipf("the checks page is not readable from here: %v", err)
	}
	text := string(page)

	entries, err := os.ReadDir("../../scripts")
	if err != nil {
		t.Skipf("the scripts directory is not readable from here: %v", err)
	}
	found := []string{}
	for _, entry := range entries {
		name := entry.Name()
		// The naming is the convention: a check is called *-check.py or .sh.
		// build, render, export, seed and the backup runner are not checks.
		if !strings.Contains(name, "-check.") {
			continue
		}
		if filepath.Ext(name) != ".py" && filepath.Ext(name) != ".sh" {
			continue
		}
		found = append(found, name)
	}
	sort.Strings(found)
	if len(found) < 5 {
		t.Fatalf("only %d check scripts were found, so this test is looking in the wrong place: %v", len(found), found)
	}

	for _, name := range found {
		if !strings.Contains(text, name) {
			t.Errorf("docs/CHECKS.md does not mention %s — a check nobody knows about is one nobody runs", name)
		}
	}

	// And the reverse: a page naming a script that is gone sends a maintainer
	// looking for something that was deleted.
	for _, line := range strings.Split(text, "\n") {
		for _, word := range strings.FieldsFunc(line, func(r rune) bool {
			return r == '`' || r == ' ' || r == '|' || r == '(' || r == ')'
		}) {
			if !strings.Contains(word, "-check.") {
				continue
			}
			name := strings.TrimPrefix(word, "scripts/")
			if _, err := os.Stat(filepath.Join("../../scripts", name)); err != nil {
				t.Errorf("docs/CHECKS.md names %s and scripts/ has no such file", name)
			}
		}
	}
}
