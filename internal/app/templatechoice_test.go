package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Which template an export is rendered from is decided by three conditions in a
// row, and none of them was reachable: no test and no seed ever put a PPTX file
// on disk, so pptx_templates was empty everywhere and customPPTXPath pointed at
// something that was not there. A mutation run turned all three into || and
// nothing failed. Each one silently swaps the file a report is drawn on, and
// the result of that is what goes into a meeting.

// markedDeck writes a word into the first text run of a package's first slide,
// so a test can tell which file an export came from.
func markedDeck(t *testing.T, body []byte, mark string) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	marked := false
	for _, file := range reader.File {
		opened, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(opened)
		opened.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !marked && file.Name == "ppt/slides/slide1.xml" {
			// The whole run, not an addition to it. replacePPTXToken matches
			// <a:t>{{TOKEN}}</a:t> exactly — a placeholder sharing a run with
			// other text is not one, which is the contract a template author
			// works to and the reason an earlier version of this helper made
			// the substitution test fail against correct code.
			text := string(data)
			if start := strings.Index(text, "<a:t>"); start >= 0 {
				if end := strings.Index(text[start:], "</a:t>"); end >= 0 {
					data = []byte(text[:start] + "<a:t>" + mark + text[start+end:])
					marked = true
				}
			}
		}
		entry, err := writer.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !marked {
		t.Fatal("the template has no text run to mark, so this test cannot tell the files apart")
	}
	return out.Bytes()
}

// templateOnDisk writes a marked deck where the export will look for it.
func templateOnDisk(t *testing.T, path, mark string) {
	t.Helper()
	deck, err := referenceStylePPTX()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, markedDeck(t, deck, mark), 0o600); err != nil {
		t.Fatal(err)
	}
}

// recordTemplate says an organisation has one. NULL organisation is the house
// default, which is what the column meant before organisations had their own.
func recordTemplate(t *testing.T, server *testServer, organizationID *int64, uploader string) {
	t.Helper()
	if _, err := server.app.db.Exec(server.ctx(),
		`INSERT INTO pptx_templates(original_name, file_name, size_bytes, sha256, uploaded_by, organization_id)
		 VALUES($1,$2,$3,$4,(SELECT id FROM users WHERE username=$5),$6)`,
		"서식.pptx", "서식.pptx", 1024, strings.Repeat("a", 64), uploader, organizationID); err != nil {
		t.Fatalf("record the template: %v", err)
	}
}

// guards: exportReportPPTX, templateForOrganization
func TestAnExportIsDrawnOnTheOrganisationsOwnTemplate(t *testing.T) {
	server := newTestServer(t)
	directory := t.TempDir()
	original := customPPTXPath
	customPPTXPath = directory + "/weekly-report.pptx"
	t.Cleanup(func() { customPPTXPath = original })

	organisation := server.createOrganization("서식 조직", "TMPLORG")
	author := server.createUser("tmpl_author", "USER", &organisation)
	uploader := server.lastCreatedUsername("tmpl_author")

	templateOnDisk(t, customPPTXPath, "사내공용서식")
	templateOnDisk(t, organizationPPTXPath(organisation), "조직전용서식")
	recordTemplate(t, server, &organisation, uploader)

	id := server.submitted(author, "2026-08-24", "서식 시험")
	export := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
	if export.Code != http.StatusOK {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	text, _ := deckText(t, export.Body.Bytes())
	if !strings.Contains(text, "조직전용서식") {
		t.Error("the export was not drawn on the organisation's template")
	}
	if strings.Contains(text, "사내공용서식") {
		t.Error("the export used the house template instead of the organisation's — everyone gets somebody else's cover page")
	}
}

// The row says an organisation has a template and the file is gone. Falling
// back beats refusing: the writer finds out from a report that looks ordinary,
// not from a button that does nothing.

// guards: exportReportPPTX
func TestAMissingOrganisationTemplateFallsBackInsteadOfFailing(t *testing.T) {
	server := newTestServer(t)
	directory := t.TempDir()
	original := customPPTXPath
	customPPTXPath = directory + "/weekly-report.pptx"
	t.Cleanup(func() { customPPTXPath = original })

	organisation := server.createOrganization("사라진 서식", "TMPLGONE")
	author := server.createUser("tmpl_gone", "USER", &organisation)
	templateOnDisk(t, customPPTXPath, "사내공용서식")
	recordTemplate(t, server, &organisation, server.lastCreatedUsername("tmpl_gone"))
	// No file at organizationPPTXPath — that is the case being tested.

	id := server.submitted(author, "2026-08-24", "사라진 서식")
	export := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
	if export.Code != http.StatusOK {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	if text, _ := deckText(t, export.Body.Bytes()); !strings.Contains(text, "사내공용서식") {
		t.Error("a missing organisation file did not fall back to the house template")
	}
}

// A custom house template is rendered by substituting its placeholders, and the
// built-in one by drawing the reference layout. Choosing the wrong renderer for
// a template leaves an operator's {{AUTHOR}} sitting on the page — or throws
// their whole format away — and both files are decks, so nothing downstream
// notices.
//
// The two tests above cannot see this: their fixtures are copies of the
// reference deck, which both renderers turn into something that still carries
// the mark. Only a placeholder tells them apart.

// guards: exportReportPPTX
func TestACustomTemplateHasItsPlaceholdersFilledIn(t *testing.T) {
	server := newTestServer(t)
	directory := t.TempDir()
	original := customPPTXPath
	customPPTXPath = directory + "/weekly-report.pptx"
	t.Cleanup(func() { customPPTXPath = original })

	author := server.createUser("tmpl_ph", "USER", nil)
	// A house template that asks for the author's name where the operator put
	// the placeholder.
	templateOnDisk(t, customPPTXPath, "{{AUTHOR}}")

	id := server.submitted(author, "2026-08-24", "치환 시험")
	export := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
	if export.Code != http.StatusOK {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	text, _ := deckText(t, export.Body.Bytes())

	var displayName string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT display_name FROM users WHERE username=$1`, server.lastCreatedUsername("tmpl_ph")).Scan(&displayName); err != nil {
		t.Fatalf("read the author: %v", err)
	}
	if strings.Contains(text, "{{AUTHOR}}") {
		t.Error("the placeholder reached the reader — the custom template was drawn by the wrong renderer")
	}
	if !strings.Contains(text, displayName) {
		t.Errorf("the author's name never replaced the placeholder:\n%s", text[:min(len(text), 300)])
	}
}
