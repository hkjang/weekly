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

// Every authorisation refusal this package makes, written down.
//
// v0.259.0 shipped without one. authz-check.py deletes a refusal, runs the
// suite, and puts it back — for forty minutes — and `git add -A` beside it
// swept a half-removed check into a release: GET /api/v1/handover answered for
// somebody else's organisation. The suite noticed while the tool was running
// and the failure was read as flaky, because a test that fails beside a tool
// that is editing the tree looks exactly like a test that fails on its own.
//
// authz-check.py asks a different and slower question — is this refusal
// guarding anything? — and cannot run on every commit. This one runs in
// milliseconds and asks only: is it still here. Removing a refusal is then a
// deliberate act with a line to delete, rather than something a stray `git add`
// can do on your behalf.
//
// The ledger records handler and message rather than line numbers, which move
// for reasons nobody needs to be told about.
//
// guards: every writeError(w, 403, ...) in this package

var refusalPattern = regexp.MustCompile(`writeError\(\s*w,\s*(?:http\.StatusForbidden|403)\s*,\s*"([^"]+)"\s*,\s*(.+)$`)
var refusalRough = regexp.MustCompile(`writeError\(\s*w,\s*(?:http\.StatusForbidden|403)`)
var refusalHandler = regexp.MustCompile(`^func \(a \*App\) (\w+)\(`)

// refusalSites reads the package as text, because that is the thing a stray
// edit changes. Anything the rough pattern sees and the precise one cannot read
// is reported rather than skipped: a site the ledger cannot describe is a site
// nobody is watching, which is the failure this exists to prevent.
func refusalSites(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	sites := []string{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		handler := "(패키지)"
		for number, line := range strings.Split(string(body), "\n") {
			if found := refusalHandler.FindStringSubmatch(line); found != nil {
				handler = found[1]
			}
			if !refusalRough.MatchString(line) {
				continue
			}
			found := refusalPattern.FindStringSubmatch(line)
			if found == nil {
				t.Errorf("%s:%d 의 거부를 원장이 읽지 못합니다: %s\n"+
					"  읽을 수 없는 자리는 지켜지지 않는 자리입니다. 모양을 맞추거나 이 시험을 넓히세요.",
					name, number+1, strings.TrimSpace(line))
				continue
			}
			message := strings.TrimSpace(found[2])
			if quoted := regexp.MustCompile(`^"([^"]*)"`).FindStringSubmatch(message); quoted != nil {
				message = quoted[1]
			} else {
				// A message built at the call site. What it says is that
				// handler's business; that it refuses at all is this ledger's.
				message = "<변수>"
			}
			sites = append(sites, fmt.Sprintf("%s  %s  %s  %s", name, handler, found[1], message))
		}
	}
	return sites
}

func TestNoAuthorisationRefusalLeavesWithoutBeingNoticed(t *testing.T) {
	sites := refusalSites(t)
	recorded, err := os.ReadFile("refusals.sums")
	if err != nil {
		t.Fatalf("원장을 읽을 수 없습니다: %v\n지금 자리들은 다음과 같습니다:\n%s",
			err, strings.Join(sites, "\n"))
	}
	want := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
		if strings.TrimSpace(line) != "" {
			want[line]++
		}
	}
	got := map[string]int{}
	for _, line := range sites {
		got[line]++
	}
	for line, count := range want {
		if got[line] < count {
			t.Errorf("거부가 사라졌습니다:\n  %s\n"+
				"  일부러 없앤 것이라면 refusals.sums 에서 그 줄을 지우세요. "+
				"그러지 않았다면 무엇이 이 줄을 지웠는지 먼저 확인하십시오.", line)
		}
	}
	for line, count := range got {
		if want[line] < count {
			t.Errorf("원장에 없는 거부가 있습니다. 새로 더한 것이라면 이 줄을 refusals.sums 에 넣으세요:\n  %s", line)
		}
	}
}
