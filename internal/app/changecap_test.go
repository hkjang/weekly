package app

import (
	"fmt"
	"testing"
)

// The dashboard draws its change bar from count alone and never reads entries,
// so nothing on any screen would have shown that the response carried 1,866
// rows across two groups. It only appears when you weigh the response: half a
// megabyte, serialised and discarded, on the screen the product opens on.

// guards: orderChangeEntries, buildChangeSummary
func TestAChangeGroupCapsItsRowsAndKeepsTheBiggestMovers(t *testing.T) {
	items := []workItemView{}
	for index := 0; index < changeGroupLimit+60; index++ {
		items = append(items, workItemView{
			ID: int64(index + 1), Title: fmt.Sprintf("업무 %03d", index), DisplayName: "담당",
			Weeks: []workItemWeek{
				{WeekStart: "2026-08-10", Progress: 5, CurrentResult: "진행"},
				{WeekStart: "2026-08-17", Progress: 5 + 1 + index%20, CurrentResult: "진행"},
			},
		})
	}
	// The largest movement of the week, created last so that it sits at the
	// very end of the unordered list. Without the ordering it falls outside the
	// cut and the group quietly drops the one row worth reading.
	items = append(items, workItemView{
		ID: 9001, Title: "가장 크게 움직인 업무", DisplayName: "담당",
		Weeks: []workItemWeek{
			{WeekStart: "2026-08-10", Progress: 5, CurrentResult: "진행"},
			{WeekStart: "2026-08-17", Progress: 90, CurrentResult: "진행"},
		},
	})
	for index := range items {
		summarizeWorkItem(&items[index], defaultRollupConfig())
	}
	view := buildChangeSummary(items, "2026-08-17", defaultRollupConfig())

	var moved *changeSummaryGroup
	for index := range view.Groups {
		if view.Groups[index].Kind == changeProgressed {
			moved = &view.Groups[index]
		}
	}
	if moved == nil {
		t.Fatal("no 진척 group was built")
	}
	if moved.Count <= changeGroupLimit {
		t.Fatalf("the fixture built only %d rows, so a cap of %d proves nothing", moved.Count, changeGroupLimit)
	}
	if len(moved.Entries) > changeGroupLimit {
		t.Errorf("the group carries %d rows, above the %d cap", len(moved.Entries), changeGroupLimit)
	}
	// count keeps the true number: it is what the dashboard bar is drawn from,
	// and capping the rows must not change what the screen says happened.
	if moved.Count != len(items) {
		t.Errorf("count=%d want=%d — the cap changed what the screen reports", moved.Count, len(items))
	}
	if moved.Limit != changeGroupLimit {
		t.Errorf("limit=%d want=%d", moved.Limit, changeGroupLimit)
	}
	if len(moved.Entries) == 0 || moved.Entries[0].Title != "가장 크게 움직인 업무" {
		t.Errorf("the biggest movement of the week did not lead the group: %+v", moved.Entries[:1])
	}
}
