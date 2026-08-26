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
			JobID  int64  `json:"jobId"`
			ID     int64  `json:"id"`
			Status string `json:"status"`
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
	// "닫혔다" 로는 부족합니다. 전부 거부됐으면 PARTIAL 이 아니라 FAILED 여야
	// 하고, 올린 사람이 화면에서 읽는 것이 바로 그 낱말입니다. 경계가 한 칸
	// 밀리면 한 건도 들어가지 않은 가져오기가 "부분 성공" 으로 남습니다.
	if failed == total && status != "FAILED" {
		t.Errorf("파일 %d개가 모두 거부됐는데 상태가 %q 입니다", total, status)
	}
	if failed == 0 {
		t.Errorf("거부된 파일이 실패로 세어지지 않았습니다: 실패 %d / 전체 %d", failed, total)
	}
	if processed != total {
		t.Errorf("처리 수가 전체와 다릅니다: %d / %d", processed, total)
	}
	// And the answer to the upload has to be that same state. Deriving it a
	// second time is how the two came apart: the record said FAILED and the
	// reply said READY, which is one of the words that means it worked.
	if envelope.Data.Status != status {
		t.Errorf("업로드 응답은 %q 라고 하는데 기록은 %q 입니다", envelope.Data.Status, status)
	}
}

// guards: uploadImportPPTX
//
// The request body is capped at what the administrator's own settings imply —
// files × size, plus a little — and then held under a hard 500 MB ceiling. The
// ceiling is a clamp downwards. Turn its comparison around and it becomes a
// clamp upwards: a deployment that limits imports to one small file starts
// accepting half-gigabyte bodies, and the setting an administrator lowered on
// purpose stops meaning anything.
func TestTheConfiguredImportLimitsBoundTheRequest(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("import_limits", "USER", nil)

	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": "https://ai.internal/v1", "ai.model": "weekly-1",
			// One file of at most one megabyte: a request of eight cannot be
			// anything the administrator asked to allow.
			"import.max_files": "1", "import.max_file_mb": "1",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("설정을 저장하지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	oversized := make([]byte, 8<<20)
	reply := server.upload("/api/v1/import/pptx", "files", "커다란파일.pptx", oversized, author)
	if reply.Code == http.StatusOK || reply.Code == http.StatusCreated || reply.Code == http.StatusAccepted {
		t.Fatalf("한도를 1MB로 낮췄는데 8MB 요청이 받아들여졌습니다: %d %s", reply.Code, reply.Body.String())
	}

	// And nothing was left half-written behind the refusal.
	var jobs int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM import_files WHERE original_filename = '커다란파일.pptx'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Errorf("거부된 업로드가 파일 기록 %d개를 남겼습니다", jobs)
	}
}

// guards: uploadImportPPTX
//
// Between "everything failed" and "everything was queued" there is a third
// outcome, and it is the one the CASE arm nobody could reach describes: some
// files rejected, the rest already imported before. Nothing is queued, so the
// job closes immediately — but it did not fail outright, and telling the
// uploader it did would send them looking for a problem with files that were
// simply already there.
func TestAnImportOfRejectedAndAlreadySeenFilesIsPartial(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("import_partial", "USER", nil)

	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": "https://ai.internal/v1", "ai.model": "weekly-1",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("AI 를 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	// First upload: one file, accepted and queued.
	seen := []byte("이미 한 번 올린 파일의 내용")
	first := server.upload("/api/v1/import/pptx", "files", "이미본.pptx", seen, author)
	if first.Code != http.StatusAccepted && first.Code != http.StatusOK && first.Code != http.StatusCreated {
		t.Fatalf("첫 업로드: %d %s", first.Code, first.Body.String())
	}

	// Second upload: the same bytes again, beside an empty file. Neither is
	// queued — one is a duplicate, one is refused — so the job closes on the
	// spot, with some failures but not all.
	second := server.uploadMany("/api/v1/import/pptx", "files", map[string][]byte{
		"이미본.pptx": seen,
		"빈것.pptx":  {},
	}, author)
	if second.Code != http.StatusAccepted && second.Code != http.StatusOK && second.Code != http.StatusCreated {
		t.Fatalf("둘째 업로드: %d %s", second.Code, second.Body.String())
	}
	var envelope struct {
		Data struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}

	var status string
	var failed, total int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT status, failed_files, total_files FROM import_jobs WHERE id=$1`, envelope.Data.ID).
		Scan(&status, &failed, &total); err != nil {
		t.Fatal(err)
	}
	if failed == 0 || failed >= total {
		t.Fatalf("일부만 실패한 상태를 만들지 못했습니다: 실패 %d / 전체 %d", failed, total)
	}
	if status != "PARTIAL" {
		t.Errorf("일부 실패·나머지 중복인데 상태가 %q 입니다", status)
	}
	if envelope.Data.Status != status {
		t.Errorf("업로드 응답은 %q 라고 하는데 기록은 %q 입니다", envelope.Data.Status, status)
	}
}
