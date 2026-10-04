package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The first upload has already written its image when its INSERT waits for a
// lock. No attachment row is visible yet, but the report still owns the file.
// guards: cleanupAttachmentFiles
func TestAttachmentCleanupPreservesTheFirstUploadWhileItsInsertWaits(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_cleanup", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "정리 중 첫 첨부")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)
	payload := samplePNG(t, 4, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := server.app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// SHARE blocks INSERT but lets the sweep read both attachments and reports.
	// newTestServer gives this test its own scratch database.
	if _, err := tx.Exec(ctx, `LOCK TABLE report_attachments IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	var response *httptest.ResponseRecorder
	go func() {
		defer close(finished)
		response = server.uploadMany(path, "files", map[string][]byte{"capture.png": payload}, author)
	}()
	defer func() {
		// Release the lock before joining the handler, even when an assertion
		// fails. It must finish before the harness closes the DB/state directory.
		_ = tx.Rollback(context.Background())
		<-finished
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := server.app.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock'
			AND query LIKE 'INSERT INTO report_attachments%')`).Scan(&waiting)
		if err != nil {
			t.Fatalf("observe the upload's INSERT lock wait: %v", err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("upload never reached the INSERT lock wait: %v", ctx.Err())
		case <-finished:
			t.Fatalf("upload finished before waiting on INSERT: %v", response)
		case <-ticker.C:
		}
	}
	if count := server.attachmentCount(reportID); count != 0 {
		t.Fatalf("before cleanup: rows=%d, want 0 uncommitted attachments", count)
	}
	if files := server.attachmentFiles(t, reportID); len(files) != 1 {
		t.Fatalf("before cleanup: files=%v, want one written image", files)
	}
	server.app.cleanupAttachmentFiles(ctx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatalf("upload did not finish after releasing the lock: %v", ctx.Err())
	}
	if response == nil || response.Code != http.StatusCreated {
		t.Fatalf("upload after cleanup = %v, want 201", response)
	}
	server.assertAttachmentReadable(t, reportID, author, payload)
}

// guards: cleanupAttachmentFiles
func TestAttachmentCleanupRemovesOnlyDeletedReports(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_deleted", "USER", nil)
	deletedID, _ := server.draft(author, "2026-08-24", "삭제할 보고서")
	liveID, _ := server.draft(author, "2026-08-31", "보존할 보고서")
	oneCapture(t, server, author, deletedID)
	oneCapture(t, server, author, liveID)
	server.deleteReportForAttachmentCleanup(t, deletedID, author)
	if count := server.attachmentCount(deletedID); count != 0 {
		t.Fatalf("report deletion left %d attachment rows, want cascade deletion", count)
	}
	if files := server.attachmentFiles(t, deletedID); len(files) != 1 {
		t.Fatalf("before cleanup: deleted report files=%v, want one image", files)
	}

	server.app.cleanupAttachmentFiles(server.ctx())
	directory := filepath.Join(stateDirectoryAttachments, strconv.FormatInt(deletedID, 10))
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Errorf("deleted report directory remains after cleanup: %v", err)
	}
	if count := server.attachmentCount(liveID); count != 1 {
		t.Errorf("live report has %d attachment rows after cleanup, want 1", count)
	}
	if files := server.attachmentFiles(t, liveID); len(files) != 1 {
		t.Errorf("live report files after cleanup=%v, want one image", files)
	}
	server.assertAttachmentReadable(t, liveID, author, samplePNG(t, 4, 4))
}

