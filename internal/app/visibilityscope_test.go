package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var placeholder = regexp.MustCompile(`\$(\d+)`)

// guards: where=100, scopeForPrincipal
//
// This predicate decides who can see whose work, and until now nothing checked
// it. An empty predicate for anyone but an administrator is every organisation's
// work on one screen.
func TestWorkScopeNeverOpensUpMoreThanTheRoleAllows(t *testing.T) {
	org := int64(7)
	cases := []struct {
		name      string
		scope     workScope
		wantSelf  bool // the predicate restricts to the caller's own rows
		wantOrg   bool // ...or to their organisation subtree
		wantEmpty bool // ...or not at all
	}{
		{"관리자", workScope{UserID: 1, Role: "ADMIN"}, false, false, true},
		{"일반 사용자", workScope{UserID: 2, Role: "USER"}, true, false, false},
		{"조직 없는 팀장", workScope{UserID: 3, Role: "TEAM_LEADER"}, true, false, false},
		{"조직 있는 팀장", workScope{UserID: 4, Role: "TEAM_LEADER", OrganizationID: &org}, true, true, false},
		{"조직 있는 관리자급", workScope{UserID: 5, Role: "ORG_MANAGER", OrganizationID: &org}, true, true, false},
		{"모르는 역할", workScope{UserID: 6, Role: "AUDITOR"}, true, false, false},
		// SelfOnly is the caller saying "just mine" and must beat every role,
		// including the one that otherwise sees everything.
		{"관리자의 본인 한정", workScope{UserID: 7, Role: "ADMIN", SelfOnly: true}, true, false, false},
		{"팀장의 본인 한정", workScope{UserID: 8, Role: "TEAM_LEADER", OrganizationID: &org, SelfOnly: true}, true, false, false},
	}
	// The scope is built from the signed-in principal, and dropping a field here
	// is the same leak by another route: an ORG_MANAGER whose organisation went
	// missing falls back to their own rows, and a USER whose role went missing
	// would not.
	organisation := org
	built := scopeForPrincipal(&principal{ID: 11, Role: "ORG_MANAGER", OrganizationID: &organisation}, false)
	if built.UserID != 11 || built.Role != "ORG_MANAGER" || built.OrganizationID == nil || *built.OrganizationID != org {
		t.Errorf("the scope did not carry the principal through: %+v", built)
	}
	if built.SelfOnly {
		t.Error("selfOnly=false became true")
	}
	if restricted := scopeForPrincipal(&principal{ID: 11, Role: "ADMIN"}, true); !restricted.SelfOnly {
		t.Error("selfOnly=true was dropped, so an administrator asking for their own work sees everyone's")
	}

	for _, item := range cases {
		predicate, args := item.scope.where(1)
		switch {
		case item.wantEmpty:
			if predicate != "" {
				t.Errorf("%s: expected no restriction, got %q", item.name, predicate)
			}
			if len(args) != 0 {
				t.Errorf("%s: no restriction should carry no arguments, got %v", item.name, args)
			}
			continue
		case predicate == "":
			t.Errorf("%s: the predicate is empty, so every organisation's work is visible", item.name)
			continue
		}
		if item.wantSelf && !strings.Contains(predicate, "w.user_id=$") {
			t.Errorf("%s: %q does not restrict to the caller", item.name, predicate)
		}
		if hasOrg := strings.Contains(predicate, "organization_id"); hasOrg != item.wantOrg {
			t.Errorf("%s: organisation subtree present=%v, want %v — %q", item.name, hasOrg, item.wantOrg, predicate)
		}
		if args[0] != item.scope.UserID {
			t.Errorf("%s: first argument is %v, not the caller's id %d", item.name, args[0], item.scope.UserID)
		}
	}
}

// where() is handed the next free parameter number, and callers use 1 and 3.
// A predicate that numbers its placeholders from somewhere else does not fail
// loudly — it binds another query's value.
func TestWorkScopePlaceholdersMatchTheOffsetAndTheArguments(t *testing.T) {
	org := int64(7)
	scopes := []workScope{
		{UserID: 2, Role: "USER"},
		{UserID: 4, Role: "TEAM_LEADER", OrganizationID: &org},
		{UserID: 5, Role: "ORG_MANAGER", OrganizationID: &org},
		{UserID: 8, Role: "ADMIN", SelfOnly: true},
	}
	for _, scope := range scopes {
		for _, start := range []int{1, 3, 12} {
			predicate, args := scope.where(start)
			found := map[int]bool{}
			for _, match := range placeholder.FindAllStringSubmatch(predicate, -1) {
				number, _ := strconv.Atoi(match[1])
				found[number] = true
			}
			if len(found) != len(args) {
				t.Errorf("%+v at $%d: %d distinct placeholders for %d arguments — %q",
					scope, start, len(found), len(args), predicate)
			}
			for offset := range args {
				if !found[start+offset] {
					t.Errorf("%+v at $%d: $%d is never used, so argument %d binds nothing — %q",
						scope, start, start+offset, offset+1, predicate)
				}
			}
		}
	}
}

