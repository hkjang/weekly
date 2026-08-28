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

// The nearest ancestor's template has to be the one the deck comes out of.
//
// TestOrganizationTemplateWalksUpToTheNearestOwner proves templatePathFor picks
// the right row. That is the choosing; this is the consequence — a writer three
// levels down asks for their weekly deck and it arrives on their division's
// paper. Nothing joined those two halves, and the whole feature exists for the
// second one.
//
// guards: exportReportPPTX, uploadPPTXTemplate
func TestAReportComesOutOnItsNearestAncestorsTemplate(t *testing.T) {
	server := newTestServer(t)
	company := server.createOrganization("서식 회사", "TPLCO")
	division := server.createChildOrganization("서식 본부", "TPLDIV", &company)
	office := server.createChildOrganization("서식 실", "TPLOFF", &division)
	team := server.createChildOrganization("서식 팀", "TPLTEAM", &office)
	elsewhere := server.createChildOrganization("서식 옆본부", "TPLOTHER", &company)

	author := server.createUser("cascade_author", "USER", &team)
	stranger := server.createUser("cascade_stranger", "USER", &elsewhere)

	reportOf := func(who *http.Cookie, week, title string) int64 {
		t.Helper()
		id, version := server.draft(who, week, "서식 시험")
		written := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
			"summary": "서식 시험", "version": version,
			"items": []map[string]any{{"category": "개발", "title": title, "currentResult": "진행",
				"nextPlan": "계속", "issue": "", "progress": 30}},
		}, who)
		if written.Code != http.StatusOK {
			t.Fatalf("write the report: %d %s", written.Code, written.Body.String())
		}
		return id
	}
	mine := reportOf(author, "2026-08-24", "계단 업무")
	theirs := reportOf(stranger, "2026-08-24", "옆 조직 업무")

	// Two valid templates that differ only in a mark. defaultPPTX carries three
	// placeholders and upload requires two of them, so the third can hold the
	// mark without making the file invalid.
	marked := func(mark string) []byte {
		t.Helper()
		template, err := defaultPPTX()
		if err != nil {
			t.Fatalf("read the built-in template: %v", err)
		}
		body, err := renderPPTX(template, map[string]string{"{{WEEK_SCHEDULE}}": mark})
		if err != nil {
			t.Fatalf("mark the template: %v", err)
		}
		return body
	}
	upload := func(organization int64, mark string) {
		t.Helper()
		response := server.upload(
			fmt.Sprintf("/api/v1/admin/pptx-template?organizationId=%d", organization),
			"file", "template.pptx", marked(mark), server.admin)
		if response.Code != http.StatusOK {
			t.Fatalf("upload the template for %d: %d %s", organization, response.Code, response.Body.String())
		}
	}
	deckText := func(reportID int64, who *http.Cookie) string {
		t.Helper()
		response := server.request(http.MethodGet,
			fmt.Sprintf("/api/v1/reports/%d/export.pptx", reportID), nil, who)
		if response.Code != http.StatusOK {
			t.Fatalf("export: %d %s", response.Code, response.Body.String())
		}
		body := response.Body.Bytes()
		archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("the deck is not a zip: %v", err)
		}
		text := strings.Builder{}
		for _, file := range archive.File {
			if !strings.HasPrefix(file.Name, "ppt/slides/slide") {
				continue
			}
			stream, openErr := file.Open()
			if openErr != nil {
				continue
			}
			part, _ := io.ReadAll(stream)
			stream.Close()
			text.Write(part)
		}
		return text.String()
	}

	upload(division, "본부-서식")
	if text := deckText(mine, author); !strings.Contains(text, "본부-서식") {
		t.Error("두 단계 위 조직의 서식으로 나오지 않았습니다")
	}

	// A nearer one wins. This is the ordering the recursive walk exists for.
	upload(office, "실-서식")
	text := deckText(mine, author)
	if !strings.Contains(text, "실-서식") {
		t.Error("더 가까운 조직의 서식이 쓰이지 않았습니다")
	}
	if strings.Contains(text, "본부-서식") {
		t.Error("더 먼 조직의 서식이 함께 들어갔습니다")
	}

	// And it does not reach sideways.
	if other := deckText(theirs, stranger); strings.Contains(other, "실-서식") || strings.Contains(other, "본부-서식") {
		t.Error("다른 갈래의 조직이 남의 서식으로 내보냈습니다")
	}

	// Removing the nearer one falls back rather than falling to the house
	// default: the division still has one, and that is what "상위 조직 서식을
	// 따르게 하시겠습니까" promises when an administrator deletes.
	removed := server.request(http.MethodDelete,
		fmt.Sprintf("/api/v1/admin/pptx-template?organizationId=%d", office), nil, server.admin)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove the nearer template: %d %s", removed.Code, removed.Body.String())
	}
	if text := deckText(mine, author); !strings.Contains(text, "본부-서식") {
		t.Error("가까운 서식을 지운 뒤 상위 조직 서식으로 돌아가지 않았습니다")
	}

	// The writer's own report still has the writer's own work in it — a
	// template that swallowed the content would pass every check above.
	if text := deckText(mine, author); !strings.Contains(text, "계단 업무") {
		t.Error("서식은 맞는데 보고서 내용이 없습니다")
	}
}
