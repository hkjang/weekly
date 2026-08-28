package app

import (
	"strings"
	"testing"
)

// The portfolio card carried two numbers with two units under one word.
// Measured on a seeded deployment: "중복 46건 제거" sat beside "업무 35건을
// 7건으로 정리했습니다", and both were true — 35 rows, 7 work items, verified
// against the database — but nobody cuts 46 duplicates out of 35 items, so the
// card read as arithmetic that had gone wrong. DuplicatesCut counts repeated
// lines of prose dropped while a task's weeks were folded together;
// SourceItems→TotalItems counts rows folded into work items.
//
// guards: buildHighlights
func TestTheDeduplicationCardDoesNotCountTwoThingsAsOne(t *testing.T) {
	insights := rollupInsights{
		SourceItems: 35, TotalItems: 7, DedupRate: 80, DuplicatesCut: 46, MergedTitles: 0,
		ContinuingItems: 7,
	}
	highlights := buildHighlights(insights, nil, rollupConfig{})
	var card *rollupHighlight
	for index := range highlights {
		if strings.Contains(highlights[index].Title, "정리") || strings.Contains(highlights[index].Title, "중복") {
			card = &highlights[index]
		}
	}
	if card == nil {
		t.Fatal("the deduplication card is gone")
	}
	whole := card.Title + " " + card.Detail

	// The line count may only appear next to a word that says it is lines.
	position := strings.Index(whole, "46")
	if position < 0 {
		t.Fatalf("the line count is missing: %s", whole)
	}
	after := whole[position:]
	if !strings.HasPrefix(after, "46줄") {
		t.Errorf("46 is not marked as lines, so it reads as items: %s", whole)
	}
	// And the row counts have to stay together, so the reader can see 35 became 7.
	if !strings.Contains(whole, "35") || !strings.Contains(whole, "7건") {
		t.Errorf("the row reduction is not stated: %s", whole)
	}
	if strings.Contains(card.Title, "중복 46") {
		t.Errorf("the title still calls repeated lines duplicates in items: %s", card.Title)
	}
}
