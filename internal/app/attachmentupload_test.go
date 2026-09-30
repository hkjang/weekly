package app

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// A refused upload has to leave the report exactly as it found it.
//
// The handler stored each image as it reached it — write the file, insert the
// row, move on — and only then looked at the next one. So a request whose
// second file was a PDF somebody had renamed answered 400 UNSUPPORTED_IMAGE
// with the first image already attached and on disk. The screen says the upload
// failed, so the writer picks the good files again and sends them once more,
// and now the report carries the first one twice: two rows, one placement, one
// capture printed twice in the exported deck. writeAttachmentFile already
// promises one file is written whole or not at all; a request that stores some
// of its images and reports failure breaks the same promise one level up.
//
// guards: uploadAttachments
func TestARefusedAttachmentUploadStoresNoneOfTheImages(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_partial", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "캡처가 붙은 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)

	// uploadMany sends the files in sorted name order, so the good one is
	// decoded and would be stored before the handler ever sees the bad one.
	files := map[string][]byte{
		"a-good.png": samplePNG(t, 4, 4),
		"b-bad.png":  []byte("이것은 이미지가 아니라 이름만 바꾼 문서입니다."),
	}
	response := server.uploadMany(path, "files", files, author)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("an upload holding a file that is not an image answered %d: %s",
			response.Code, response.Body.String())
	}

	stored := server.attachmentCount(reportID)
	if stored != 0 {
		t.Errorf("a refused upload stored %d image(s); the writer was told none of it was kept: %s",
			stored, refusal(t, response))
	}
	if left := server.attachmentFiles(t, reportID); len(left) != 0 {
		t.Errorf("a refused upload left %v on disk", left)
	}

	// And the refusal is not bought by refusing everything: the same two
	// images, both readable, are still stored together.
	good := server.uploadMany(path, "files", map[string][]byte{
		"a-good.png": samplePNG(t, 4, 4),
		"b-good.png": samplePNG(t, 6, 6),
	}, author)
	if good.Code != http.StatusCreated && good.Code != http.StatusOK {
		t.Fatalf("a readable upload answered %d: %s", good.Code, good.Body.String())
	}
	if stored := server.attachmentCount(reportID); stored != 2 {
		t.Errorf("two readable images stored %d rows", stored)
	}
}

// attachmentCount reads the rows the upload actually left behind, rather than
// what the response said it left behind — the two are the point of the test.
func (s *testServer) attachmentCount(reportID int64) int {
	s.t.Helper()
	var count int
	if err := s.app.db.QueryRow(s.ctx(),
		`SELECT count(*) FROM report_attachments WHERE report_id=$1`, reportID).Scan(&count); err != nil {
		s.t.Fatalf("count the stored captures: %v", err)
	}
	return count
}

// attachmentFiles lists the report's stored images on disk.
func (s *testServer) attachmentFiles(t *testing.T, reportID int64) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDirectoryAttachments, strconv.FormatInt(reportID, 10)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read the capture directory: %v", err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
