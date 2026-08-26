package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An import where every file is rejected has nothing to process, so the job has
// to be closed there and then. Invert the condition and the opposite happens:
// a job with work queued is closed before the work runs, and a job with none
// stays open for ever — the uploader watches a progress bar that will never
// move and has nothing to tell them why.

// guards: uploadImportPPTX
func TestAnImportWhereEveryFileIsRejectedDoesNotStayOpen(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("import_dead", "USER", nil)

	// The upload path is behind the AI gateway switch, so the branch this test
	// is about is unreachable until the feature is on. Nothing is called here:
	// a rejected file is rejected while the request is still being read.
	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": "https://ai.internal/v1", "ai.model": "weekly-1",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("AI 를 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	// An empty file is refused before anything is queued.
	reply := server.upload("/api/v1/import/pptx", "files", "빈파일.pptx", []byte{}, author)
	if reply.Code != http.StatusOK && reply.Code != http.StatusCreated && reply.Code != http.StatusAccepted {
		t.Fatalf("업로드: %d %s", reply.Code, reply.Body.String())
	}
	var envelope struct {
		Data struct {
			JobID int64 `json:"jobId"`
			ID    int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	jobID := envelope.Data.JobID
	if jobID == 0 {
		jobID = envelope.Data.ID
	}
	if jobID == 0 {
		t.Fatalf("작업 식별자를 찾지 못했습니다: %s", reply.Body.String())
	}

	var status string
	var processed, failed, total int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT status, processed_files, failed_files, total_files FROM import_jobs WHERE id=$1`, jobID).
		Scan(&status, &processed, &failed, &total); err != nil {
		t.Fatal(err)
	}
	if status == "PENDING" || status == "PROCESSING" {
		t.Errorf("처리할 것이 하나도 없는데 작업이 %q 로 열려 있습니다 (전체 %d, 실패 %d)", status, total, failed)
	}
	if failed == 0 {
		t.Errorf("거부된 파일이 실패로 세어지지 않았습니다: 실패 %d / 전체 %d", failed, total)
	}
	if processed != total {
		t.Errorf("처리 수가 전체와 다릅니다: %d / %d", processed, total)
	}
}
