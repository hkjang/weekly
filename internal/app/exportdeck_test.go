package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// deckText returns every slide's XML concatenated, so a test can ask whether a
// sentence reached the file rather than whether the handler returned 200.
func deckText(t *testing.T, body []byte) (string, []string) {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the export is not a readable zip (%d bytes): %v", len(body), err)
	}
	var text strings.Builder
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
		if !strings.HasPrefix(file.Name, "ppt/slides/slide") || !strings.HasSuffix(file.Name, ".xml") {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(opened)
		opened.Close()
		if err != nil {
			t.Fatal(err)
		}
		text.Write(content)
	}
	return text.String(), names
}

// The deck is what somebody carries into the meeting, and nothing had ever
// opened one. A handler that answers 200 with a file PowerPoint refuses is the
// same as a handler that fails, except it fails in the room.
//
// guards: exportReportPPTX
func TestTheExportedDeckIsAFileWithTheReportInIt(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("deck_author", "USER", nil)
	id, version := server.draft(author, "2026-08-24", "회의에 들고 갈 요약")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "회의에 들고 갈 요약", "version": version,
		"items": []map[string]any{{
			"category": "인프라", "title": "격오지 회선 이설", "currentResult": "3개 지사 완료",
			"nextPlan": "잔여 2개 지사", "issue": "임대 회선 일정 지연", "progress": 60,
		}},
	}, author)
	if filled.Code != http.StatusOK {
		t.Fatalf("fill the report: %d %s", filled.Code, filled.Body.String())
	}

	export := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
	if export.Code != http.StatusOK {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	if kind := export.Header().Get("Content-Type"); !strings.Contains(kind, "presentation") {
		t.Errorf("Content-Type is %q, which a browser will not treat as a deck", kind)
	}
	if disposition := export.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
		t.Errorf("Content-Disposition is %q, so the file opens in the tab instead of saving", disposition)
	}

	body := export.Body.Bytes()
	text, names := deckText(t, body)

	// A PPTX PowerPoint will open needs these parts. A zip missing them is a
	// file that downloads and then refuses to open, which is the worst possible
	// moment to find out.
	for _, required := range []string{"[Content_Types].xml", "ppt/presentation.xml", "_rels/.rels"} {
		found := false
		for _, name := range names {
			if name == required {
				found = true
			}
		}
		if !found {
			t.Errorf("the deck has no %s; PowerPoint will refuse it. Parts: %v", required, names)
		}
	}
	slides := 0
	for _, name := range names {
		if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") {
			slides++
		}
	}
	if slides == 0 {
		t.Fatalf("the deck has no slides at all: %v", names)
	}

	// And the report has to actually be in it.
	for _, phrase := range []string{"격오지 회선 이설", "3개 지사 완료", "잔여 2개 지사"} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the deck does not contain %q — it exported something, but not this report", phrase)
		}
	}

	// The issue the author wrote has to be in the file that goes to the meeting.
	// The built-in template has two columns — 추진실적 and 추진계획 — and no
	// third place, so this text used to reach the database and never the room.
	if !strings.Contains(text, "임대 회선 일정 지연") {
		t.Errorf("the issue is not in the deck; an issue absent from the meeting material does not get raised")
	}
	if !strings.Contains(text, "이슈 및 애로사항") {
		t.Errorf("the issue page has no heading, so the reader cannot tell what they are looking at")
	}

	// One task with one issue: one work page and one issue page. The template
	// is four slides and every export used to be four, the unused ones holding
	// nothing but the column headers and a pair of dashes.
	if slides != 2 {
		t.Errorf("a one-item report with an issue exported %d slides, want 2 (work + issues)", slides)
	}
}

// guards: issueSlide
//
// The page appears only when there is something to put on it, and says so when
// there is more than fits.
func TestTheIssuePageAppearsOnlyWhenThereAreIssues(t *testing.T) {
	width, height := 12192000, 6858000

	if _, has := issueSlide(nil, width, height); has {
		t.Error("an empty report produced an issue page")
	}
	if _, has := issueSlide([]reportItem{{Title: "이슈 없는 업무", CurrentResult: "완료"}}, width, height); has {
		t.Error("a report whose items have no issues produced an issue page")
	}
	if _, has := issueSlide([]reportItem{{Title: "업무", Issue: "   "}}, width, height); has {
		t.Error("whitespace counted as an issue")
	}

	slide, has := issueSlide([]reportItem{
		{Category: "인프라", Title: "회선 이설", Issue: "임대 일정 지연"},
		{Category: "개발", Title: "적재 배치", Issue: "원천 스키마 변경"},
	}, width, height)
	if !has {
		t.Fatal("two issues produced no page")
	}
	for _, phrase := range []string{"이슈 및 애로사항", "회선 이설", "임대 일정 지연", "적재 배치", "원천 스키마 변경"} {
		if !strings.Contains(slide.Shapes, phrase) {
			t.Errorf("the page does not carry %q", phrase)
		}
	}

	// More issues than the page holds: what is missing has to be stated, not
	// dropped over the bottom edge.
	many := make([]reportItem, 0, 30)
	for index := 0; index < 30; index++ {
		many = append(many, reportItem{Category: "운영", Title: fmt.Sprintf("업무 %d", index+1),
			Issue: fmt.Sprintf("이슈 %d 상세", index+1)})
	}
	crowded, has := issueSlide(many, width, height)
	if !has {
		t.Fatal("thirty issues produced no page")
	}
	if !strings.Contains(crowded.Shapes, "담지 못한") {
		t.Errorf("a page that could not fit every issue says nothing about the rest")
	}
}

