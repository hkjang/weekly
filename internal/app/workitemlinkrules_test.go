package app

import (
	"fmt"
	"net/http"
	"testing"
)

// TestOnlyTheTwoEndsRemoveAPredecessorLink checks that an outsider is refused,
// and stops there — both of its tasks belong to one person, so the other end
// does not exist in the fixture and the half of the rule its name promises was
// never run. Turning the handler's `&&` into `||` leaves it passing: only
// somebody owning both ends could delete, which is the opposite of the rule.

func linkBetween(t *testing.T, server *testServer, declarer *http.Cookie, blocked, blocker int64) int64 {
	t.Helper()
	created := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/links", blocked),
		map[string]any{"blockerId": blocker, "note": "이것이 끝나야 한다"}, declarer)
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("declare a predecessor: %d %s", created.Code, created.Body.String())
	}
	var id int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT id FROM work_item_links WHERE blocked_id=$1 AND blocker_id=$2`, blocked, blocker).Scan(&id); err != nil {
		t.Fatalf("read the link back: %v", err)
	}
	return id
}

func linkCount(t *testing.T, server *testServer, id int64) int {
	t.Helper()
	var count int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM work_item_links WHERE id=$1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// guards: deleteWorkItemLink
func TestBothEndsOfADependencyCanTakeItBack(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("의존 조직", "LINKENDS")
	waiting := server.createUser("link_waiting", "USER", &organisation)
	blocking := server.createUser("link_blocking", "USER", &organisation)

	// The waiting side declares it, so removing it is plainly theirs to do.
	waitingTask := workItemOf(t, server, waiting, "2026-08-24", "기다리는 업무")
	blockingTask := workItemOf(t, server, blocking, "2026-08-24", "막고 있는 업무")
	id := linkBetween(t, server, waiting, waitingTask, blockingTask)

	gone := server.request(http.MethodDelete, fmt.Sprintf("/api/v1/work-item-links/%d", id), nil, waiting)
	if gone.Code != http.StatusNoContent {
		t.Fatalf("the declaring side could not remove its own link: %d %s", gone.Code, gone.Body.String())
	}
	if linkCount(t, server, id) != 0 {
		t.Fatal("the delete answered but the link is still there")
	}

	// And the blocking side, who never agreed to it. An assertion about their
	// work that is not real should not need their agreement to take back.
	second := linkBetween(t, server, waiting, waitingTask, blockingTask)
	byBlocker := server.request(http.MethodDelete, fmt.Sprintf("/api/v1/work-item-links/%d", second), nil, blocking)
	if byBlocker.Code != http.StatusNoContent {
		t.Fatalf("the blocking side could not remove a claim on their own work: %d %s", byBlocker.Code, byBlocker.Body.String())
	}
	if linkCount(t, server, second) != 0 {
		t.Fatal("the blocking side's delete answered but the link is still there")
	}
}

// guards: deleteWorkItemLink
func TestAnIdentifierThatIsNotOneIsRefusedBeforeAnythingIsRead(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("link_badid", "USER", nil)

	for _, id := range []string{"0", "-1", "abc"} {
		w := server.request(http.MethodDelete, "/api/v1/work-item-links/"+id, nil, owner)
		if w.Code != http.StatusBadRequest || errorCode(w) != "INVALID_ID" {
			t.Errorf("식별자 %q: %d %s", id, w.Code, w.Body.String())
		}
	}
}

// guards: createWorkItemLink
func TestADependencyThatWouldCloseALoopIsRefused(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("순환 조직", "LINKLOOP")
	owner := server.createUser("link_cycle", "USER", &organisation)

	// One report carrying two differently titled tasks: the same owner cannot
	// file two reports for one week, and two identical titles fold into one task.
	first, second := twoWorkItemsOf(t, server, owner)
	linkBetween(t, server, owner, first, second)

	// second already waits on nothing; making it wait on first closes the loop.
	loop := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/links", second),
		map[string]any{"blockerId": first}, owner)
	if loop.Code != http.StatusConflict || errorCode(loop) != "DEPENDENCY_CYCLE" {
		t.Fatalf("a loop was accepted: %d %s", loop.Code, loop.Body.String())
	}

	// A predecessor has to be named at all.
	none := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/links", second),
		map[string]any{"blockerId": 0}, owner)
	if none.Code != http.StatusBadRequest || errorCode(none) != "INVALID_LINK" {
		t.Errorf("선행 업무 없이 등록됐습니다: %d %s", none.Code, none.Body.String())
	}
}
