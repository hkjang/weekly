package app

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// "복구와 취소 선택이 뜨고, 복구를 누르면 읽을 수 없는 내용을 제거했다."
//
// That is what opening a downloaded weekly report did, on PowerPoint 2016 and
// later. Nothing in the suite saw it, because nothing in the suite exported on
// the template production uses: the build embeds a PowerPoint-saved reference
// deck that is not in the repository, and every test ran on the four-slide
// stand-in generated in code — which has no notes slides, stores each slide
// before its relationships, and so could not show any of what went wrong:
//
//   - a dropped page's ppt/slides/_rels/slideN.xml.rels was kept, because
//     PowerPoint stores it before slideN.xml and the drop was decided only
//     when slideN.xml went past
//   - the issue page or a capture then became slideN.xml and its .rels was
//     written a second time — two zip entries with one name
//   - the dropped page's notes slide stayed, pointing at a slide that was gone
//
// and on every deck generated in code (the period report, the built-in
// template) three schema faults PowerPoint also repairs: a slide layout id of
// 1 where the minimum is 2147483648, a theme whose style lists have one entry
// where three are required, and view properties missing required children.
//
// powerPointLikeTemplate below has the production template's shape, so the
// paths are exercised on what customers actually download.

// powerPointLikeTemplate is the generated four-slide reference rebuilt the way
// PowerPoint saves it: notes slides on pages 1, 3 and 4 with a notes master,
// each slide's relationships stored before the slide, a thumbnail, and —
// optionally — the sections PowerPoint 2010 and later can add.
func powerPointLikeTemplate(t *testing.T, sections bool) []byte {
	t.Helper()
	generated, err := referenceStylePPTX()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(generated), int64(len(generated)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(stream)
		stream.Close()
		parts[file.Name] = string(data)
	}
	const (
		pml       = `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"`
		relsOpen  = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
		relType   = `http://schemas.openxmlformats.org/officeDocument/2006/relationships/`
		emptyTree = `<p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/></p:spTree>`
	)
	parts["ppt/notesMasters/notesMaster1.xml"] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:notesMaster ` + pml + `><p:cSld>` + emptyTree +
		`</p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/></p:notesMaster>`
	parts["ppt/notesMasters/_rels/notesMaster1.xml.rels"] = relsOpen + `<Relationship Id="rId1" Type="` + relType + `theme" Target="../theme/theme2.xml"/></Relationships>`
	parts["ppt/theme/theme2.xml"] = defaultTheme
	notes := map[int]int{1: 1, 3: 2, 4: 3} // slide → notes slide, as in the production template
	for slide, note := range notes {
		parts[fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", note)] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:notes ` + pml + `><p:cSld>` + emptyTree +
			`</p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:notes>`
		parts[fmt.Sprintf("ppt/notesSlides/_rels/notesSlide%d.xml.rels", note)] = relsOpen +
			`<Relationship Id="rId2" Type="` + relType + fmt.Sprintf(`slide" Target="../slides/slide%d.xml"/>`, slide) +
			`<Relationship Id="rId1" Type="` + relType + `notesMaster" Target="../notesMasters/notesMaster1.xml"/></Relationships>`
		name := fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", slide)
		parts[name] = strings.Replace(parts[name], `</Relationships>`,
			`<Relationship Id="rId2" Type="`+relType+fmt.Sprintf(`notesSlide" Target="../notesSlides/notesSlide%d.xml"/>`, note)+`</Relationships>`, 1)
	}
	parts["ppt/_rels/presentation.xml.rels"] = strings.Replace(parts["ppt/_rels/presentation.xml.rels"], `</Relationships>`,
		`<Relationship Id="rId9" Type="`+relType+`notesMaster" Target="notesMasters/notesMaster1.xml"/></Relationships>`, 1)
	presentation := strings.Replace(parts["ppt/presentation.xml"], `</p:sldMasterIdLst>`,
		`</p:sldMasterIdLst><p:notesMasterIdLst><p:notesMasterId r:id="rId9"/></p:notesMasterIdLst>`, 1)
	if sections {
		presentation = strings.Replace(presentation, `</p:presentation>`,
			`<p:extLst><p:ext uri="{521415D9-36F7-43E2-AB2F-B90AF26B5E84}"><p14:sectionLst xmlns:p14="http://schemas.microsoft.com/office/powerpoint/2010/main">`+
				`<p14:section name="실적" id="{6A1F5C2E-0D7B-4E57-9C55-0B3C1E0F0001}"><p14:sldIdLst><p14:sldId id="256"/><p14:sldId id="257"/></p14:sldIdLst></p14:section>`+
				`<p14:section name="계획" id="{6A1F5C2E-0D7B-4E57-9C55-0B3C1E0F0002}"><p14:sldIdLst><p14:sldId id="258"/><p14:sldId id="259"/></p14:sldIdLst></p14:section>`+
				`</p14:sectionLst></p:ext></p:extLst></p:presentation>`, 1)
	}
	parts["ppt/presentation.xml"] = presentation
	parts["docProps/thumbnail.jpeg"] = "\xff\xd8\xff\xd9"
	parts["_rels/.rels"] = strings.Replace(parts["_rels/.rels"], `</Relationships>`,
		`<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/thumbnail" Target="docProps/thumbnail.jpeg"/></Relationships>`, 1)
	types := strings.Replace(parts["[Content_Types].xml"], `<Default Extension="rels"`, `<Default Extension="jpeg" ContentType="image/jpeg"/><Default Extension="rels"`, 1)
	types = strings.Replace(types, `</Types>`, `<Override PartName="/ppt/notesMasters/notesMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesMaster+xml"/>`+
		`<Override PartName="/ppt/theme/theme2.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`+
		`<Override PartName="/ppt/notesSlides/notesSlide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/>`+
		`<Override PartName="/ppt/notesSlides/notesSlide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/>`+
		`<Override PartName="/ppt/notesSlides/notesSlide3.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/></Types>`, 1)
	parts["[Content_Types].xml"] = types
	parts["docProps/app.xml"] = strings.Replace(parts["docProps/app.xml"], `<Notes>0</Notes>`, `<Notes>3</Notes>`, 1)

	// PowerPoint's own order: relationships ahead of the parts they belong to.
	order := []string{"[Content_Types].xml", "_rels/.rels"}
	for slide := 1; slide <= 4; slide++ {
		order = append(order, fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", slide))
	}
	order = append(order, "ppt/_rels/presentation.xml.rels", "ppt/presentation.xml")
	for slide := 1; slide <= 4; slide++ {
		order = append(order, fmt.Sprintf("ppt/slides/slide%d.xml", slide))
	}
	listed := map[string]bool{}
	for _, name := range order {
		listed[name] = true
	}
	for _, name := range sortedKeys(parts) {
		if !listed[name] {
			order = append(order, name)
		}
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range order {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(2026, 1, 30, 9, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, parts[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	template := output.Bytes()
	// The fixture has to be a deck PowerPoint itself would open, or nothing
	// measured against it means anything.
	if problems := pptxProblems(template); len(problems) > 0 {
		t.Fatalf("the PowerPoint-shaped fixture is itself broken:\n  %s", strings.Join(problems, "\n  "))
	}
	return template
}

// sampleCapture is a capture slide with a one-pixel PNG, built the way
// attachmentSlides builds one.
func sampleCapture(id int64) builtSlide {
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0, 0, 0, 0x0D, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1F, 0x15, 0xC4, 0x89,
		0, 0, 0, 0x0A, 'I', 'D', 'A', 'T', 0x78, 0x9C, 0x63, 0, 1, 0, 0, 5, 0, 1, 0x0D, 0x0A, 0x2D, 0xB4,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82,
	}
	return attachmentSlides([]storedAttachment{{attachmentView: attachmentView{ID: id, Filename: "캡처.png", Width: 1, Height: 1},
		ContentType: "image/png", Extension: "png"}}, 9906000, 6858000,
		func(storedAttachment) (func() ([]byte, error), bool) {
			return func() ([]byte, error) { return png, nil }, true
		})[0]
}

func noRepair(t *testing.T, label string, deck []byte) {
	t.Helper()
	if problems := pptxProblems(deck); len(problems) > 0 {
		t.Errorf("%s — PowerPoint would offer to repair this deck:\n  %s", label, strings.Join(problems, "\n  "))
	}
}

func itemsFor(count int) []reportItem {
	items := make([]reportItem, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, reportItem{
			Category: "인프라", Title: fmt.Sprintf("업무 %d", index+1),
			CurrentResult: strings.Repeat("실적 문장. ", 12), NextPlan: strings.Repeat("계획 문장. ", 12),
			Issue: map[bool]string{true: "일정 지연", false: ""}[index == 0], ManagementAsk: map[bool]string{true: "예산 승인", false: ""}[index == 0],
		})
	}
	return items
}

// guards: renderReferencePPTX, appendSlidesToPPTX, finalizePPTX
func TestAReportDeckOnTheHouseTemplateOpensWithoutRepair(t *testing.T) {
	week := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	templates := map[string][]byte{
		"PowerPoint 모양 템플릿":   powerPointLikeTemplate(t, false),
		"구역(section)이 있는 템플릿": powerPointLikeTemplate(t, true),
	}
	if generated, err := referenceStylePPTX(); err == nil {
		templates["내장 4쪽 템플릿"] = generated
	}
	// The production template, wherever a checkout keeps it.
	for _, candidate := range []string{os.Getenv("WEEKLY_REFERENCE_PPTX"), "../../1월5주간업무보고_AI엔지니어링.pptx", "../../cmd/weekly/templates/reference.pptx"} {
		if candidate == "" {
			continue
		}
		if body, err := os.ReadFile(candidate); err == nil {
			templates["운영 템플릿 "+candidate] = body
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	for name, template := range templates {
		for _, count := range []int{0, 1, 2, 3, 4, 9} {
			items := itemsFor(count)
			deck, err := renderReferencePPTX(template, &reportView{Items: items}, "AI엔지니어링 파트", week)
			if err != nil {
				t.Fatalf("%s, %d건: %v", name, count, err)
			}
			label := fmt.Sprintf("%s, 업무 %d건", name, count)
			noRepair(t, label, deck)

			// Then what reportDeck adds: the issue page, captures on both sides.
			width, height := presentationSlideSize(deck)
			after := []builtSlide{sampleCapture(2)}
			if slide, has := issueSlide(items, width, height); has {
				after = append([]builtSlide{slide}, after...)
			}
			grown, err := appendSlidesToPPTX(deck, []builtSlide{sampleCapture(1)}, after)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			final, err := finalizePPTX(grown)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			noRepair(t, label+" + 이슈·캡처", final)

			// Notes stay with the pages they belong to: the first page keeps
			// its notes, a dropped page's notes leave with it.
			if strings.Contains(name, "모양") {
				parts := deckParts(t, final)
				if _, kept := parts["ppt/notesSlides/notesSlide1.xml"]; !kept {
					t.Errorf("%s: the first page lost its notes", label)
				}
			}
		}
	}
}

// guards: finalizePPTX, reconcileSections
func TestSectionsFollowTheSlidesThatAreLeft(t *testing.T) {
	template := powerPointLikeTemplate(t, true)
	deck, err := renderReferencePPTX(template, &reportView{Items: itemsFor(1)}, "팀", time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	grown, err := appendSlidesToPPTX(deck, []builtSlide{sampleCapture(1)}, []builtSlide{sampleCapture(2)})
	if err != nil {
		t.Fatal(err)
	}
	final, err := finalizePPTX(grown)
	if err != nil {
		t.Fatal(err)
	}
	presentation := deckParts(t, final)["ppt/presentation.xml"]
	sections := regexp.MustCompile(`<p14:section name="([^"]+)"[^>]*><p14:sldIdLst>(.*?)</p14:sldIdLst>`).FindAllStringSubmatch(presentation, -1)
	if len(sections) != 2 {
		t.Fatalf("both sections should survive, even the one whose pages were all dropped: %s", presentation)
	}
	// The capture before the report opens the first section, the one after it
	// joins the section of the page before it.
	if got := strings.Count(sections[0][2], "<p14:sldId "); got != 3 {
		t.Errorf("the first section holds %d slides, want the capture before, the page and the capture after: %s", got, sections[0][2])
	}
	if sections[1][2] != "" {
		t.Errorf("the second section's pages were all dropped, yet it names %s", sections[1][2])
	}
	noRepair(t, "구역", final)
}

// guards: buildRollupDeck, buildPPTX, defaultPPTX
func TestEveryDeckBuiltInCodeIsOneOfficeOpensAsIs(t *testing.T) {
	rollup, err := buildRollupDeck(sampleRollup(t, "MONTH", "2026-07", 30, 4))
	if err != nil {
		t.Fatal(err)
	}
	noRepair(t, "기간 보고", rollup)

	plain, err := defaultPPTX()
	if err != nil {
		t.Fatal(err)
	}
	noRepair(t, "기본 템플릿", plain)
	rendered, err := renderPPTX(plain, map[string]string{
		"{{WEEK_SCHEDULE}}": "2026.08.03 ~ 08.09", "{{THIS_WEEK}}": "• 첫 줄\n  - 둘째 줄", "{{NEXT_WEEK}}": "-",
		"{{ISSUES}}": "-", "{{AUTHOR}}": "홍길동", "{{TEAM}}": "팀",
	})
	if err != nil {
		t.Fatal(err)
	}
	final, err := finalizePPTX(rendered)
	if err != nil {
		t.Fatal(err)
	}
	noRepair(t, "기본 템플릿 렌더링", final)

	// A custom template keeps its own pages and only gains captures.
	custom, err := renderPPTX(powerPointLikeTemplate(t, true), map[string]string{"{{THIS_WEEK}}": "x"})
	if err != nil {
		t.Fatal(err)
	}
	grown, err := appendSlidesToPPTX(custom, []builtSlide{sampleCapture(1)}, []builtSlide{sampleCapture(2)})
	if err != nil {
		t.Fatal(err)
	}
	if final, err = finalizePPTX(grown); err != nil {
		t.Fatal(err)
	}
	noRepair(t, "사용자 템플릿 + 캡처", final)
}

// guards: escapeXML, renderReferencePPTX
//
// Report text is typed, pasted and imported. A control character copied out of
// another program is not allowed anywhere in an XML document, and one of them
// in one task title is enough for PowerPoint to refuse the slide.
func TestTextThatXMLForbidsCannotBreakTheDeck(t *testing.T) {
	hostile := "제목\x00\x01\x08\x0B\x0C\x1F 끝￾\xff\xfe <&> \"'"
	items := []reportItem{{Category: hostile, Title: hostile, CurrentResult: hostile + "\n" + hostile, NextPlan: hostile,
		Issue: hostile, ManagementAsk: hostile}}
	deck, err := renderReferencePPTX(powerPointLikeTemplate(t, false), &reportView{Items: items}, hostile, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	slide, _ := issueSlide(items, 9906000, 6858000)
	grown, err := appendSlidesToPPTX(deck, nil, []builtSlide{slide})
	if err != nil {
		t.Fatal(err)
	}
	final, err := finalizePPTX(grown)
	if err != nil {
		t.Fatal(err)
	}
	noRepair(t, "제어 문자", final)
	if !strings.Contains(pptxXMLText(t, final), "끝") {
		t.Error("the readable part of the text was lost along with the forbidden characters")
	}
}

// The rules have to catch what actually went wrong, or a validator that
// approves everything would pass every test above. Each broken package here is
// one of the faults this product shipped.
func TestTheRulesCatchEveryRepairThisProductCaused(t *testing.T) {
	good := powerPointLikeTemplate(t, true)
	parts := deckParts(t, good)
	rebuild := func(change func(map[string]string), duplicate string) []byte {
		copied := map[string]string{}
		for name, body := range parts {
			copied[name] = body
		}
		change(copied)
		var output bytes.Buffer
		writer := zip.NewWriter(&output)
		names := append([]string{"[Content_Types].xml"}, sortedKeys(copied)...)
		for index, name := range names {
			if index > 0 && name == "[Content_Types].xml" {
				continue
			}
			entry, _ := writer.Create(name)
			_, _ = io.WriteString(entry, copied[name])
			if name == duplicate {
				again, _ := writer.Create(name)
				_, _ = io.WriteString(again, copied[name])
			}
		}
		_ = writer.Close()
		return output.Bytes()
	}
	for _, broken := range []struct {
		name      string
		change    func(map[string]string)
		duplicate string
		want      string
	}{
		{"두 번 쓴 항목", func(map[string]string) {}, "ppt/slides/_rels/slide2.xml.rels", "duplicate entry"},
		{"사라진 쪽을 가리키는 노트", func(p map[string]string) {
			delete(p, "ppt/slides/slide4.xml")
			delete(p, "ppt/slides/_rels/slide4.xml.rels")
		}, "", "does not exist"},
		{"쪽 없이 남은 관계", func(p map[string]string) {
			p["ppt/slides/_rels/slide9.xml.rels"] = p["ppt/slides/_rels/slide2.xml.rels"]
		}, "", "not in the package"},
		{"1번 레이아웃", func(p map[string]string) {
			p["ppt/slideMasters/slideMaster1.xml"] = strings.Replace(p["ppt/slideMasters/slideMaster1.xml"], `id="2147483649"`, `id="1"`, 1)
		}, "", "below 2147483648"},
		{"한 줄짜리 테마", func(p map[string]string) {
			p["ppt/theme/theme1.xml"] = regexp.MustCompile(`(<a:fillStyleLst>)(<a:solidFill>.*?</a:solidFill>)(?:<a:solidFill>.*?</a:solidFill>){2}`).ReplaceAllString(p["ppt/theme/theme1.xml"], "$1$2")
		}, "", "requires 3"},
		{"빈 보기 속성", func(p map[string]string) {
			p["ppt/viewProps.xml"] = regexp.MustCompile(`(?s)<p:normalViewPr>.*?</p:normalViewPr>`).ReplaceAllString(p["ppt/viewProps.xml"], "<p:normalViewPr/>")
		}, "", "restoredLeft"},
		{"없는 쪽을 가리키는 구역", func(p map[string]string) {
			p["ppt/presentation.xml"] = strings.Replace(p["ppt/presentation.xml"], `<p14:sldId id="259"/>`, `<p14:sldId id="259"/><p14:sldId id="300"/>`, 1)
		}, "", "section"},
		{"타입 없는 그림", func(p map[string]string) {
			p["[Content_Types].xml"] = strings.Replace(p["[Content_Types].xml"], `<Default Extension="jpeg" ContentType="image/jpeg"/>`, "", 1)
		}, "", "no content type"},
		{"겹친 도형 번호", func(p map[string]string) {
			p["ppt/slides/slide1.xml"] = strings.Replace(p["ppt/slides/slide1.xml"], `<p:cNvPr id="3"`, `<p:cNvPr id="2"`, 1)
		}, "", "used twice"},
		{"칸 수가 다른 표", func(p map[string]string) {
			p["ppt/slides/slide1.xml"] = strings.Replace(p["ppt/slides/slide1.xml"], `<a:gridCol w="4716525"/><a:gridCol w="4716525"/>`, `<a:gridCol w="4716525"/><a:gridCol w="4716525"/><a:gridCol w="1"/>`, 1)
		}, "", "cells for"},
	} {
		problems := strings.Join(pptxProblems(rebuild(broken.change, broken.duplicate)), "\n")
		if !strings.Contains(problems, broken.want) {
			t.Errorf("%s: the rules did not notice (want %q), they said:\n%s", broken.name, broken.want, problems)
		}
	}
}

func deckParts(t *testing.T, deck []byte) map[string]string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(deck), int64(len(deck)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(stream)
		stream.Close()
		parts[file.Name] = string(data)
	}
	return parts
}

// guards: exportReportPPTX, reportDeck
//
// The same, through the download button, on the shape of template production
// embeds — the integration tests elsewhere export on the generated stand-in,
// which is why none of them ever saw the prompt.
func TestADownloadedReportOpensWithoutRepair(t *testing.T) {
	server := newTestServer(t)
	server.app.defaultPPTX = powerPointLikeTemplate(t, false)
	author := server.createUser("deck_repair", "USER", nil)
	id, version := server.draft(author, "2026-08-31", "복구 없이 열리는 보고서")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "복구 시험", "version": version,
		"items": []map[string]any{{
			"category": "인프라", "title": "회선 이설", "currentResult": "완료", "nextPlan": "검증",
			"issue": "임대 일정 지연", "managementAsk": "예산 승인 필요", "progress": 60,
		}},
	}, author)
	if filled.Code != http.StatusOK {
		t.Fatalf("fill: %d %s", filled.Code, filled.Body.String())
	}
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0, 0, 0, 0x0D, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1F, 0x15, 0xC4, 0x89,
		0, 0, 0, 0x0A, 'I', 'D', 'A', 'T', 0x78, 0x9C, 0x63, 0, 1, 0, 0, 5, 0, 1, 0x0D, 0x0A, 0x2D, 0xB4,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82,
	}
	if upload := server.upload(fmt.Sprintf("/api/v1/reports/%d/attachments", id), "files", "캡처.png", png, author); upload.Code != http.StatusOK && upload.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", upload.Code, upload.Body.String())
	}
	export := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
	if export.Code != http.StatusOK {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	noRepair(t, "내려받은 보고서", export.Body.Bytes())
	parts := deckParts(t, export.Body.Bytes())
	if _, dropped := parts["ppt/notesSlides/notesSlide2.xml"]; dropped {
		t.Error("a dropped page's notes slide is still in the download")
	}
	text := pptxXMLText(t, export.Body.Bytes())
	for _, phrase := range []string{"회선 이설", "임대 일정 지연", "예산 승인 필요"} {
		if !strings.Contains(text, phrase) {
			t.Errorf("%q is missing from the deck", phrase)
		}
	}
}
