package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A guard marker that names something this package does not have.
//
// upgrade_test.go said `// guards: migrations` for a day. The variable is
// called migrationFiles; nothing called migrations has ever existed here.
// guard-check.py reported it as "never executed", which reads like a weak test
// rather than a wrong name, and it only reports in CI, where it sat red across
// nineteen commits while releases went out.
//
// The half of that question that needs no coverage run — does the named thing
// exist at all — is answerable here, in the suite that runs before every
// commit.
//
// It reads markers the same way guard-check.py does: only one sitting in the
// comment block directly above a test function. A `guards:` line anywhere else
// is prose about the file, and refusalledger_test.go has one.
func TestEveryGuardMarkerNamesSomethingThisPackageHas(t *testing.T) {
	declared := map[string]bool{}
	function := regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?(\w+)\s*[(\[]`)
	single := regexp.MustCompile(`(?m)^(?:const|var|type)\s+(\w+)`)
	inBlock := regexp.MustCompile(`^\s+(\w+)`)

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sources {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if !strings.HasSuffix(name, "_test.go") {
			for _, found := range function.FindAllStringSubmatch(source, -1) {
				declared[found[1]] = true
			}
			for _, found := range single.FindAllStringSubmatch(source, -1) {
				declared[found[1]] = true
			}
			open := false
			for _, line := range strings.Split(source, "\n") {
				switch {
				case open && strings.HasPrefix(line, ")"):
					open = false
				case open && !strings.HasPrefix(strings.TrimSpace(line), "//"):
					if found := inBlock.FindStringSubmatch(line); found != nil {
						declared[found[1]] = true
					}
				case regexp.MustCompile(`^(?:const|var|type)\s*\($`).MatchString(line):
					open = true
				}
			}
		}
	}

	marker := regexp.MustCompile(`^\s*//\s*guards:\s*(.+?)\s*$`)
	testFunc := regexp.MustCompile(`^func (Test\w+)\(`)
	checked := 0
	for _, name := range sources {
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		pending := ""
		for _, line := range strings.Split(string(body), "\n") {
			if found := marker.FindStringSubmatch(line); found != nil {
				pending = found[1]
				continue
			}
			if testFunc.MatchString(line) {
				subjects := pending
				pending = ""
				if subjects == "" {
					continue
				}
				for _, subject := range strings.Split(subjects, ",") {
					subject = strings.TrimSpace(strings.SplitN(subject, "=", 2)[0])
					if subject == "" {
						continue
					}
					checked++
					if !declared[subject] {
						t.Errorf("%s 의 guards 표시가 %q 를 가리키는데 이 꾸러미에 그런 것이 없습니다",
							name, subject)
					}
				}
				continue
			}
			// The marker has to sit in the comment block immediately above the
			// test, or it is describing something else.
			if pending != "" && !strings.HasPrefix(strings.TrimSpace(line), "//") {
				pending = ""
			}
		}
	}
	if checked < 50 {
		t.Errorf("표시를 %d개밖에 못 읽었습니다 — 읽는 쪽이 깨졌을 수 있습니다", checked)
	}
}
