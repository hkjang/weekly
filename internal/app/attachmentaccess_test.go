package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Captures are part of a report, so they follow the report's boundary: only its
// author adds them, and only somebody allowed to read the report sees them.
//
// Both refusals existed and neither was tested. Removing either from the handler
// left the whole suite green, which is how a rule stops being a rule without
// anybody deciding to drop it.

// guards: listAttachments
func TestNobodyListsSomebodyElsesCaptures(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_owner", "USER", nil)
	stranger := server.createUser("capture_stranger", "USER", nil)
	id, _ := server.draft(author, "2026-08-24", "캡처가 붙은 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", id)

	if own := server.request(http.MethodGet, path, nil, author); own.Code != http.StatusOK {
		t.Fatalf("the author cannot list their own captures: %d %s", own.Code, own.Body.String())
	}

	other := server.request(http.MethodGet, path, nil, stranger)
	if other.Code == http.StatusOK {
		t.Errorf("a stranger listed somebody else's captures: %s", other.Body.String())
	}
	// A refusal, not a failure: 500 is also "not 200", and reading it as a
	// boundary held is how a broken endpoint passes for a working one.
	if other.Code != http.StatusForbidden && other.Code != http.StatusNotFound {
		t.Errorf("listing somebody else's captures answered %d, want 403 or 404", other.Code)
	}
}

// guards: uploadAttachments
func TestNobodyAddsCapturesToSomebodyElsesReport(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_owner", "USER", nil)
	stranger := server.createUser("capture_stranger", "USER", nil)
	id, _ := server.draft(author, "2026-08-24", "캡처가 붙은 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", id)
	image := samplePNG(t, 4, 4)

	if own := server.upload(path, "files", "mine.png", image, author); own.Code != http.StatusCreated && own.Code != http.StatusOK {
		t.Fatalf("the author cannot attach to their own report: %d %s", own.Code, own.Body.String())
	}

	other := server.upload(path, "files", "theirs.png", image, stranger)
	if other.Code == http.StatusOK {
		t.Errorf("a stranger attached an image to somebody else's report: %s", other.Body.String())
	}
	if other.Code != http.StatusForbidden && other.Code != http.StatusNotFound {
		t.Errorf("attaching to somebody else's report answered %d, want 403 or 404", other.Code)
	}

	// And the refusal has to be a refusal, not a silent no-op that still stored
	// the file: the author's list must hold exactly what the author uploaded.
	listed := server.request(http.MethodGet, path, nil, author)
	if listed.Code != http.StatusOK {
		t.Fatalf("list the author's captures: %d %s", listed.Code, listed.Body.String())
	}
	if got := listed.Body.String(); !strings.Contains(got, "mine.png") || strings.Contains(got, "theirs.png") {
		t.Errorf("the author's captures should hold mine.png and not theirs.png: %s", got)
	}
}
