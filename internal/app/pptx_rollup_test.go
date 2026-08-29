package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func sampleRollup(t *testing.T, kind, period string, itemCount, linesPerItem int) rollupView {
	t.Helper()
	resolved, err := resolvePeriod(kind, period, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "MONDAY")
	if err != nil {
		t.Fatal(err)
	}
	entries := []sourceEntry{}
	weeks := expectedWeekStarts(resolved, "MONDAY", time.Time{})
	for index := 0; index < itemCount; index++ {
		details := make([]string, 0, linesPerItem)
		for line := 0; line < linesPerItem; line++ {
			details = append(details, fmt.Sprintf("업무 %d의 세부 실적 항목 %d 상세 설명입니다", index, line))
		}
		week := weeks[index%len(weeks)]
		entries = append(entries, sourceEntry{
			ReportID: int64(index + 1), UserID: 1, DisplayName: "장현경", WeekStart: week, Status: "CLOSED",
			Category: fmt.Sprintf("구분%d", index%4), Title: fmt.Sprintf("업무 항목 %d", index),
			CurrentResult: strings.Join(details, "\n"), NextPlan: strings.Join(details, "\n"),
			Issue: map[bool]string{true: "지원 필요", false: ""}[index%3 == 0], Progress: (index * 17) % 101,
		})
	}
	reports := []reportListItem{}
	for index, week := range weeks {
		reports = append(reports, reportListItem{ID: int64(index + 1), UserID: 1, DisplayName: "장현경", WeekStart: week})
	}
	return buildRollup(resolved, scopeSelf, "장현경", entries, reports, weeks, defaultRollupConfig())
}

func readDeck(t *testing.T, body []byte) (slides []string, names map[string]bool) {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names = map[string]bool{}
	slideParts := map[int]string{}
	highest := 0
	for _, file := range reader.File {
		names[file.Name] = true
		match := slideNumberPattern.FindStringSubmatch(file.Name)
		if match == nil {
			continue
		}
		stream, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		var buffer bytes.Buffer
		if _, copyErr := buffer.ReadFrom(stream); copyErr != nil {
			t.Fatal(copyErr)
		}
		stream.Close()
		number := 0
		fmt.Sscanf(match[1], "%d", &number)
		slideParts[number] = buffer.String()
		if number > highest {
			highest = number
		}
	}
	for index := 1; index <= highest; index++ {
		slides = append(slides, slideParts[index])
	}
	return slides, names
}

func TestRollupDeckIsValidAndPaginated(t *testing.T) {
	// A year of long items is exactly the case that overflowed the fixed
	// four slide weekly frame.
	view := sampleRollup(t, periodYear, "2026", 40, 9)
	deck, err := buildRollupDeck(view)
	if err != nil {
		t.Fatal(err)
	}
	validatePPTXXML(t, deck)
	slides, names := readDeck(t, deck)
	if len(slides) < 8 {
		t.Fatalf("slide count = %d, want the content spread over many slides", len(slides))
	}
	for _, required := range []string{"[Content_Types].xml", "ppt/presentation.xml", "ppt/_rels/presentation.xml.rels", "ppt/slideMasters/slideMaster1.xml", "ppt/theme/theme1.xml"} {
		if !names[required] {
			t.Errorf("missing required part %s", required)
		}
	}
	// Every slide must have its own relationship part and a content type override.
	for index := range slides {
		rels := fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", index+1)
		if !names[rels] {
			t.Errorf("slide %d has no relationship part", index+1)
		}
	}
	joined := strings.Join(slides, "")
	for _, heading := range []string{"경영 인사이트", "업무 현황", "업무 상세"} {
		if !strings.Contains(joined, heading) {
			t.Errorf("deck is missing the %q section", heading)
		}
	}
	if !strings.Contains(slides[0], "연간 업무보고") {
		t.Error("cover slide must name the period kind")
	}
}

