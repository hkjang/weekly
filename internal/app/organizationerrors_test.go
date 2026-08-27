package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Creating organisations is among the first things an administrator does on a
// new deployment, and the two ways it goes wrong are both the caller's: a code
// somebody already used, and a parent that is not there. Neither had a test.
//
// The difference that matters is not the status code, it is what the sentence
// sends the administrator off to do. "이미 사용 중인 조직 코드입니다" is a
// keyboard away from fixed. "조직을 만들 수 없습니다" with a 500 sends them to
// a log to look for a fault that does not exist.

// guards: createOrganization
func TestACodeSomebodyIsAlreadyUsingSaysSoRatherThanFailing(t *testing.T) {
	server := newTestServer(t)

	first := server.request(http.MethodPost, "/api/v1/admin/organizations",
		map[string]any{"name": "먼저 만든 조직", "code": "SAMECODE"}, server.admin)
	if first.Code != http.StatusOK && first.Code != http.StatusCreated {
		t.Fatalf("the first organisation was refused: %d %s", first.Code, first.Body.String())
	}

	again := server.request(http.MethodPost, "/api/v1/admin/organizations",
		map[string]any{"name": "다른 이름 같은 코드", "code": "SAMECODE"}, server.admin)
	if again.Code != http.StatusConflict {
		t.Fatalf("a duplicate code answered %d, want 409: %s", again.Code, again.Body.String())
	}
	if code := errorCode(again); code != "ORGANIZATION_EXISTS" {
		t.Errorf("code=%q — the administrator cannot tell their own mistake from a fault", code)
	}
	// And it has to name what is wrong. A conflict that does not say the code is
	// taken leaves the reader guessing which field to change.
	if body := again.Body.String(); !strings.Contains(body, "코드") {
		t.Errorf("the refusal does not say the code is the problem: %s", body)
	}
}

// guards: createOrganization
func TestAParentThatIsNotThereIsTheCallersMistakeNotAFault(t *testing.T) {
	server := newTestServer(t)
	missing := int64(9_000_000)

	created := server.request(http.MethodPost, "/api/v1/admin/organizations",
		map[string]any{"name": "부모 없는 조직", "code": "NOPARENT", "parentId": missing}, server.admin)
	if created.Code != http.StatusBadRequest {
		t.Fatalf("a missing parent answered %d, want 400: %s", created.Code, created.Body.String())
	}
	if code := errorCode(created); code != "INVALID_PARENT" {
		t.Errorf("code=%q — a foreign key the caller got wrong was reported as something else", code)
	}
	// Nothing was written. A refused create that leaves a row behind is worse
	// than one that fails loudly.
	var rows int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM organizations WHERE name='부모 없는 조직'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d organisation(s) were created despite the refusal", rows)
	}
}

// The other two ways a unique constraint meets a person, and neither had a
// test. Both are the caller's mistake and both are seconds from fixed — but
// only if the answer says which mistake it was.

// guards: createUser
func TestAnIdSomebodyIsAlreadyUsingSaysSoRatherThanFailing(t *testing.T) {
	server := newTestServer(t)
	body := func() map[string]any {
		return map[string]any{"username": "duplicate_id", "displayName": "중복 아이디",
			"role": "USER", "password": "WeeklyVerify1234"}
	}
	if first := server.request(http.MethodPost, "/api/v1/admin/users", body(), server.admin); first.Code != http.StatusOK && first.Code != http.StatusCreated {
		t.Fatalf("the first account was refused: %d %s", first.Code, first.Body.String())
	}

	again := server.request(http.MethodPost, "/api/v1/admin/users", body(), server.admin)
	if again.Code != http.StatusConflict {
		t.Fatalf("a duplicate id answered %d, want 409: %s", again.Code, again.Body.String())
	}
	if code := errorCode(again); code != "USERNAME_EXISTS" {
		t.Errorf("code=%q — the administrator cannot tell a taken id from a fault", code)
	}
	if body := again.Body.String(); !strings.Contains(body, "아이디") {
		t.Errorf("the refusal does not say which field is the problem: %s", body)
	}

	// And only one account exists, whichever request lost.
	var accounts int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM users WHERE username='duplicate_id'`).Scan(&accounts); err != nil {
		t.Fatalf("count: %v", err)
	}
	if accounts != 1 {
		t.Errorf("%d accounts hold that id, want 1", accounts)
	}
}

// guards: createWorkItemLink
func TestDeclaringTheSamePredecessorTwiceSaysItIsAlreadyThere(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("link_twice", "USER", nil)
	first, second := twoWorkItemsOf(t, server, owner)

	declare := func() *httptest.ResponseRecorder {
		return server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/links", first),
			map[string]any{"blockerId": second, "note": "이것이 끝나야 한다"}, owner)
	}
	if created := declare(); created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("the first declaration was refused: %d %s", created.Code, created.Body.String())
	}

	again := declare()
	if again.Code != http.StatusConflict {
		t.Fatalf("declaring it twice answered %d, want 409: %s", again.Code, again.Body.String())
	}
	if code := errorCode(again); code != "LINK_EXISTS" {
		t.Errorf("code=%q — a link that is already there is not a server fault", code)
	}

	// One link, not two. A second row would show the same dependency twice on
	// every screen that draws it.
	var links int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM work_item_links WHERE blocked_id=$1 AND blocker_id=$2`, first, second).Scan(&links); err != nil {
		t.Fatalf("count: %v", err)
	}
	if links != 1 {
		t.Errorf("%d links between the same pair, want 1", links)
	}
}
