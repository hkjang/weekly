package app

import (
	"net/http"
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
