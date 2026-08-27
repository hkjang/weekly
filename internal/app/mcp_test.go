package app

import (
	"fmt"
	"strings"
	"testing"
)

// An omitted argument must read as omitted. fmt.Sprint turns a missing map key
// into "<nil>", which every `if value == ""` guard in the tool dispatcher then
// waves through as a real value. weekly_reports_search with no arguments sent
// those four characters to PostgreSQL as a date, so the first question an agent
// asks — what reports are there? — always failed.
func TestOmittedMCPArgumentsReadAsEmptyNotNilText(t *testing.T) {
	arguments := map[string]any{"status": "APPROVED", "empty": "", "blank": nil, "number": float64(7)}
	cases := []struct{ name, want string }{
		{"weekStart", ""}, // absent
		{"blank", ""},     // present but null
		{"empty", ""},     // present and empty
		{"status", "APPROVED"},
		{"number", "7"},
	}
	for _, testCase := range cases {
		if got := mcpArgumentString(arguments, testCase.name); got != testCase.want {
			t.Errorf("mcpArgumentString(%q) = %q, want %q", testCase.name, got, testCase.want)
		}
	}
	if got := asString(arguments["weekStart"]); got != "<nil>" {
		t.Fatalf("asString on a missing key returned %q; this test exists because it returns \"<nil>\"", got)
	}
}

// Tool arguments arrive as JSON, so a number is a float64 — and an agent may
// well send it as text instead.
func TestMCPArgumentIntAcceptsWhatJSONActuallyDelivers(t *testing.T) {
	cases := []struct {
		arguments map[string]any
		want      int
	}{
		{map[string]any{}, 100},                        // absent
		{map[string]any{"limit": nil}, 100},            // null
		{map[string]any{"limit": float64(30)}, 30},     // JSON number
		{map[string]any{"limit": "30"}, 30},            // text
		{map[string]any{"limit": float64(9999)}, 100},  // above the ceiling
		{map[string]any{"limit": float64(0)}, 1},       // below the floor
		{map[string]any{"limit": "not a number"}, 100}, // unreadable
	}
	for _, testCase := range cases {
		if got := mcpArgumentInt(testCase.arguments, "limit", 100, 1, 100); got != testCase.want {
			t.Errorf("mcpArgumentInt(%v) = %d, want %d", testCase.arguments, got, testCase.want)
		}
	}
}

// Two doors to the same period rollup. The screen's has been trimming the
// weekly series since it turned out to be 522 KB of an 885 KB response; this
// one returned loadRollup's view untouched. Measured on a 300 person
// deployment, the year at organisation scope came back as 2,563,406 bytes —
// six times what the screen sends, to a caller whose entire view of the data is
// that payload and who cannot scroll.

// guards: rollupForModel
func TestTheRollupAModelReadsIsTrimmedAndSaysSo(t *testing.T) {
	app := &App{}
	view := rollupView{Kind: periodYear, Period: "2026", Scope: scopeTeam}
	const tasks = mcpRollupItems + 63
	for index := 0; index < tasks; index++ {
		view.Items = append(view.Items, rollupItem{
			Key: fmt.Sprintf("k%d", index), Title: fmt.Sprintf("업무 %03d", index),
			Weeks: []rollupItemWeek{{WeekStart: "2026-06-01", Progress: 10}, {WeekStart: "2026-06-08", Progress: 30}},
		})
	}

	data := app.rollupForModel(&view)
	items, _ := data["items"].([]rollupItem)
	if len(items) != mcpRollupItems {
		t.Fatalf("returned %d rows, want %d", len(items), mcpRollupItems)
	}
	if total, _ := data["itemsTotal"].(int); total != tasks {
		t.Errorf("itemsTotal=%v, want %d — a caller that cannot scroll needs the number", data["itemsTotal"], tasks)
	}

	// The weekly series rides on the rows a chart would draw, and no further.
	carrying := 0
	for _, item := range items {
		if len(item.Weeks) > 0 {
			carrying++
		}
	}
	if carrying != rollupTimelineItems {
		t.Errorf("%d rows carry a weekly series, want %d", carrying, rollupTimelineItems)
	}
	// And the field that says how many do has to agree with how many do. It
	// said zero while every row carried one.
	if declared, _ := data["timelineItems"].(int); declared != carrying {
		t.Errorf("timelineItems=%v while %d rows carry a series", data["timelineItems"], carrying)
	}

	// Told, not merely encoded. A field a model might not compare against
	// len(items) is a weaker signal than a sentence.
	note, _ := data["note"].(string)
	if !strings.Contains(note, fmt.Sprint(tasks)) {
		t.Errorf("the note does not say how many there were: %q", note)
	}
	if !strings.Contains(note, "전체로 보고 요약하지 마세요") {
		t.Errorf("the note does not warn against summarising a partial list: %q", note)
	}
	// An empty weeks list must not read as "nothing happened".
	if !strings.Contains(note, "진척이 없었다는 뜻이 아닙니다") {
		t.Errorf("the note does not say what an empty weeks list means: %q", note)
	}
}

