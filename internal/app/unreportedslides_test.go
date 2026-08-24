package app

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// deckWithEmptySlides builds normalised PPTX text where `empty` slide numbers
// carry only the scaffolding a divider or a blank table produces.
func deckWithEmptySlides(total int, empty ...int) string {
	blank := map[int]bool{}
	for _, number := range empty {
		blank[number] = true
	}
	var text strings.Builder
	for slide := 1; slide <= total; slide++ {
		fmt.Fprintf(&text, "=== SLIDE %d (SOURCE slide%d.xml) ===\n", slide, slide)
		switch {
		case blank[slide]:
			text.WriteString("[SHAPE 1 name=\"Rectangle\"]\n[TABLE 1]\n[ROW 1]\n[COL 1]\n[EMPTY]\n")
		default:
			fmt.Fprintf(&text, "[SHAPE 1]\n업무 %d 관련 실적\n", slide)
		}
		text.WriteString("\n")
	}
	return text.String()
}

func itemsCiting(slides ...int) []aiReportItem {
	items := make([]aiReportItem, 0, len(slides))
	for _, slide := range slides {
		items = append(items, aiReportItem{
			Category: "인프라", Title: fmt.Sprintf("업무 %d", slide), CurrentResult: "완료",
			Confidence: 0.95, CategoryConfidence: 0.95, SourceSlides: []int{slide}, Progress: 100,
		})
	}
	return items
}

func finalizeDeck(t *testing.T, normalized string, items []aiReportItem) (aiWeeklyResult, importAnalysisDecision) {
	t.Helper()
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	result := aiWeeklyResult{Summary: "요약", ReportItems: items, DateConfidence: 0.95}
	detected := detectedWeek{Start: monday, End: monday.AddDate(0, 0, 6), Confidence: 0.95, Source: "filename"}
	decision := finalizeImportedAIResult(&result, detected, extractedPPTX{Normalized: normalized, SlideCount: 12},
		"MONDAY", time.UTC)
	return result, decision
}

func warningAboutUnreportedSlides(warnings []string) string {
	for _, warning := range warnings {
		if strings.Contains(warning, "근거로도 쓰이지 않았습니다") {
			return warning
		}
	}
	return ""
}

// A deck whose work silently failed to reach the draft is the failure this
// pipeline cannot see on its own: the reviewer reads what is there, never what
// is absent.
func TestUnreportedSlidesAreNamedAndHeldForReview(t *testing.T) {
	deck := deckWithEmptySlides(12, 4, 7, 9)
	result, decision := finalizeDeck(t, deck, itemsCiting(1, 2, 3))

	warning := warningAboutUnreportedSlides(result.Warnings)
	if warning == "" {
		t.Fatalf("9 slides carried text and 3 were reported, yet nothing was said: %v", result.Warnings)
	}
	for _, slide := range []string{"5", "6", "8", "10", "11", "12"} {
		if !strings.Contains(warning, slide) {
			t.Errorf("slide %s went unreported but the warning does not name it: %q", slide, warning)
		}
	}
	// Dividers and blank tables are not missing work, and naming them would
	// train the reader to ignore the whole warning.
	for _, blank := range []string{"4", "7", "9"} {
		if strings.Contains(warning, " "+blank+",") || strings.HasSuffix(warning, " "+blank+"가") {
			t.Errorf("empty slide %s was reported as unreported work: %q", blank, warning)
		}
	}
	if decision.Status != "NEEDS_REVIEW" {
		t.Errorf("a deck missing 6 of 9 content slides stayed %q, so it is pre-selected for confirmation", decision.Status)
	}
}

// The other half: a warning that fires on every import is a warning nobody reads.
func TestFullyReportedDeckStaysQuietAndReady(t *testing.T) {
	deck := deckWithEmptySlides(12, 4, 7, 9)
	result, decision := finalizeDeck(t, deck, itemsCiting(1, 2, 3, 5, 6, 8, 10, 11, 12))

	if warning := warningAboutUnreportedSlides(result.Warnings); warning != "" {
		t.Errorf("every content slide was cited, yet: %q", warning)
	}
	if decision.Status != "READY" {
		t.Errorf("a fully reported deck was held for review anyway: %q (%v)", decision.Status, result.Warnings)
	}
}

// The list is capped so a sixty-slide deck does not produce a warning nobody
// finishes reading. The count has to survive the cap: a shortened list that
// also shortens the number would understate what went missing.
func TestManyUnreportedSlidesKeepTheirCount(t *testing.T) {
	deck := deckWithEmptySlides(60)
	result, _ := finalizeDeck(t, deck, itemsCiting(1))

	warning := warningAboutUnreportedSlides(result.Warnings)
	if !strings.Contains(warning, "59장") {
		t.Errorf("59 slides went unreported but the warning does not say so: %q", warning)
	}
	if !strings.Contains(warning, "외 39장") {
		t.Errorf("the shortened list does not say how many it left out: %q", warning)
	}
	if runeLength(warning) > 500 {
		t.Errorf("the warning is %d runes; nobody reads to the end of that: %q", runeLength(warning), warning)
	}
}
