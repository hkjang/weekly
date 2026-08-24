package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
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
	// One task, one page: renderReferencePPTX stops at the last slide that
	// carries work rather than emitting the template's full four.
	if len(extracted.Slides) != 1 || extracted.SlideCount != 1 || extracted.Truncated ||
		!strings.Contains(extracted.Normalized, "AI 구조화 완료") || !strings.Contains(extracted.Normalized, "[TABLE 1]") ||
		!strings.Contains(extracted.Normalized, "[ROW 1]") || !strings.Contains(extracted.Normalized, "[COL 1]") {
		t.Fatalf("unexpected extraction: slides=%d text=%s", len(extracted.Slides), extracted.Normalized)
	}
	detected := detectPPTXWeek("weekly.pptx", extracted.Normalized, "MONDAY", time.Local)
	if detected.Start.Format("2006-01-02") != "2026-08-03" || detected.End.Format("2006-01-02") != "2026-08-09" || detected.Source != "slide_text" || detected.Confidence < 0.9 {
		t.Fatalf("unexpected detected week: %#v", detected)
	}
}

func TestDetectPPTXWeekAlignsStandaloneDateToConfiguredStart(t *testing.T) {
	detected := detectPPTXWeek("weekly.pptx", "작성일 2026-08-06", "TUESDAY", time.UTC)
	if got := detected.Start.Format("2006-01-02"); got != "2026-08-04" {
		t.Fatalf("week start = %s", got)
	}
	if got := detected.End.Format("2006-01-02"); got != "2026-08-10" {
		t.Fatalf("week end = %s", got)
	}
	if detected.Confidence >= 0.9 {
		t.Fatalf("standalone date confidence is too high: %f", detected.Confidence)
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

func TestParseImportSlidePreservesParagraphRunsBulletsAndTableCoordinates(t *testing.T) {
	slideXML := `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
 <p:cSld><p:spTree>
  <p:sp><p:nvSpPr><p:cNvPr id="2" name="업무 제목"/></p:nvSpPr><p:txBody>
   <a:p><a:pPr lvl="1"><a:buChar char="•"/></a:pPr><a:r><a:t>분할된 </a:t></a:r><a:r><a:t>문장</a:t></a:r></a:p>
   <a:p><a:pPr><a:buChar char="-"/></a:pPr><a:r><a:t>두 번째 문단</a:t></a:r></a:p>
  </p:txBody></p:sp>
  <p:graphicFrame><a:graphic><a:graphicData><a:tbl>
   <a:tr><a:tc><a:txBody><a:p><a:r><a:t>실적</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p/></a:txBody></a:tc></a:tr>
   <a:tr><a:tc><a:txBody><a:p><a:pPr lvl="0"><a:buChar char="*"/></a:pPr><a:r><a:t>완료 </a:t></a:r><a:r><a:t>항목</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>계획</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
  </a:tbl></a:graphicData></a:graphic></p:graphicFrame>
 </p:spTree></p:cSld>
</p:sld>`

	slide, err := parseImportSlideXML(strings.NewReader(slideXML), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(slide.Shapes) != 1 || slide.Shapes[0].Name != "업무 제목" || len(slide.Shapes[0].Paragraphs) != 2 {
		t.Fatalf("unexpected shapes: %#v", slide.Shapes)
	}
	paragraph := slide.Shapes[0].Paragraphs[0]
	if paragraph.Text != "분할된 문장" || paragraph.Bullet != "•" || !paragraph.HasBullet || paragraph.Level != 1 || !paragraph.HasLevel {
		t.Fatalf("split runs or bullet metadata lost: %#v", paragraph)
	}
	if len(slide.Tables) != 1 || len(slide.Tables[0].Rows) != 2 || len(slide.Tables[0].Rows[0].Cells) != 2 {
		t.Fatalf("table geometry lost: %#v", slide.Tables)
	}
	if len(slide.Tables[0].Rows[0].Cells[1].Paragraphs) != 0 {
		t.Fatalf("empty cell was not retained: %#v", slide.Tables[0].Rows[0].Cells[1])
	}
	if got := slide.Tables[0].Rows[1].Cells[0].Paragraphs[0].Text; got != "완료 항목" {
		t.Fatalf("table split runs = %q", got)
	}
	if len(slide.Cells) != 4 || len(slide.Cells[1]) != 0 {
		t.Fatalf("legacy cells must retain empty coordinates: %#v", slide.Cells)
	}
	formatted := formatExtractedSlide(extractedSlideWithOrder(slide, 1))
	for _, expected := range []string{
		`=== SLIDE 1 (SOURCE slide7.xml) ===`, `[SHAPE 1 name="업무 제목"]`,
		`PARAGRAPH 1 bullet="•" level=1: 분할된 문장`, `[TABLE 1]`, `[ROW 1]`, `[COL 2]`, `[EMPTY]`,
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("normalized slide missing %q:\n%s", expected, formatted)
		}
	}
}

func TestExtractPPTXUsesPresentationRelationshipOrder(t *testing.T) {
	body := generatedImportPPTX(t, []int{2, 1, 3}, map[int]string{
		1: importTextSlideXML("XML ONE"),
		2: importTextSlideXML("XML TWO"),
		3: importTextSlideXML("XML THREE"),
	})
	extracted, err := extractPPTX(body, 50000)
	if err != nil {
		t.Fatal(err)
	}
	if extracted.SlideCount != 3 || len(extracted.Slides) != 3 {
		t.Fatalf("slide count = %d/%d", extracted.SlideCount, len(extracted.Slides))
	}
	if got := []int{extracted.Slides[0].Number, extracted.Slides[1].Number, extracted.Slides[2].Number}; fmt.Sprint(got) != "[2 1 3]" {
		t.Fatalf("presentation order = %v", got)
	}
	indices := []int{
		strings.Index(extracted.Normalized, "XML TWO"),
		strings.Index(extracted.Normalized, "XML ONE"),
		strings.Index(extracted.Normalized, "XML THREE"),
	}
	if indices[0] < 0 || indices[0] >= indices[1] || indices[1] >= indices[2] {
		t.Fatalf("normalized presentation order incorrect: %v\n%s", indices, extracted.Normalized)
	}
}

func TestExtractPPTXDistributesTruncationBudgetAcrossSlides(t *testing.T) {
	long := func(marker string) string {
		return importTextSlideXML(marker + " " + strings.Repeat("긴 업무 설명 ", 100))
	}
	body := generatedImportPPTX(t, []int{1, 2, 3}, map[int]string{
		1: long("FIRST_MARKER"), 2: long("SECOND_MARKER"), 3: long("THIRD_MARKER"),
	})
	extracted, err := extractPPTX(body, 600)
	if err != nil {
		t.Fatal(err)
	}
	if !extracted.Truncated || fmt.Sprint(extracted.TruncatedSlides) != "[1 2 3]" {
		t.Fatalf("truncation metadata = truncated:%v slides:%v", extracted.Truncated, extracted.TruncatedSlides)
	}
	if runeLength(extracted.Normalized) > 600 {
		t.Fatalf("normalized length %d exceeds budget", runeLength(extracted.Normalized))
	}
	for _, marker := range []string{"=== SLIDE 1", "=== SLIDE 2", "=== SLIDE 3", "SLIDE 1 CONTENT TRUNCATED", "SLIDE 2 CONTENT TRUNCATED", "SLIDE 3 CONTENT TRUNCATED"} {
		if !strings.Contains(extracted.Normalized, marker) {
			t.Fatalf("fairly truncated output missing %q:\n%s", marker, extracted.Normalized)
		}
	}
}

func TestOrderExtractedSlidesFallsBackToNumericOrderForUnreferencedSlides(t *testing.T) {
	slides := map[int]extractedSlide{3: {Number: 3}, 1: {Number: 1}, 2: {Number: 2}}
	presentation := []byte(`<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId r:id="rId3"/></p:sldIdLst></p:presentation>`)
	relationships := []byte(`<Relationships><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide3.xml"/></Relationships>`)
	ordered := orderExtractedSlides(slides, presentation, relationships)
	if got := []int{ordered[0].Number, ordered[1].Number, ordered[2].Number}; fmt.Sprint(got) != "[3 1 2]" {
		t.Fatalf("order with fallback = %v", got)
	}
}

func TestFairTextBudgetsAreDeterministic(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		if got := fmt.Sprint(fairTextBudgets([]int{2, 100, 100}, 12)); got != "[2 5 5]" {
			t.Fatalf("water-filled budgets = %s", got)
		}
		if got := fmt.Sprint(fairTextBudgets([]int{100, 100, 100}, 10)); got != "[4 3 3]" {
			t.Fatalf("remainder must be assigned in slide order: %s", got)
		}
	}
}

func extractedSlideWithOrder(slide extractedSlide, order int) extractedSlide {
	slide.Order = order
	return slide
}

func importTextSlideXML(text string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:nvSpPr><p:cNvPr id="2" name="Body"/></p:nvSpPr><p:txBody><a:p><a:r><a:t>` + escapeXML(text) + `</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
}

func generatedImportPPTX(t *testing.T, order []int, slides map[int]string) []byte {
	t.Helper()
	var slideIDs, relationships strings.Builder
	for index, number := range order {
		identifier := fmt.Sprintf("rId%d", index+1)
		fmt.Fprintf(&slideIDs, `<p:sldId id="%d" r:id="%s"/>`, 256+index, identifier)
		fmt.Fprintf(&relationships, `<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, identifier, number)
	}
	files := map[string]string{
		"ppt/presentation.xml":            `<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst>` + slideIDs.String() + `</p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0"?><Relationships>` + relationships.String() + `</Relationships>`,
	}
	for number, document := range slides {
		files[fmt.Sprintf("ppt/slides/slide%d.xml", number)] = document
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, document := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, document); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
