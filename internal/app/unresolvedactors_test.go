package app

import (
	"fmt"
	"strings"
	"testing"
)

func pageBy(id, creator, modifier string) ConfluencePage {
	return ConfluencePage{ID: id, CreatorUsername: creator, LastModifierUsername: modifier}
}

// The sync counted what it produced and what failed. A Confluence account that
// belongs to nobody produces nothing and fails at nothing, so twelve pages of
// work disappeared under a green SUCCESS.
// guards: unresolvedConfluenceActors, unresolvedActorNotice
func TestUnresolvedActorsAreCountedAndNamed(t *testing.T) {
	pages := []ConfluencePage{
		pageBy("1", "j.hkjang", "j.hkjang"),
		pageBy("2", "k.lee", "k.lee"),
		pageBy("3", "confadmin", "confadmin"),
		pageBy("4", "confadmin", "p.park"), // one known author, one not: still lost
	}
	mappings := map[string]int64{"confadmin": 1}

	names, unattributed := unresolvedConfluenceActors(pages, mappings)
	if got := strings.Join(names, ","); got != "j.hkjang,k.lee,p.park" {
		t.Errorf("unresolved actors are %q; a mapped account or a missing one is misclassified", got)
	}
	if unattributed != 3 {
		t.Errorf("3 pages had an actor nobody could resolve, counted %d", unattributed)
	}

	notice := unresolvedActorNotice(names, unattributed)
	for _, name := range names {
		if !strings.Contains(notice, name) {
			t.Errorf("the notice does not name %q, so nobody can map it: %q", name, notice)
		}
	}
	if !strings.Contains(notice, "3개") {
		t.Errorf("the notice does not say how much work was lost: %q", notice)
	}
}

// A fully mapped deployment must stay silent, or the notice becomes furniture.
// guards: unresolvedConfluenceActors
func TestFullyMappedSyncSaysNothing(t *testing.T) {
	pages := []ConfluencePage{pageBy("1", "a", "b"), pageBy("2", "b", "a")}
	names, unattributed := unresolvedConfluenceActors(pages, map[string]int64{"a": 1, "b": 2})
	if len(names) != 0 || unattributed != 0 {
		t.Errorf("every actor was mapped, yet %v / %d pages were reported lost", names, unattributed)
	}
}

// Pages with no recorded author at all are a different problem and must not be
// blamed on a missing mapping.
// guards: unresolvedConfluenceActors
func TestAnonymousPagesAreNotCalledUnmapped(t *testing.T) {
	names, unattributed := unresolvedConfluenceActors([]ConfluencePage{pageBy("1", "", "")}, map[string]int64{})
	if len(names) != 0 || unattributed != 0 {
		t.Errorf("a page with no author was reported as an unmapped account: %v / %d", names, unattributed)
	}
}

// The list is shortened for readability; the counts are what the reader acts on
// and must survive it.
// guards: unresolvedConfluenceActors, unresolvedActorNotice
func TestManyUnresolvedActorsKeepTheirCounts(t *testing.T) {
	pages := make([]ConfluencePage, 0, 30)
	for index := 0; index < 30; index++ {
		pages = append(pages, pageBy(fmt.Sprintf("%d", index), fmt.Sprintf("user%02d", index), ""))
	}
	names, unattributed := unresolvedConfluenceActors(pages, map[string]int64{})
	notice := unresolvedActorNotice(names, unattributed)
	if !strings.Contains(notice, "30명") || !strings.Contains(notice, "외 10명") {
		t.Errorf("30 accounts went unresolved but the notice does not add up: %q", notice)
	}
	if !strings.Contains(notice, "30개") {
		t.Errorf("the notice does not say how many pages were lost: %q", notice)
	}
}

// recordConfluenceError rewrites its input through safeConfluenceError, which
// would replace this text with the generic connection message. This asserts the
// notice is not the kind of string that survives that path, so the separate
// recorder stays necessary rather than looking redundant.
func TestNoticeWouldBeDestroyedByTheErrorPath(t *testing.T) {
	notice := unresolvedActorNotice([]string{"j.hkjang"}, 4)
	if rewritten := safeConfluenceError(fmt.Errorf("%s", notice)); rewritten == notice {
		t.Skip("safeConfluenceError now passes text through; recordConfluenceNotice may be redundant")
	} else if !strings.Contains(rewritten, "연결하지 못했습니다") {
		t.Errorf("unexpected rewrite of the notice: %q", rewritten)
	}
}
