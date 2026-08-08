package app

import (
	"strings"
	"testing"
	"time"
)

func TestExtractAndDetectGeneratedReferencePPTX(t *testing.T) {
	template, err := referenceStylePPTX()
	if err != nil {
		t.Fatal(err)
	}
	report := &reportView{Items: []reportItem{{Category: "플랫폼", Title: "Weekly", CurrentResult: "AI 구조화 완료", NextPlan: "Import 검증"}}}
	body, err := renderReferencePPTX(template, report, "AI엔지니어링 파트", time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := extractPPTX(body, 50000)
	if err != nil {
		t.Fatal(err)
	}
	if len(extracted.Slides) != 4 || !strings.Contains(extracted.Normalized, "AI 구조화 완료") || !strings.Contains(extracted.Normalized, "TABLE_CELLS_IN_ORDER") {
		t.Fatalf("unexpected extraction: slides=%d text=%s", len(extracted.Slides), extracted.Normalized)
	}
	detected := detectPPTXWeek("weekly.pptx", extracted.Normalized, "MONDAY", time.Local)
	if detected.Start.Format("2006-01-02") != "2026-08-03" || detected.End.Format("2006-01-02") != "2026-08-07" || detected.Source != "slide_text" || detected.Confidence < 0.9 {
		t.Fatalf("unexpected detected week: %#v", detected)
	}
}

func TestDetectPPTXWeekFromFilename(t *testing.T) {
	detected := detectPPTXWeek("주간보고_20251017.pptx", "날짜 없음", "MONDAY", time.UTC)
	if got := detected.Start.Format("2006-01-02"); got != "2025-10-13" {
		t.Fatalf("week start = %s", got)
	}
}

func TestExtractPPTXRejectsInvalidArchive(t *testing.T) {
	if _, err := extractPPTX([]byte("not a pptx"), 50000); err == nil {
		t.Fatal("invalid archive must be rejected")
	}
}