// A rollup that fits says so plainly rather than warning about a truncation
// that did not happen.

// guards: rollupForModel
func TestASmallRollupIsNotDescribedAsPartial(t *testing.T) {
	app := &App{}
	view := rollupView{Kind: periodMonth, Period: "2026-08", Scope: scopeSelf}
	for index := 0; index < 3; index++ {
		view.Items = append(view.Items, rollupItem{Key: fmt.Sprintf("k%d", index), Title: "업무"})
	}
	data := app.rollupForModel(&view)
	if items, _ := data["items"].([]rollupItem); len(items) != 3 {
		t.Fatalf("returned %d rows, want 3", len(items))
	}
	if note, _ := data["note"].(string); strings.Contains(note, "전체로 보고 요약하지 마세요") {
		t.Errorf("a complete list was described as partial: %q", note)
	}
}

// The row cap is written from a reason about size — "the payload is its entire
// view" — but a row is not a fixed weight. Measured on a 300 person deployment,
// the same hundred rows weighed 108,414 bytes for a month and 169,341 for a
// year, because a row carries the prose of every week it appeared in. A count
// bounds a table; the caller here has only a context window.
//
// guards: rollupForModel
func TestTheRollupAModelReadsIsBoundedByWeightNotOnlyByRows(t *testing.T) {
	app := &App{}
	view := rollupView{Kind: periodYear, Period: "2026", Scope: scopeTeam}
	// Rows the size the field measures them at: a year's worth of weekly prose
	// on the rows that carry a series, and a plain row behind them.
	for index := 0; index < mcpRollupItems*3; index++ {
		item := rollupItem{
			Key: fmt.Sprintf("k%d", index), Title: fmt.Sprintf("업무 %03d", index),
			CurrentResult: strings.Repeat("배포 준비를 마쳤습니다. ", 12),
			NextPlan:      strings.Repeat("다음 주에는 인수인계를 정리합니다. ", 12),
		}
		for week := 0; week < 52; week++ {
			item.Weeks = append(item.Weeks, rollupItemWeek{
				WeekStart: fmt.Sprintf("2026-%02d-01", week%12+1), Progress: week % 100,
			})
		}
		view.Items = append(view.Items, item)
	}

	data := app.rollupForModel(&view)
	size := encodedSize(data)
	if size > mcpRollupBytes {
		t.Errorf("the model's whole view is %d bytes, over the %d budget", size, mcpRollupBytes)
	}
	items, _ := data["items"].([]rollupItem)
	if len(items) == 0 {
		t.Fatal("the budget left the caller nothing at all")
	}
	if len(items) >= mcpRollupItems {
		t.Errorf("rows this heavy should have been cut past the row cap, kept %d", len(items))
	}
	// The series are shed first because they buy the most bytes per row lost —
	// but not to nothing, or the sentence explaining them describes nothing and
	// the caller loses every trace of shape.
	if declared, _ := data["timelineItems"].(int); declared != mcpRollupTimelineFloor {
		t.Errorf("timelineItems=%v, want the floor %d", data["timelineItems"], mcpRollupTimelineFloor)
	}
	// And it says the cut happened, and why — a caller told only "100 of 300"
	// may reasonably page for the rest, and the next page would weigh the same.
	note, _ := data["note"].(string)
	if !strings.Contains(note, fmt.Sprint(len(items))) {
		t.Errorf("the note does not say how many came back: %q", note)
	}
	if !strings.Contains(note, "응답 크기 상한") {
		t.Errorf("the note does not say the size was what cut it: %q", note)
	}
}

// A rollup that already fits must not be described as cut for size.
//
// guards: rollupForModel
func TestARollupInsideTheBudgetIsNotDescribedAsCutForSize(t *testing.T) {
	app := &App{}
	view := rollupView{Kind: periodMonth, Period: "2026-08", Scope: scopeSelf}
	for index := 0; index < 5; index++ {
		view.Items = append(view.Items, rollupItem{
			Key: fmt.Sprintf("k%d", index), Title: fmt.Sprintf("업무 %d", index),
		})
	}
	data := app.rollupForModel(&view)
	if note, _ := data["note"].(string); strings.Contains(note, "응답 크기 상한") {
		t.Errorf("a rollup that fits was described as trimmed for size: %q", note)
	}
	if items, _ := data["items"].([]rollupItem); len(items) != 5 {
		t.Errorf("a rollup that fits lost rows: %d of 5", len(items))
	}
}