// guards: safeImportPath=100
//
// The only thing standing between a stored path and os.ReadFile.
func TestImportPathStaysInsideItsOwnJob(t *testing.T) {
	root := func(jobID int64) string {
		return filepath.Join(importDirectory, strconv.FormatInt(jobID, 10))
	}
	allowed := []struct {
		name  string
		path  string
		jobID int64
	}{
		{"정상", filepath.Join(root(5), "weekly.pptx"), 5},
		{"하위 폴더", filepath.Join(root(5), "sub", "weekly.pptx"), 5},
		{"대문자 확장자", filepath.Join(root(5), "WEEKLY.PPTX"), 5},
	}
	for _, item := range allowed {
		if !safeImportPath(item.path, item.jobID) {
			t.Errorf("%s: %q was rejected", item.name, item.path)
		}
	}

	refused := []struct {
		name  string
		path  string
		jobID int64
	}{
		{"상위로 탈출", filepath.Join(root(5), "..", "..", "..", "etc", "passwd.pptx"), 5},
		{"다른 작업의 파일", filepath.Join(root(6), "weekly.pptx"), 5},
		// The one a trailing separator exists for: job 5's root is a string
		// prefix of job 51's, so without it 51's files would read as 5's.
		{"번호가 겹치는 작업", filepath.Join(root(51), "weekly.pptx"), 5},
		{"저장소 밖", filepath.Join("/etc", "weekly.pptx"), 5},
		{"확장자가 다름", filepath.Join(root(5), "weekly.exe"), 5},
		{"확장자를 덧붙임", filepath.Join(root(5), "weekly.pptx.exe"), 5},
		{"확장자 없음", filepath.Join(root(5), "weekly"), 5},
		{"디렉터리 자체", root(5), 5},
		{"빈 경로", "", 5},
	}
	for _, item := range refused {
		if safeImportPath(item.path, item.jobID) {
			t.Errorf("%s: %q was accepted for job %d", item.name, item.path, item.jobID)
		}
	}
	_ = fmt.Sprint()
}

// The predicate names the aliases w and u, and three call sites carry a comment
// saying that renaming either would leave the query compiling and silently
// unfiltered. A risk worth writing down three times is worth checking.
//
// Go already catches the other way this breaks: dropping the predicate leaves
// `where` unused and the build fails. Only the aliases can slip.
func TestEveryScopedQueryBindsTheAliasesThePredicateNames(t *testing.T) {
	ownerAlias := regexp.MustCompile(`(?:FROM|JOIN)\s+\w+\s+w\b`)
	userAlias := regexp.MustCompile(`(?:FROM|JOIN)\s+users\s+u\b`)

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		body, readErr := readSource(source)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, function := range splitGoFunctions(body) {
			if !strings.Contains(function.body, "scope.where(") && !strings.Contains(function.body, ".where(") {
				continue
			}
			if !strings.Contains(function.body, "where(") || strings.Contains(function.body, "func (s workScope) where") {
				continue
			}
			checked++
			if !ownerAlias.MatchString(function.body) {
				t.Errorf("%s: %s calls workScope.where but binds nothing to the alias w, so the owner filter matches another table",
					source, function.name)
			}
			if !userAlias.MatchString(function.body) {
				t.Errorf("%s: %s calls workScope.where but never joins users u, so the organisation filter has no organisation",
					source, function.name)
			}
		}
	}
	// A guard over an empty set is not a guard.
	if checked < 4 {
		t.Fatalf("found %d scoped queries; the call sites moved and this stopped looking at them", checked)
	}
}

type goFunction struct {
	name string
	body string
}

func readSource(path string) (string, error) {
	raw, err := os.ReadFile(path)
	return string(raw), err
}

// splitGoFunctions cuts a file at top level `func` declarations. It is enough
// for asking "does this function's text contain X"; it is not a parser.
func splitGoFunctions(body string) []goFunction {
	declaration := regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)`)
	positions := declaration.FindAllStringSubmatchIndex(body, -1)
	functions := make([]goFunction, 0, len(positions))
	for index, position := range positions {
		end := len(body)
		if index+1 < len(positions) {
			end = positions[index+1][0]
		}
		functions = append(functions, goFunction{
			name: body[position[2]:position[3]],
			body: body[position[0]:end],
		})
	}
	return functions
}

// guards: weekStartWarning=100
//
// The one message in the product that asks an administrator to accept a
// consequence before it happens. It has to state the size of that consequence.
func TestWeekStartWarningStatesWhatIsAtStake(t *testing.T) {
	full := weekStartWarning(37, "2025-09-01")
	for _, required := range []string{"37", "2025-09-01", "참여 분석", "기간 보고", "확인이 필요합니다"} {
		if !strings.Contains(full, required) {
			t.Errorf("the warning does not mention %q: %q", required, full)
		}
	}
	// Without a known earliest week the sentence must still be a sentence, and
	// must not offer an empty parenthesis where a date belongs.
	partial := weekStartWarning(4, "")
	if strings.Contains(partial, "()") || strings.Contains(partial, "가장 이른 주차 )") {
		t.Errorf("an unknown earliest week left an empty gap: %q", partial)
	}
	if !strings.Contains(partial, "4") {
		t.Errorf("the count is missing: %q", partial)
	}
}