// guards: renderReferencePPTX
//
// The page count follows the work, and the file stays openable while it does.
// Removing a slide from the zip without removing it from the content types, the
// slide order and the presentation's relationships produces a deck that
// downloads and then will not open — which is discovered in the room.
func TestTheDeckGrowsWithTheWorkAndStaysOpenable(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("deck_pages", "USER", nil)

	for _, item := range []struct {
		name  string
		count int
	}{{"한 건", 1}, {"여덟 건", 8}, {"스무 건", 20}} {
		id, version := server.draft(author, weekFor(item.count), "쪽수 시험")
		rows := make([]map[string]any, 0, item.count)
		for index := 0; index < item.count; index++ {
			rows = append(rows, map[string]any{
				"category": "인프라", "title": fmt.Sprintf("업무 %d", index+1),
				"currentResult": strings.Repeat("실적 문장. ", 6), "nextPlan": strings.Repeat("계획 문장. ", 6),
				"progress": 50,
			})
		}
		filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id),
			map[string]any{"summary": "쪽수 시험", "version": version, "items": rows}, author)
		if filled.Code != http.StatusOK {
			t.Fatalf("%s: fill: %d %s", item.name, filled.Code, filled.Body.String())
		}
		export := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
		if export.Code != http.StatusOK {
			t.Fatalf("%s: export: %d %s", item.name, export.Code, export.Body.String())
		}
		text, names := deckText(t, export.Body.Bytes())
		slides, overrides := 0, 0
		for _, name := range names {
			if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") && !strings.Contains(name, "_rels") {
				slides++
			}
		}
		if slides < 1 {
			t.Fatalf("%s: no slides", item.name)
		}
		// Every slide the deck lists has to exist, and every slide that exists
		// has to be listed. Either direction failing is a file that will not open.
		presentation := partOf(t, export.Body.Bytes(), "ppt/presentation.xml")
		if listed := strings.Count(presentation, "<p:sldId "); listed != slides {
			t.Errorf("%s: the deck holds %d slides but its order names %d", item.name, slides, listed)
		}
		contentTypes := partOf(t, export.Body.Bytes(), "[Content_Types].xml")
		overrides = strings.Count(contentTypes, `PartName="/ppt/slides/slide`)
		if overrides != slides {
			t.Errorf("%s: %d slides but %d content-type entries", item.name, slides, overrides)
		}
		rels := partOf(t, export.Body.Bytes(), "ppt/_rels/presentation.xml.rels")
		if related := strings.Count(rels, `Target="slides/slide`); related != slides {
			t.Errorf("%s: %d slides but %d relationships", item.name, slides, related)
		}
		// The last task has to be somewhere in the file, or pages were dropped
		// with work on them.
		last := fmt.Sprintf("업무 %d", item.count)
		if !strings.Contains(text, last) {
			t.Errorf("%s: %q is not in the deck — a page carrying work was dropped", item.name, last)
		}
	}
}

// partOf returns one named part of a PPTX.
func partOf(t *testing.T, deck []byte, name string) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(deck), int64(len(deck)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer opened.Close()
		content, err := io.ReadAll(opened)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	t.Fatalf("the deck has no %s", name)
	return ""
}

// weekFor keeps each report in its own week, since one person has one report a week.
func weekFor(count int) string {
	return map[int]string{1: "2026-08-24", 8: "2026-08-17", 20: "2026-08-10"}[count]
}

// guards: exportReportPPTX
func TestNobodyExportsSomebodyElsesDeck(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("deck_owner", "USER", nil)
	stranger := server.createUser("deck_stranger", "USER", nil)
	id := server.submitted(author, "2026-08-24", "남의 보고서")

	own := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
	if own.Code != http.StatusOK {
		t.Fatalf("the author cannot export their own report: %d %s", own.Code, own.Body.String())
	}
	other := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, stranger)
	if other.Code == http.StatusOK {
		t.Errorf("a stranger downloaded somebody else's deck (%d bytes)", other.Body.Len())
	}
}

