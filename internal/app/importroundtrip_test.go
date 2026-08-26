package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// The two headline features have never met. A deck is exported for a meeting;
// a deck is imported to save retyping. Everything between "the model answered"
// and "the report exists" — the worker, the extractor, the week detector, the
// projection into report items — ran only in production.
//
// With a gateway that answers, the round trip can be run: export what one
// person wrote, feed it back as somebody else's upload, and see the work come
// out the other side.

// awaitImportJob waits for a job to stop moving. It polls a terminal status
// rather than sleeping a guessed interval: the worker is a goroutine, and the
// question is whether it finishes, not how fast.
func awaitImportJob(t *testing.T, server *testServer, jobID int64) (string, string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var status, message string
	for time.Now().Before(deadline) {
		if err := server.app.db.QueryRow(server.ctx(),
			`SELECT j.status, coalesce(max(f.error_message), '')
				FROM import_jobs j LEFT JOIN import_files f ON f.import_job_id = j.id
				WHERE j.id = $1 GROUP BY j.status`, jobID).Scan(&status, &message); err != nil {
			t.Fatal(err)
		}
		switch status {
		case "READY", "PARTIAL", "FAILED":
			return status, message
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("가져오기 작업이 20초 안에 끝나지 않았습니다: 마지막 상태 %q", status)
	return status, message
}

// guards: processImportFile
func TestADeckExportedFromOneWeekCanBeImportedIntoAnother(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("roundtrip_author", "USER", nil)

	// One week, written out properly, then exported the way a presenter would.
	id, version := server.draft(author, "2026-08-24", "회의에 들고 갈 요약")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "회의에 들고 갈 요약", "version": version,
		"items": []map[string]any{{
			"category": "인프라", "title": "격오지 회선 이설", "currentResult": "3개 지사 완료",
			"nextPlan": "잔여 2개 지사", "issue": "임대 회선 일정 지연", "progress": 60,
		}},
	}, author)
	if filled.Code != http.StatusOK {
		t.Fatalf("보고서 저장: %d %s", filled.Code, filled.Body.String())
	}
	deck := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
	if deck.Code != http.StatusOK {
		t.Fatalf("내보내기: %d %s", deck.Code, deck.Body.String())
	}
	if deck.Body.Len() < 1024 {
		t.Fatalf("내보낸 파일이 %d바이트뿐입니다", deck.Body.Len())
	}

	// The gateway reads the deck and answers with what it found. This is the
	// contract the product asks of a model, written out by hand.
	server.aiGateway(t, map[string]any{
		"summary": "가져온 요약", "weekStart": "2026-09-07", "dateConfidence": 0.9,
		"reportItems": []any{map[string]any{
			"category": "인프라", "title": "격오지 회선 이설",
			"currentResults": []string{"3개 지사 완료"},
			"nextPlans":      []string{"잔여 2개 지사"},
			"issues":         []string{"임대 회선 일정 지연"},
			"progress":       60, "confidence": 0.9, "categoryConfidence": 0.9,
			"sourceSlides": []int{1},
		}},
		"warnings": []string{},
	})

	upload := server.upload("/api/v1/import/pptx", "files", "지난주.pptx", deck.Body.Bytes(), author)
	if upload.Code != http.StatusAccepted && upload.Code != http.StatusOK && upload.Code != http.StatusCreated {
		t.Fatalf("업로드: %d %s", upload.Code, upload.Body.String())
	}
	var envelope struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}

	status, message := awaitImportJob(t, server, envelope.Data.ID)
	if status != "READY" {
		t.Fatalf("내보낸 자기 자신을 다시 읽지 못했습니다: %q — %s", status, message)
	}

	// And what came back has to be the work, not an empty shell.
	var parsed string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT coalesce(parsed_result::text, '') FROM import_files WHERE import_job_id = $1 LIMIT 1`,
		envelope.Data.ID).Scan(&parsed); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"격오지 회선 이설", "3개 지사 완료", "잔여 2개 지사"} {
		if !containsAny(parsed, expected) {
			t.Errorf("가져온 결과에 %q 가 없습니다: %s", expected, parsed)
		}
	}
}
