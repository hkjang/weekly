package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// uploadSharedCapture posts one image through the production upload route and
// returns its row id. Unlike uploadCaptures it takes the bytes, because the
// point of these tests is two rows carrying the *same* bytes: the stored path is
// reportID/sha256.ext (uploadAttachments), so a writer who attaches the same
// screenshot twice ends up with two rows over one file on the volume.
func (s *testServer) uploadSharedCapture(t *testing.T, path, name string, body []byte, author *http.Cookie) int64 {
	t.Helper()
	response := s.uploadMany(path, "files", map[string][]byte{name: body}, author)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload %s answered %d: %s", name, response.Code, response.Body.String())
	}
	var envelope struct {
		Data []attachmentView `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the upload response: %v (%s)", err, response.Body.String())
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID == 0 {
		t.Fatalf("upload %s created %d row(s): %s", name, len(envelope.Data), response.Body.String())
	}
	return envelope.Data[0].ID
}

// sharedCaptureFile reports the single file the report's captures are stored in,
// asserting that there is exactly one.
func (s *testServer) sharedCaptureFile(t *testing.T, reportID int64) string {
	t.Helper()
	files := s.attachmentFiles(t, reportID)
	if len(files) != 1 {
		t.Fatalf("two uploads of one image left %v on disk, want a single shared file", files)
	}
	return filepath.Join(stateDirectoryAttachments, strconv.FormatInt(reportID, 10), files[0])
}

// hideAttachmentsOnDelete arms the scratch database so the next DELETE on
// report_attachments succeeds while every statement after it that reads the
// table fails. A BEFORE DELETE trigger renames the table away: the DELETE is
// already planned when the trigger runs, so it returns its row and commits the
// rename with it, and the aggregate the handler runs next — a separate
// statement on the pool — finds no such relation.
//
// That is the shape the field failure takes. The row is gone for good by the
// time the reference count is asked for, so a count that cannot be read is not
// a count of zero; it is a count nobody has. The returned function puts the
// table back and is registered as cleanup too, because leaving a renamed table
// behind would take the rest of this server's tests with it.
func hideAttachmentsOnDelete(t *testing.T, server *testServer) func() {
	t.Helper()
	if _, err := server.app.db.Exec(server.ctx(), `CREATE FUNCTION hide_attachments() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			EXECUTE 'ALTER TABLE report_attachments RENAME TO report_attachments_hidden';
			RETURN OLD;
		END $$`); err != nil {
		t.Fatalf("install the hiding function: %v", err)
	}
	if _, err := server.app.db.Exec(server.ctx(), `CREATE TRIGGER hide_attachments
		BEFORE DELETE ON report_attachments FOR EACH ROW EXECUTE FUNCTION hide_attachments()`); err != nil {
		t.Fatalf("arm the hiding trigger: %v", err)
	}
	restore := func() {
		ctx := context.Background()
		_, _ = server.app.db.Exec(ctx, `ALTER TABLE IF EXISTS report_attachments_hidden RENAME TO report_attachments`)
		_, _ = server.app.db.Exec(ctx, `DROP TRIGGER IF EXISTS hide_attachments ON report_attachments`)
		_, _ = server.app.db.Exec(ctx, `DROP FUNCTION IF EXISTS hide_attachments()`)
	}
	t.Cleanup(restore)
	return restore
}

// Deleting one of two captures that share a file must not touch the file while
// the other row still points at it — and "nobody references these bytes" is not
// the same statement as "the reference count could not be read".
//
// deleteAttachment dropped the Scan error with `_ =`, which left remaining at
// its zero value, and zero is exactly the value that means delete the file. The
// damage is not a wrong number on a screen: os.Remove is not reversible. The
// surviving row keeps pointing at bytes that are gone, its image GET answers
// ATTACHMENT_FILE_MISSING, the list reports available:false, and the capture
// drops out of the exported deck. The delete answers 204 either way and wrote
// no line, so the operator has nothing to read.
//
// guards: deleteAttachment
func TestADeletedCaptureDoesNotTakeAStillReferencedFileWithIt(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_delete_refcount", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "참조 수 조회 실패 시 보존")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)

	image := samplePNG(t, 9, 9)
	first := server.uploadSharedCapture(t, path, "first.png", image, author)
	second := server.uploadSharedCapture(t, path, "second.png", image, author)
	shared := server.sharedCaptureFile(t, reportID)

	restore := hideAttachmentsOnDelete(t, server)
	response := server.request(http.MethodDelete, fmt.Sprintf("%s/%d", path, first), nil, author)
	restore()

	// The row DELETE committed before the count was ever asked for, so the
	// answer stays what it was: the deletion happened.
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete with an unreadable reference count answered %d, want 204: %s",
			response.Code, response.Body.String())
	}
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("the reference count could not be read and the file the surviving row needs is gone: %v", err)
	}
	remaining := server.listCaptures(t, path, author)
	if len(remaining) != 1 || remaining[0].ID != second {
		t.Fatalf("after the delete the report holds %v, want only capture %d", remaining, second)
	}
	if !remaining[0].Available {
		t.Errorf("the surviving capture reports available:false, so the panel shows it as broken: %+v", remaining[0])
	}
	survivor := server.request(http.MethodGet, fmt.Sprintf("%s/%d", path, second), nil, author)
	if survivor.Code != http.StatusOK {
		t.Errorf("the surviving capture's image GET=%d, want 200: %s", survivor.Code, survivor.Body.String())
	} else if !bytes.Equal(survivor.Body.Bytes(), image) {
		t.Errorf("the surviving capture's image GET returned %d bytes, want the %d original bytes",
			survivor.Body.Len(), len(image))
	}

	// Silence is the second half of the defect: an operator who never learns the
	// count failed cannot tell this apart from a normal delete.
	if !server.logged("attachmentId="+strconv.FormatInt(first, 10), "error=") {
		t.Errorf("a reference count the database refused left no line naming the capture:\n%s", server.logs.String())
	}
	// The stored path is derived from the image's own bytes, so it never belongs
	// in a log line or an audit detail — identifiers only.
	if name := filepath.Base(shared); strings.Contains(server.logs.String(), name) {
		t.Errorf("the stored file name reached the log output: %s", name)
	}
}

// The ordinary paths this must keep: a file with another row behind it stays, a
// file whose last row just went stays gone.
//
// guards: deleteAttachment
func TestDeletingACaptureRemovesItsFileOnlyWithTheLastReference(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_delete_lastref", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "마지막 참조에서만 파일 삭제")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)

	image := samplePNG(t, 11, 11)
	first := server.uploadSharedCapture(t, path, "first.png", image, author)
	second := server.uploadSharedCapture(t, path, "second.png", image, author)
	shared := server.sharedCaptureFile(t, reportID)

	if response := server.request(http.MethodDelete, fmt.Sprintf("%s/%d", path, first), nil, author); response.Code != http.StatusNoContent {
		t.Fatalf("deleting one of two captures answered %d: %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("a file one more row still references was removed: %v", err)
	}
	survivor := server.request(http.MethodGet, fmt.Sprintf("%s/%d", path, second), nil, author)
	if survivor.Code != http.StatusOK || !bytes.Equal(survivor.Body.Bytes(), image) {
		t.Errorf("the surviving capture's image GET=%d; want 200 and the original bytes", survivor.Code)
	}

	if response := server.request(http.MethodDelete, fmt.Sprintf("%s/%d", path, second), nil, author); response.Code != http.StatusNoContent {
		t.Fatalf("deleting the last capture answered %d: %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Errorf("the last reference went and the file stayed on the volume: %v", err)
	}
	if listed := server.listCaptures(t, path, author); len(listed) != 0 {
		t.Errorf("the report still lists %v captures", listed)
	}
}

// A row can outlive its file — a deployment that upgrades without mounting the
// state volume keeps every row and loses every file. Deleting such a row is the
// writer tidying up, not an error to report.
//
// guards: deleteAttachment
func TestDeletingACaptureWhoseFileIsAlreadyGoneStillSucceeds(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_delete_nofile", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "파일이 이미 없는 첨부 삭제")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)

	only := server.uploadSharedCapture(t, path, "only.png", samplePNG(t, 13, 13), author)
	shared := server.sharedCaptureFile(t, reportID)
	if err := os.Remove(shared); err != nil {
		t.Fatalf("take the file away from under the row: %v", err)
	}

	response := server.request(http.MethodDelete, fmt.Sprintf("%s/%d", path, only), nil, author)
	if response.Code != http.StatusNoContent {
		t.Fatalf("deleting a capture whose file is gone answered %d: %s", response.Code, response.Body.String())
	}
	if server.logged("remove attachment file") {
		t.Errorf("a file that was already gone was reported as a removal failure:\n%s", server.logs.String())
	}
	if listed := server.listCaptures(t, path, author); len(listed) != 0 {
		t.Errorf("the report still lists %v captures", listed)
	}
}
