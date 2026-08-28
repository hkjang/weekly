package app

import "fmt"

// orgSubtreeDepth bounds the walk down an organisation tree.
//
// organizations.parent_id can hold a cycle and nothing stops it. The API only
// ever inserts an organisation whose parent already exists — so it cannot make
// one — but it also offers no way to rename, re-parent or remove an
// organisation at all, which leaves an operator who has to restructure a chart
// editing the table by hand. That is exactly where a loop gets made.
//
// An unbounded UNION ALL over a loop never finishes. Measured on a deployment
// with 전사 → 본부 → 팀 → 전사: every team-scoped read for everybody inside the
// cycle hung until the browser gave up, and PostgreSQL grew an intermediate
// result nobody would ever read. No error reached anyone; the screen simply
// never loaded, for some people and not others.
//
// Sixteen because the walk *up* the same tree, in pptx.go, has carried
// `WHERE ancestry.depth < 16` all along, and the dependency walk in
// workitemlinks.go carries a depth too. Two of the three recursive walks over
// this data already knew; the fifteen copies of the third did not.
const orgSubtreeDepth = 16

// orgSubtree is an organisation and everything under it, as a subquery whose
// starting id is the given placeholder.
//
// Written once because it was written fifteen times. Fifteen copies of a query
// is fifteen chances for one of them to keep a bug the others have lost.
func orgSubtree(placeholder int) string {
	return fmt.Sprintf(`(WITH RECURSIVE orgs(id, depth) AS (
			SELECT id, 0 FROM organizations WHERE id=$%d
			UNION ALL
			SELECT suborg.id, orgs.depth+1 FROM organizations suborg JOIN orgs ON suborg.parent_id=orgs.id
			WHERE orgs.depth < %d)
		SELECT id FROM orgs)`, placeholder, orgSubtreeDepth)
}