// A cancelled query cannot prove that the parent is gone. Even files belonging
// to a deleted report must wait until a sweep can confirm that absence.
// guards: cleanupAttachmentFiles
func TestAttachmentCleanupPreservesFilesWhenTheParentQueryFails(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_cancelled", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "조회 실패 시 파일 보존")
	oneCapture(t, server, author, reportID)
	server.deleteReportForAttachmentCleanup(t, reportID, author)
	files := server.attachmentFiles(t, reportID)
	if len(files) != 1 {
		t.Fatalf("before cancelled cleanup: files=%v, want one uploaded image", files)
	}
	directory := filepath.Join(stateDirectoryAttachments, strconv.FormatInt(reportID, 10))
	ctx, cancel := context.WithCancel(server.ctx())
	cancel()
	server.app.cleanupAttachmentFiles(ctx)
	body, err := os.ReadFile(filepath.Join(directory, files[0]))
	if err != nil {
		t.Fatalf("cancelled cleanup lost the uploaded image: %v", err)
	}
	if !bytes.Equal(body, samplePNG(t, 4, 4)) {
		t.Error("cancelled cleanup changed the uploaded image bytes")
	}
	server.app.cleanupAttachmentFiles(server.ctx())
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Errorf("a successful parent query did not allow cleanup: %v", err)
	}
}

// Deleting the last attachment leaves an empty directory for a live report.
// Neither zero rows nor zero files proves that the report was deleted.
// guards: cleanupAttachmentFiles
func TestAttachmentCleanupKeepsAnEmptyDirectoryForALiveReport(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_empty", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "빈 첨부 디렉터리")
	attachmentID := oneCapture(t, server, author, reportID)
	deleted := server.request(http.MethodDelete,
		fmt.Sprintf("/api/v1/reports/%d/attachments/%d", reportID, attachmentID), nil, author)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete the last attachment = %d: %s", deleted.Code, deleted.Body.String())
	}
	if count := server.attachmentCount(reportID); count != 0 {
		t.Fatalf("after deleting the last attachment: rows=%d, want 0", count)
	}
	directory := filepath.Join(stateDirectoryAttachments, strconv.FormatInt(reportID, 10))
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("before cleanup: directory entries=%v, error=%v, want an empty directory", entries, err)
	}
	server.app.cleanupAttachmentFiles(server.ctx())
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("cleanup removed a live report's empty directory: %v", err)
	}
}

func (s *testServer) deleteReportForAttachmentCleanup(t *testing.T, reportID int64, author *http.Cookie) {
	t.Helper()
	path := fmt.Sprintf("/api/v1/reports/%d", reportID)
	current := s.request(http.MethodGet, path, nil, author)
	if current.Code != http.StatusOK {
		t.Fatalf("read the report's current version = %d: %s", current.Code, current.Body.String())
	}
	version, ok := decodeData(t, current)["version"].(float64)
	if !ok || version < 1 {
		t.Fatalf("report has no current version: %s", current.Body.String())
	}
	deleted := s.request(http.MethodDelete, fmt.Sprintf("%s?version=%.0f", path, version), nil, author)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete the report = %d: %s", deleted.Code, deleted.Body.String())
	}
}

// assertAttachmentReadable checks the list the author sees and then downloads
// that very attachment through the real Handler, including its original bytes.
func (s *testServer) assertAttachmentReadable(t *testing.T, reportID int64, author *http.Cookie, payload []byte) {
	t.Helper()
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)
	listed := s.request(http.MethodGet, path, nil, author)
	if listed.Code != http.StatusOK {
		t.Fatalf("attachment list = %d: %s", listed.Code, listed.Body.String())
	}
	var envelope struct {
		Data []attachmentView `json:"data"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("attachment list has %d images, want 1", len(envelope.Data))
	}
	if !envelope.Data[0].Available {
		t.Error("successful upload has available:false in the attachment list")
	}
	image := s.request(http.MethodGet, fmt.Sprintf("%s/%d", path, envelope.Data[0].ID), nil, author)
	if image.Code != http.StatusOK {
		t.Fatalf("successful upload then image GET=%d: %s", image.Code, refusal(t, image))
	}
	if !bytes.Equal(image.Body.Bytes(), payload) {
		t.Error("downloaded attachment differs from the original image bytes")
	}
}
