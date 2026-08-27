package app

import (
	"fmt"
	"net/http"
	"testing"
)

// Walked on a deployment: merging 교육 개선 into 감사 개선 moved the weekly text
// and nothing else. The decision recorded against the source stayed on an item
// that no longer appears in any list — the target's decision list came back
// empty — and the dependency between the two still pointed at the source, so
// the target's own screen said it was blocked by itself, by a task showing
// 진척 0 and no last week because its history had moved away. Nothing could
// ever clear that: the blocker is not in any list to open.

// guards: mergeWorkItem
func TestMergingWorkCarriesEverythingThatPointedAtIt(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("merge_carries", "USER", nil)
	other := server.createUser("merge_neighbour", "USER", nil)

	// One report with two items: weekWithIssue writes a whole week, so two
	// calls for one author collide on the same period.
	reportID, version := server.draft(author, "2026-08-17", "합칠 두 업무")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID), map[string]any{
		"summary": "합칠 두 업무", "version": version,
		"items": []map[string]any{
			{"category": "개발", "title": "합쳐질 업무", "currentResult": "진행했습니다",
				"nextPlan": "이어서 합니다", "issue": "막힌 데가 있습니다", "progress": 30},
			{"category": "개발", "title": "남을 업무", "currentResult": "진행했습니다",
				"nextPlan": "이어서 합니다", "issue": "", "progress": 40},
		},
	}, author)
	if filled.Code != http.StatusOK {
		t.Fatalf("보고서 저장: %d %s", filled.Code, filled.Body.String())
	}
	server.weekWithIssue(other, "2026-08-17", "남의 선행 업무", "", 20)

	source := server.workItemNamed(server.lastCreatedUsername("merge_carries"), "합쳐질 업무")
	target := server.workItemNamed(server.lastCreatedUsername("merge_carries"), "남을 업무")
	outside := server.workItemNamed(server.lastCreatedUsername("merge_neighbour"), "남의 선행 업무")

	// A decision on the source, a dependency between the two ends, and one that
	// reaches outside. Each is a different way of pointing at the source.
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/decisions", source),
		map[string]any{"title": "합쳐질 업무의 방향 결정", "decidedBy": "팀장", "decidedOn": "2026-08-17"}, author); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("결정 기록: %d %s", w.Code, w.Body.String())
	}
	linkBetween(t, server, author, source, target)
	linkBetween(t, server, author, source, outside)

	merged := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/merge", source),
		map[string]any{"intoId": target}, author)
	if merged.Code != http.StatusOK {
		t.Fatalf("병합: %d %s", merged.Code, merged.Body.String())
	}

	var decisionsOnSource, decisionsOnTarget int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FILTER (WHERE work_item_id=$1), count(*) FILTER (WHERE work_item_id=$2) FROM decisions`,
		source, target).Scan(&decisionsOnSource, &decisionsOnTarget); err != nil {
		t.Fatal(err)
	}
	if decisionsOnSource != 0 {
		t.Errorf("결정 %d건이 합쳐진 쪽에 남았습니다. 그 업무는 어느 목록에도 없습니다", decisionsOnSource)
	}
	if decisionsOnTarget != 1 {
		t.Errorf("남은 업무가 결정을 %d건 가졌습니다, 1건이어야 합니다", decisionsOnTarget)
	}

	var selfLinks, sourceLinks, outsideLinks int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT
		count(*) FILTER (WHERE blocker_id=$2 AND blocked_id=$2),
		count(*) FILTER (WHERE blocker_id=$1 OR blocked_id=$1),
		count(*) FILTER (WHERE (blocker_id=$2 AND blocked_id=$3) OR (blocker_id=$3 AND blocked_id=$2))
		FROM work_item_links`, source, target, outside).Scan(&selfLinks, &sourceLinks, &outsideLinks); err != nil {
		t.Fatal(err)
	}
	if selfLinks != 0 {
		t.Errorf("남은 업무가 자기 자신에게 막혔습니다: %d건", selfLinks)
	}
	if sourceLinks != 0 {
		t.Errorf("링크 %d건이 합쳐진 쪽을 가리킨 채 남았습니다", sourceLinks)
	}
	if outsideLinks != 1 {
		t.Errorf("바깥으로 나가던 관계가 %d건입니다, 옮겨져 1건이어야 합니다", outsideLinks)
	}
}
