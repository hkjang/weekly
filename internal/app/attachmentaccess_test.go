package app

import (
	"encoding/json"
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

// oneCapture attaches a single image and returns its identifier.
func oneCapture(t *testing.T, server *testServer, cookie *http.Cookie, reportID int64) int64 {
	t.Helper()
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)
	w := server.upload(path, "files", "capture.png", samplePNG(t, 4, 4), cookie)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("attach an image: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || len(envelope.Data) == 0 {
		t.Fatalf("the upload returned no image: %s", w.Body.String())
	}
	return envelope.Data[0].ID
}

// A single capture is reachable by its own address, and each verb on it had its
// own refusal. All three could be deleted without a test noticing — reading
// somebody else's screenshot, renaming it, and destroying it.

// guards: serveAttachment
func TestNobodyOpensSomebodyElsesCapture(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_owner", "USER", nil)
	stranger := server.createUser("capture_stranger", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "캡처가 붙은 보고서")
	imageID := oneCapture(t, server, author, reportID)
	path := fmt.Sprintf("/api/v1/reports/%d/attachments/%d", reportID, imageID)

	own := server.request(http.MethodGet, path, nil, author)
	if own.Code != http.StatusOK {
		t.Fatalf("the author cannot open their own capture: %d %s", own.Code, own.Body.String())
	}
	if own.Body.Len() == 0 {
		t.Fatal("the author's capture came back empty")
	}

	other := server.request(http.MethodGet, path, nil, stranger)
	if other.Code == http.StatusOK {
		t.Errorf("a stranger opened somebody else's capture (%d bytes)", other.Body.Len())
	}
	if other.Code != http.StatusForbidden && other.Code != http.StatusNotFound {
		t.Errorf("opening somebody else's capture answered %d, want 403 or 404", other.Code)
	}
}

// guards: updateAttachment
func TestNobodyRenamesSomebodyElsesCapture(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_owner", "USER", nil)
	stranger := server.createUser("capture_stranger", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "캡처가 붙은 보고서")
	imageID := oneCapture(t, server, author, reportID)
	path := fmt.Sprintf("/api/v1/reports/%d/attachments/%d", reportID, imageID)

	if own := server.request(http.MethodPatch, path, map[string]any{"caption": "내 캡처"}, author); own.Code != http.StatusOK {
		t.Fatalf("the author cannot caption their own capture: %d %s", own.Code, own.Body.String())
	}

	other := server.request(http.MethodPatch, path, map[string]any{"caption": "남이 붙인 설명"}, stranger)
	if other.Code == http.StatusOK {
		t.Errorf("a stranger captioned somebody else's capture: %s", other.Body.String())
	}
	if other.Code != http.StatusForbidden && other.Code != http.StatusNotFound {
		t.Errorf("captioning somebody else's capture answered %d, want 403 or 404", other.Code)
	}
	// Refusing and writing anyway is still writing.
	listed := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/attachments", reportID), nil, author)
	if strings.Contains(listed.Body.String(), "남이 붙인 설명") {
		t.Errorf("the refusal changed the caption anyway: %s", listed.Body.String())
	}
}

// guards: deleteAttachment
func TestNobodyDeletesSomebodyElsesCapture(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_owner", "USER", nil)
	stranger := server.createUser("capture_stranger", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "캡처가 붙은 보고서")
	imageID := oneCapture(t, server, author, reportID)
	path := fmt.Sprintf("/api/v1/reports/%d/attachments/%d", reportID, imageID)

	other := server.request(http.MethodDelete, path, nil, stranger)
	if other.Code == http.StatusOK || other.Code == http.StatusNoContent {
		t.Errorf("a stranger deleted somebody else's capture: %s", other.Body.String())
	}
	if other.Code != http.StatusForbidden && other.Code != http.StatusNotFound {
		t.Errorf("deleting somebody else's capture answered %d, want 403 or 404", other.Code)
	}
	// The refusal has to leave the image where it was: a 403 that deleted it
	// anyway would read as a boundary held while the work was already gone.
	if still := server.request(http.MethodGet, path, nil, author); still.Code != http.StatusOK {
		t.Errorf("the author's capture is gone after a refused delete: %d", still.Code)
	}

	// And the author can still remove their own.
	if own := server.request(http.MethodDelete, path, nil, author); own.Code != http.StatusOK && own.Code != http.StatusNoContent {
		t.Fatalf("the author cannot delete their own capture: %d %s", own.Code, own.Body.String())
	}
}