// visibleText strips XML tags so a failure shows what a reader would see.
func visibleText(xml string) string {
	var out strings.Builder
	depth := 0
	for _, r := range xml {
		switch {
		case r == '<':
			depth++
		case r == '>':
			depth--
			out.WriteRune(' ')
		case depth == 0:
			out.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

// guards: issueSlide, askRuns
//
// Not =100: the last uncovered branch in each is the fallback for an item with
// no title at all, which the editor does not allow. Writing a case for it would
// be a test about the function rather than about the rule.
//
// ManagementAsk is "what the reporting line must decide or supply" — the most
// management-facing field the product has. It reached the CSV, the meeting
// agenda and the handover note, and neither deck. The decks are what management
// reads.
func TestTheAskReachesTheDeckAndStaysApartFromTheIssue(t *testing.T) {
	width, height := 12192000, 6858000

	onlyIssue, has := issueSlide([]reportItem{{Title: "회선 이설", Issue: "임대 일정 지연"}}, width, height)
	if !has {
		t.Fatal("an issue alone produced no page")
	}
	if !strings.Contains(onlyIssue.Shapes, "이슈 및 애로사항") {
		t.Errorf("a page with only issues is not titled for them")
	}
	if strings.Contains(onlyIssue.Shapes, "상위 조직 요청") {
		t.Errorf("a page with no asks carries the ask heading anyway")
	}

	onlyAsk, has := issueSlide([]reportItem{{Title: "예산 배정", ManagementAsk: "3분기 예산 승인 필요"}}, width, height)
	if !has {
		t.Fatal("an ask alone produced no page")
	}
	if !strings.Contains(onlyAsk.Shapes, "3분기 예산 승인 필요") {
		t.Errorf("the ask text is not on the page")
	}
	if !strings.Contains(onlyAsk.Shapes, "상위 조직 요청") {
		t.Errorf("a page with only asks is not titled for them")
	}

	both, has := issueSlide([]reportItem{
		{Category: "인프라", Title: "회선 이설", Issue: "임대 일정 지연", ManagementAsk: "사업부 예산 승인"},
	}, width, height)
	if !has {
		t.Fatal("an item with both produced no page")
	}
	// The distinction the editor asks the author to make has to survive the
	// export: merging the two would lose exactly what makes them different.
	for _, phrase := range []string{"이슈 및 요청 사항", "이슈 · 애로사항", "임대 일정 지연", "상위 조직 요청 · 결정 필요", "사업부 예산 승인"} {
		if !strings.Contains(both.Shapes, phrase) {
			t.Errorf("the page does not carry %q", phrase)
		}
	}

	// More asks than the block holds. What is missing is stated rather than
	// falling off the bottom of the page.
	many := make([]reportItem, 0, 20)
	for index := 0; index < 20; index++ {
		many = append(many, reportItem{Category: "운영", Title: fmt.Sprintf("업무 %d", index+1),
			ManagementAsk: fmt.Sprintf("요청 %d 상세 내용", index+1)})
	}
	crowded, has := issueSlide(many, width, height)
	if !has {
		t.Fatal("twenty asks produced no page")
	}
	if !strings.Contains(crowded.Shapes, "담지 못한 요청") {
		t.Error("a block that could not fit every ask says nothing about the rest")
	}
}

// guards: askSlide=100
//
// The period deck's own assembly comment says it should end on the ask. Until
// now the ask it ended on was a risk list this product inferred, not the
// sentence somebody wrote.
func TestThePeriodDeckCarriesWhatTheAuthorsAskedFor(t *testing.T) {
	if askSlide(rollupView{Items: []rollupItem{{Title: "요청 없는 업무"}}}) != nil {
		t.Error("a period with no asks produced an ask slide")
	}

	slide := askSlide(rollupView{
		Label: "2026-08", ScopeLabel: "팀",
		Items: []rollupItem{
			{Title: "예산 배정", Owners: []string{"김담당", "이담당"}, ManagementAsk: "3분기 예산 승인 필요"},
			{Title: "회선 이설", Owners: []string{"박담당"}, ManagementAsk: "통신사 계약 변경 승인"},
		},
	})
	if slide == nil {
		t.Fatal("two asks produced no slide")
	}
	for _, phrase := range []string{"상위 조직 요청 · 결정 필요", "3분기 예산 승인 필요", "통신사 계약 변경 승인", "김담당 외 1명", "박담당"} {
		if !strings.Contains(slide.Shapes, phrase) {
			t.Errorf("the slide does not carry %q", phrase)
		}
	}

	// More asks than the table holds: the count of what is missing is stated.
	many := make([]rollupItem, 0, 20)
	for index := 0; index < 20; index++ {
		many = append(many, rollupItem{Title: fmt.Sprintf("업무 %d", index+1),
			ManagementAsk: fmt.Sprintf("요청 %d", index+1)})
	}
	crowded := askSlide(rollupView{Items: many})
	if crowded == nil {
		t.Fatal("twenty asks produced no slide")
	}
	if !strings.Contains(crowded.Shapes, "담지 못한 요청") {
		t.Error("a table that could not fit every ask says nothing about the rest")
	}
}
