package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An organisation could be created and then never corrected — not a typo in its
// name, not a restructure. So an operator with a chart to fix edited the table
// by hand, and that is where a parent_id loop comes from: v0.236.0 measured one
// hanging every team-scoped read for everybody inside it. Bounding the walk made
// the loop survivable; this is the operation whose absence caused the editing.
//
// guards: updateOrganization
func TestAnOrganisationCanBeCorrectedWithoutTouchingTheTable(t *testing.T) {
	server := newTestServer(t)
	top := server.createOrganization("고칠 전사", "FIXTOP")
	middle := server.createChildOrganization("고칠 본부", "FIXMID", &top)

	patchOrg := func(id int64, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		return server.request(http.MethodPatch,
			fmt.Sprintf("/api/v1/admin/organizations/%d", id), body, server.admin)
	}

	renamed := patchOrg(middle, map[string]any{"name": "고친 본부"})
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", renamed.Code, renamed.Body.String())
	}
	if name := server.organizationName(t, middle); name != "고친 본부" {
		t.Errorf("the name is still %q", name)
	}

	// Not mentioning parentId leaves it alone; that is a different instruction
	// from asking for the top.
	if parent := server.organizationParent(t, middle); parent == nil || *parent != top {
		t.Errorf("a rename moved the organisation: parent=%v", parent)
	}
	if lifted := patchOrg(middle, map[string]any{"parentId": nil}); lifted.Code != http.StatusOK {
		t.Fatalf("lift to the top: %d %s", lifted.Code, lifted.Body.String())
	}
	if parent := server.organizationParent(t, middle); parent != nil {
		t.Errorf("null did not mean the top: parent=%v", *parent)
	}
	if back := patchOrg(middle, map[string]any{"parentId": top}); back.Code != http.StatusOK {
		t.Fatalf("put it back: %d %s", back.Code, back.Body.String())
	}
}

// The check the hand edit cannot make.
//
// guards: updateOrganization
func TestAnOrganisationCannotBeMovedUnderItself(t *testing.T) {
	server := newTestServer(t)
	top := server.createOrganization("고리 막기 전사", "NOCYCTOP")
	middle := server.createChildOrganization("고리 막기 본부", "NOCYCMID", &top)
	bottom := server.createChildOrganization("고리 막기 팀", "NOCYCBOT", &middle)

	for _, move := range []struct {
		label  string
		id     int64
		parent int64
	}{
		{"자기 자신 아래로", top, top},
		{"바로 아래 조직 아래로", top, middle},
		{"두 단계 아래 조직 아래로", top, bottom},
	} {
		response := server.request(http.MethodPatch,
			fmt.Sprintf("/api/v1/admin/organizations/%d", move.id),
			map[string]any{"parentId": move.parent}, server.admin)
		if response.Code != http.StatusConflict {
			t.Errorf("%s: answered %d, want 409 — this is the loop that hangs every team screen under it",
				move.label, response.Code)
			continue
		}
		if message := refusal(t, response); !strings.Contains(message, "고리") && !strings.Contains(message, "자기 자신") {
			t.Errorf("%s: the refusal does not say what would happen: %s", move.label, message)
		}
	}

	// And a move that is not a loop still works, including one that reverses
	// the order of two organisations.
	if lifted := server.request(http.MethodPatch,
		fmt.Sprintf("/api/v1/admin/organizations/%d", bottom),
		map[string]any{"parentId": nil}, server.admin); lifted.Code != http.StatusOK {
		t.Fatalf("lift the bottom out: %d %s", lifted.Code, lifted.Body.String())
	}
	if under := server.request(http.MethodPatch,
		fmt.Sprintf("/api/v1/admin/organizations/%d", top),
		map[string]any{"parentId": bottom}, server.admin); under.Code != http.StatusOK {
		t.Fatalf("the two swapped places and that is not a loop: %d %s", under.Code, under.Body.String())
	}
}

func (s *testServer) organizationName(t *testing.T, id int64) string {
	t.Helper()
	var name string
	if err := s.app.db.QueryRow(s.ctx(), `SELECT name FROM organizations WHERE id=$1`, id).Scan(&name); err != nil {
		t.Fatalf("read the organisation: %v", err)
	}
	return name
}

func (s *testServer) organizationParent(t *testing.T, id int64) *int64 {
	t.Helper()
	var parent *int64
	if err := s.app.db.QueryRow(s.ctx(), `SELECT parent_id FROM organizations WHERE id=$1`, id).Scan(&parent); err != nil {
		t.Fatalf("read the organisation: %v", err)
	}
	return parent
}
