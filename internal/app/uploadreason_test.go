package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A zero byte upload was told it might have exceeded the administrator's size
// limit — the opposite fact, with the opposite remedy. One branch carried three
// causes (a read that broke, an empty file, a file over the limit) and one
// sentence named two of them with an "또는".
//
// guards: uploadImportPPTX
func TestAnImportSaysWhichOfTheThreeThingsWentWrong(t *testing.T) {
	server := newTestServer(t)
	server.enableAIWithoutAGateway(t)
	author := server.createUser("upload_reason", "USER", nil)

	reasonFor := func(name string, body []byte) string {
		t.Helper()
		response := server.upload("/api/v1/import/pptx", "files", name, body, author)
		if response.Code != http.StatusAccepted {
			t.Fatalf("upload %s: %d %s", name, response.Code, response.Body.String())
		}
		var created struct {
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode the job: %v", err)
		}
		var message string
		if err := server.app.db.QueryRow(server.ctx(),
			`SELECT coalesce(error_message,'') FROM import_files WHERE import_job_id=$1`, created.Data.ID).Scan(&message); err != nil {
			t.Fatalf("read the file's outcome: %v", err)
		}
		if strings.Contains(message, "비어 있거나") {
			t.Errorf("%s: the reader is left to guess between two opposite facts: %s", name, message)
		}
		return message
	}

	empty := reasonFor("zero.pptx", nil)
	if !strings.Contains(empty, "0바이트") {
		t.Errorf("an empty file: %q", empty)
	}
	if strings.Contains(empty, "제한") {
		t.Errorf("an empty file was told about a size limit it cannot have hit: %q", empty)
	}

	// The administrator's per-file limit, exceeded.
	limit := server.app.settingInt(server.ctx(), "import.max_file_mb", 25)
	over := reasonFor("huge.pptx", make([]byte, limit<<20+1024))
	if !strings.Contains(over, fmt.Sprintf("%dMB", limit)) {
		t.Errorf("an oversized file is not told the limit it hit: %q", over)
	}
	if strings.Contains(over, "0바이트") {
		t.Errorf("an oversized file was described as empty: %q", over)
	}
}

// The attachment upload refused a request past its cap and a body that did not
// parse with one sentence joined by "거나", leaving the reader to guess between
// sending fewer images and sending them again. Go says which it was.
//
// guards: uploadAttachments
func TestAnOversizedAttachmentUploadIsNotCalledMalformed(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("upload_attach", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "첨부 상한 보고")

	maxPerReport, maxBytes := server.app.attachmentLimits(server.ctx())
	// Past the whole-request cap: every file allowed, plus the slack.
	oversized := make([]byte, maxBytes*int64(maxPerReport)+(4<<20))
	response := server.upload(fmt.Sprintf("/api/v1/reports/%d/attachments", reportID),
		"files", "huge.png", oversized, author)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("an oversized upload answered %d: %s", response.Code, response.Body.String())
	}
	message := refusal(t, response)
	if strings.Contains(message, "올바르지 않거나") {
		t.Errorf("the reader is left to guess why the upload failed: %s", message)
	}
	if !strings.Contains(message, "나눠서") {
		t.Errorf("the message does not say what to do instead: %s", message)
	}
}
