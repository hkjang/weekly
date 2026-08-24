package app

import (
	"fmt"
	"net/http"
	"testing"
)

// scripts/mutation-check.py: four separate comparisons in this handler can be
// reversed with no test noticing. They are the caps on how much one request may
// carry, on an endpoint that accepts files from anyone signed in.
//
// guards: uploadImportPPTX
func TestUploadCapsAreEnforced(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("cap_upload", "USER", nil)
	// AI has to be reachable for the handler to get past its own gate; a
	// disabled gateway answers 503 before any of these checks run.
	if settings := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": "http://ai.invalid/v1/chat/completions",
			"ai.model": "m", "ai.api_key": "k",
			"import.max_files": "3", "import.max_file_mb": "1",
		},
	}, server.admin); settings.Code != http.StatusOK {
		t.Fatalf("configure limits: %d %s", settings.Code, settings.Body.String())
	}

	// Positive control: one ordinary file is accepted, so a refusal below is
	// about the cap and not about the endpoint being shut.
	ok := server.upload("/api/v1/import/pptx", "files", "정상.pptx", []byte("PK\x03\x04small"), author)
	if ok.Code != http.StatusOK && ok.Code != http.StatusCreated && ok.Code != http.StatusAccepted {
		t.Fatalf("one small file was refused: %d %s", ok.Code, ok.Body.String())
	}

	// No files at all is not an empty import, it is a mistake.
	if none := server.uploadMany("/api/v1/import/pptx", "files", map[string][]byte{}, author); none.Code != http.StatusBadRequest {
		t.Errorf("an upload with no files answered %d, want 400 — %s", none.Code, none.Body.String())
	}

	// One past the configured count.
	tooMany := map[string][]byte{}
	for index := 0; index < 4; index++ {
		tooMany[fmt.Sprintf("파일%d.pptx", index)] = []byte("PK\x03\x04small")
	}
	over := server.uploadMany("/api/v1/import/pptx", "files", tooMany, author)
	if over.Code != http.StatusBadRequest {
		t.Errorf("4 files against a limit of 3 answered %d, want 400 — %s", over.Code, over.Body.String())
	}
	if code := errorCode(over); code != "IMPORT_FILES_REQUIRED" {
		t.Errorf("the refusal is coded %q", code)
	}

	// An empty file and an oversized one are both refused, but per file rather
	// than per request: the job is created and that file is marked failed.
	big := make([]byte, (1<<20)+1)
	for index := range big {
		big[index] = 'x'
	}
	oversize := server.upload("/api/v1/import/pptx", "files", "너무큼.pptx", big, author)
	if oversize.Code == http.StatusInternalServerError {
		t.Errorf("an oversized file crashed the endpoint: %s", oversize.Body.String())
	}
	empty := server.upload("/api/v1/import/pptx", "files", "빈파일.pptx", []byte{}, author)
	if empty.Code == http.StatusInternalServerError {
		t.Errorf("an empty file crashed the endpoint: %s", empty.Body.String())
	}
	var failed int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM import_files WHERE status='FAILED' AND original_filename IN ('너무큼.pptx','빈파일.pptx')`).
		Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 2 {
		t.Errorf("%d of the two rejected files were recorded as failed; a file refused without a record is a file the uploader cannot find out about", failed)
	}
}

// guards: deleteReport
//
// Deleting takes a version so two people cannot delete different things by the
// same name. Every comparison in that guard could be reversed untouched.
func TestDeletingAReportNeedsTheVersionInFrontOfYou(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("del_author", "USER", nil)
	id, version := server.draft(author, "2026-08-24", "지울 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d", id)

	for _, item := range []struct{ name, query string }{
		{"버전 없음", ""},
		{"버전이 0", "?version=0"},
		{"버전이 음수", "?version=-1"},
		{"버전이 숫자가 아님", "?version=최신"},
	} {
		w := server.request(http.MethodDelete, path+item.query, nil, author)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: answered %d, want 400 — %s", item.name, w.Code, w.Body.String())
		}
	}
	// A stale version is a different refusal: somebody else moved first.
	if stale := server.request(http.MethodDelete, path+"?version=999", nil, author); stale.Code == http.StatusOK || stale.Code == http.StatusNoContent {
		t.Error("a stale version deleted the report anyway")
	}
	// And the report is still there after every refusal.
	if read := server.request(http.MethodGet, path, nil, author); read.Code != http.StatusOK {
		t.Fatalf("the report went missing during the refusals: %d %s", read.Code, read.Body.String())
	}
	// Positive control last: the right version does delete it.
	done := server.request(http.MethodDelete, fmt.Sprintf("%s?version=%d", path, version), nil, author)
	if done.Code != http.StatusOK && done.Code != http.StatusNoContent {
		t.Fatalf("the correct version was refused: %d %s", done.Code, done.Body.String())
	}
	if gone := server.request(http.MethodGet, path, nil, author); gone.Code == http.StatusOK {
		t.Error("the report survived its own deletion")
	}
}