func TestRollupDeckShortPeriodStaysCompact(t *testing.T) {
	view := sampleRollup(t, periodMonth, "2026-08", 3, 2)
	deck, err := buildRollupDeck(view)
	if err != nil {
		t.Fatal(err)
	}
	validatePPTXXML(t, deck)
	slides, _ := readDeck(t, deck)
	if len(slides) > 6 {
		t.Errorf("a three item month produced %d slides, want a compact deck", len(slides))
	}
	if len(slides) < 3 {
		t.Errorf("slide count = %d, want at least cover, status and detail", len(slides))
	}
}

func TestPaginateDetailNeverExceedsTheBudget(t *testing.T) {
	items := []rollupItem{}
	for index := 0; index < 25; index++ {
		lines := make([]string, index%7+1)
		for line := range lines {
			lines[line] = fmt.Sprintf("세부 %d", line)
		}
		items = append(items, rollupItem{
			Title: fmt.Sprintf("업무 %d", index), Category: "구분",
			CurrentResult: strings.Join(lines, "\n"), NextPlan: strings.Join(lines, "\n"),
		})
	}
	linesPerItem, linesPerSlide := rollupDetailBudget(periodQuarter)
	pages := paginateDetail(items, linesPerItem, linesPerSlide)
	if len(pages) < 2 {
		t.Fatalf("page count = %d, want the items split across slides", len(pages))
	}
	seen := 0
	for pageIndex, page := range pages {
		used := 0
		for _, item := range page.Items {
			used += detailItemCost(item, linesPerItem)
		}
		// A single oversized item is allowed to fill its own slide; otherwise the
		// packer must respect the budget.
		if used > linesPerSlide && len(page.Items) > 1 {
			t.Errorf("page %d uses %d lines, above the %d budget with %d items", pageIndex, used, linesPerSlide, len(page.Items))
		}
		seen += len(page.Items)
	}
	if seen != len(items) {
		t.Errorf("pagination kept %d of %d items", seen, len(items))
	}
	// Report order must survive pagination.
	position := 0
	for _, page := range pages {
		for _, item := range page.Items {
			if item.Title != fmt.Sprintf("업무 %d", position) {
				t.Fatalf("item order changed at %d: %s", position, item.Title)
			}
			position++
		}
	}
}

func TestCappedLinesAnnouncesWhatItDropped(t *testing.T) {
	value := strings.Join([]string{"하나", "둘", "셋", "넷", "다섯"}, "\n")
	kept := cappedLines(value, 3)
	if len(kept) != 4 {
		t.Fatalf("kept %d lines, want 3 plus a marker", len(kept))
	}
	if kept[3] != "외 2건" {
		t.Errorf("marker = %q, want 외 2건", kept[3])
	}
	if got := cappedLines(value, 10); len(got) != 5 {
		t.Errorf("an under-limit value must pass through, got %d lines", len(got))
	}
}

func TestRollupDetailBudgetTightensForLongPeriods(t *testing.T) {
	monthLines, _ := rollupDetailBudget(periodMonth)
	yearLines, _ := rollupDetailBudget(periodYear)
	if yearLines >= monthLines {
		t.Errorf("year budget %d must be tighter than the month budget %d", yearLines, monthLines)
	}
}

func TestFitInsidePreservesAspectRatio(t *testing.T) {
	// A wide capture is limited by the box width.
	x, y, cx, cy := fitInside(0, 0, 1000, 1000, 2000, 1000)
	if cx != 1000 || cy != 500 {
		t.Errorf("wide image scaled to %dx%d, want 1000x500", cx, cy)
	}
	if y != 250 || x != 0 {
		t.Errorf("wide image placed at %d,%d, want it centred vertically", x, y)
	}
	// A tall capture is limited by the box height.
	x, y, cx, cy = fitInside(0, 0, 1000, 1000, 1000, 4000)
	if cx != 250 || cy != 1000 {
		t.Errorf("tall image scaled to %dx%d, want 250x1000", cx, cy)
	}
	if x != 375 || y != 0 {
		t.Errorf("tall image placed at %d,%d, want it centred horizontally", x, y)
	}
	// A degenerate size must fall back to the box rather than divide by zero.
	if _, _, width, _ := fitInside(0, 0, 800, 600, 0, 0); width != 800 {
		t.Error("a zero sized image must fall back to the box")
	}
}
