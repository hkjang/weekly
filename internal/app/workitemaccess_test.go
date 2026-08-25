package app

import (
	"fmt"
	"net/http"
	"testing"
)

// Tidying a task — splitting one in two, folding two into one — and declaring
// what it waits on are all statements about your own work. Six rules said so and
// none of them was tested.

// twoWorkItemsOf returns two distinct work items belonging to one person.
//
// One report with two differently named items, not two reports: the product
// folds the same task across weeks into one work item, which is the behaviour
// this file relies on everywhere else. An earlier version of this helper made
// two reports with the same title, got one work item back and skipped three
// tests — and a skipped test guards nothing.
func twoWorkItemsOf(t *testing.T, server *testServer, cookie *http.Cookie) (int64, int64) {
	t.Helper()
	reportID, version := server.draft(cookie, "2026-08-24", "업무 둘이 담긴 보고")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID), map[string]any{
		"summary": "업무 둘이 담긴 보고",
		"version": version,
		"items": []map[string]any{
			{"category": "인프라", "title": "회선 이설", "currentResult": "완료", "nextPlan": "점검", "issue": "", "progress": 50},
			{"category": "보안", "title": "인증서 교체", "currentResult": "진행", "nextPlan": "배포", "issue": "", "progress": 30},
		},
	}, cookie)
	if filled.Code != http.StatusOK {
		t.Fatalf("add two items: %d %s", filled.Code, filled.Body.String())
	}

	rows, err := server.app.db.Query(server.ctx(),
		`SELECT DISTINCT i.work_item_id FROM report_items i
		 WHERE i.report_id = $1 AND i.work_item_id IS NOT NULL ORDER BY 1`, reportID)
	if err != nil {
		t.Fatalf("read work items: %v", err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan work item: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) < 2 {
		t.Fatalf("the report has two items and produced %d work items", len(ids))
	}
	return ids[0], ids[1]
}

// guards: mergeWorkItem
func TestOnlyTheOwnerFoldsTwoTasksIntoOne(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("정리 내 조직", "TIDYMINE")
	theirs := server.createOrganization("정리 남의 조직", "TIDYTHEIRS")
	owner := server.createUser("tidy_owner", "USER", &mine)
	outsider := server.createUser("tidy_outsider", "USER", &theirs)

	first, second := twoWorkItemsOf(t, server, owner)
	path := fmt.Sprintf("/api/v1/work-items/%d/merge", first)

	// Both tasks belong to the same person, so the "different owners" check
	// cannot fire and the request reaches the rule this test is about.
	refused(t, "folding somebody else's two tasks together",
		server.request(http.MethodPost, path, map[string]any{"intoId": second}, outsider))

	var merged *int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT merged_into_id FROM work_items WHERE id=$1`, first).Scan(&merged); err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if merged != nil {
		t.Errorf("the refused merge happened anyway: %d was folded into %d", first, *merged)
	}
}

// guards: splitWorkItem
func TestOnlyTheOwnerSplitsATaskInTwo(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("정리 내 조직", "TIDYMINE")
	theirs := server.createOrganization("정리 남의 조직", "TIDYTHEIRS")
	owner := server.createUser("tidy_owner", "USER", &mine)
	outsider := server.createUser("tidy_outsider", "USER", &theirs)

	workItemID := workItemOf(t, server, owner, "2026-08-24", "쪼갤 업무가 담긴 보고")
	var reportItemID int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT id FROM report_items WHERE work_item_id=$1 LIMIT 1`, workItemID).Scan(&reportItemID); err != nil {
		t.Fatalf("find a report item: %v", err)
	}

	refused(t, "splitting somebody else's task",
		server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/split", workItemID),
			map[string]any{"title": "빼앗은 조각", "category": "운영", "reportItemIds": []int64{reportItemID}}, outsider))

	var created int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM work_items WHERE title=$1`, "빼앗은 조각").Scan(&created); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if created != 0 {
		t.Errorf("the refused split created %d task(s) anyway", created)
	}
}

// guards: listWorkItemLinks, createWorkItemLink
func TestPredecessorsAreDeclaredOnlyOnYourOwnWork(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("선행 내 조직", "LINKMINE")
	theirs := server.createOrganization("선행 남의 조직", "LINKTHEIRS")
	owner := server.createUser("link_owner", "USER", &mine)
	outsider := server.createUser("link_outsider", "USER", &theirs)

	blocked, blocker := twoWorkItemsOf(t, server, owner)
	path := fmt.Sprintf("/api/v1/work-items/%d/links", blocked)

	if own := server.request(http.MethodGet, path, nil, owner); own.Code != http.StatusOK {
		t.Fatalf("the owner cannot read their own task's links: %d %s", own.Code, own.Body.String())
	}
	refused(t, "reading another organisation's task links",
		server.request(http.MethodGet, path, nil, outsider))
	refused(t, "declaring a predecessor on somebody else's task",
		server.request(http.MethodPost, path, map[string]any{"blockerId": blocker}, outsider))

	var links int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM work_item_links WHERE blocked_id=$1`, blocked).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Errorf("the refused declaration created %d link(s) anyway", links)
	}
}

// guards: deleteWorkItemLink
func TestOnlyTheTwoEndsRemoveAPredecessorLink(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("선행 내 조직", "LINKMINE")
	theirs := server.createOrganization("선행 남의 조직", "LINKTHEIRS")
	owner := server.createUser("link_owner", "USER", &mine)
	outsider := server.createUser("link_outsider", "USER", &theirs)

	blocked, blocker := twoWorkItemsOf(t, server, owner)
	created := server.request(http.MethodPost, fmt.Sprintf("/api/v1/work-items/%d/links", blocked),
		map[string]any{"blockerId": blocker, "note": "이것이 끝나야 한다"}, owner)
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("declare a predecessor: %d %s", created.Code, created.Body.String())
	}
	var linkID int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT id FROM work_item_links WHERE blocked_id=$1`, blocked).Scan(&linkID); err != nil {
		t.Fatalf("find the link: %v", err)
	}

	refused(t, "removing a link between two other people's tasks",
		server.request(http.MethodDelete, fmt.Sprintf("/api/v1/work-item-links/%d", linkID), nil, outsider))

	var remaining int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM work_item_links WHERE id=$1`, linkID).Scan(&remaining); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if remaining != 1 {
		t.Error("the refused delete removed the link anyway")
	}
}

// guards: listWorkItems
func TestOrganisationWideTaskListIsForLeaders(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("업무 조직", "TASKORG")
	leader := server.createUser("task_leader", "TEAM_LEADER", &organisation)
	writer := server.createUser("task_writer", "USER", &organisation)

	if own := server.request(http.MethodGet, "/api/v1/work-items?scope=SELF", nil, writer); own.Code != http.StatusOK {
		t.Fatalf("a writer cannot list their own tasks: %d %s", own.Code, own.Body.String())
	}
	refused(t, "a writer listing the organisation's tasks",
		server.request(http.MethodGet, "/api/v1/work-items?scope=TEAM", nil, writer))
	if asLeader := server.request(http.MethodGet, "/api/v1/work-items?scope=TEAM", nil, leader); asLeader.Code != http.StatusOK {
		t.Fatalf("a leader cannot list the organisation's tasks: %d %s", asLeader.Code, asLeader.Body.String())
	}
}
