package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// stageImport puts a job and one analysed file straight into READY, which is
// where confirmation begins. Driving a real PPTX through the AI gateway to get
// here would test the gateway, not the rule under examination.
func (s *testServer) stageImport(cookie *http.Cookie, username string) (jobID, fileID int64) {
	s.t.Helper()
	var userID int64
	if err := s.app.db.QueryRow(s.ctx(), `SELECT id FROM users WHERE username=$1`, username).Scan(&userID); err != nil {
		s.t.Fatal(err)
	}
	if err := s.app.db.QueryRow(s.ctx(),
		`INSERT INTO import_jobs(user_id,status,total_files,processed_files) VALUES($1,'READY',1,1) RETURNING id`,
		userID).Scan(&jobID); err != nil {
		s.t.Fatal(err)
	}
	if err := s.app.db.QueryRow(s.ctx(),
		`INSERT INTO import_files(import_job_id,original_filename,file_hash,size_bytes,status)
		 VALUES($1,'주간보고.pptx',repeat('a',64),1024,'READY') RETURNING id`, jobID).Scan(&fileID); err != nil {
		s.t.Fatal(err)
	}
	return jobID, fileID
}

// guards: confirmImportJob
//
// docs/openapi.yaml says reviewedAt is "검토자가 승인 또는 반려한 시각. 작성자가
// 내용을 고치면 다시 null이 됩니다". Replacing a report from a PPTX is the author
// changing the content by another route, so the same has to hold — otherwise the
// response reports a review of text that no longer exists.
func TestAnImportReplacementDoesNotKeepTheOldReviewStamp(t *testing.T) {
	server := newTestServer(t)
	server.enableWorkflow()
	author := server.createUser("imp_author", "USER", nil)
	var username string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT username FROM users WHERE id=(SELECT user_id FROM user_sessions WHERE token_hash=$1)`,
		tokenHash(author.Value)).Scan(&username); err != nil {
		t.Fatal(err)
	}

	const week = "2026-08-24"
	reportID := server.submitted(author, week, "직접 쓴 내용")
	if approved := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", reportID),
		map[string]any{}, server.admin); approved.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}
	var reviewedAt *string
	if err := server.app.db.QueryRow(server.ctx(), `SELECT reviewed_at::text FROM weekly_reports WHERE id=$1`, reportID).Scan(&reviewedAt); err != nil {
		t.Fatal(err)
	}
	if reviewedAt == nil {
		t.Fatal("the report was approved but carries no review time; this test would prove nothing")
	}

	jobID, fileID := server.stageImport(author, username)
	confirm := server.request(http.MethodPost, fmt.Sprintf("/api/v1/import/%d/confirm", jobID), map[string]any{
		"files": []map[string]any{{
			"id": fileID, "weekStart": week, "strategy": "REPLACE", "summary": "PPTX에서 가져온 내용",
			"items": []map[string]any{{"category": "인프라", "title": "회선 이설", "currentResult": "완료", "progress": 100}},
		}},
	}, author)
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm the import: %d %s", confirm.Code, confirm.Body.String())
	}

	var summary, status string
	var afterReview *string
	var reviewer *int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT summary,status,reviewed_at::text,reviewed_by FROM weekly_reports WHERE id=$1`, reportID).
		Scan(&summary, &status, &afterReview, &reviewer); err != nil {
		t.Fatal(err)
	}
	if summary != "PPTX에서 가져온 내용" {
		t.Fatalf("the replacement did not take: summary is %q", summary)
	}
	if afterReview != nil {
		t.Errorf("the report was rewritten from a PPTX and still reports reviewedAt=%s", *afterReview)
	}
	if reviewer != nil {
		t.Errorf("the report still credits reviewer %d for text that was replaced", *reviewer)
	}
}

// guards: confirmImportJob
//
// Merging is the quieter half of the same act: it adds paragraphs nobody
// reviewed to a report somebody approved.
func TestAnImportMergeDoesNotKeepTheOldReviewStamp(t *testing.T) {
	server := newTestServer(t)
	server.enableWorkflow()
	author := server.createUser("imp_merge", "USER", nil)
	var username string
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT username FROM users WHERE id=(SELECT user_id FROM user_sessions WHERE token_hash=$1)`,
		tokenHash(author.Value)).Scan(&username); err != nil {
		t.Fatal(err)
	}

	const week = "2026-08-24"
	reportID := server.submitted(author, week, "직접 쓴 내용")
	if approved := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", reportID),
		map[string]any{}, server.admin); approved.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}

	jobID, fileID := server.stageImport(author, username)
	confirm := server.request(http.MethodPost, fmt.Sprintf("/api/v1/import/%d/confirm", jobID), map[string]any{
		"files": []map[string]any{{
			"id": fileID, "weekStart": week, "strategy": "MERGE", "summary": "PPTX에서 더한 내용",
			"items": []map[string]any{{"category": "개발", "title": "적재 배치 개선", "currentResult": "착수", "progress": 20}},
		}},
	}, author)
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm the merge: %d %s", confirm.Code, confirm.Body.String())
	}

	var summary string
	var reviewedAt *string
	var reviewer *int64
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT summary,reviewed_at::text,reviewed_by FROM weekly_reports WHERE id=$1`, reportID).
		Scan(&summary, &reviewedAt, &reviewer); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "PPTX에서 더한 내용") {
		t.Fatalf("the merge did not take: summary is %q", summary)
	}
	if reviewedAt != nil || reviewer != nil {
		t.Errorf("text was merged in but the report still reports a review of what came before: at=%s by=%v", stringOrNone(reviewedAt), numberOrNone(reviewer))
	}
	// A merge keeps what was already there; losing it would be a different bug.
	if !strings.Contains(summary, "직접 쓴 내용") {
		t.Errorf("the merge dropped the original summary: %q", summary)
	}
}

func stringOrNone(value *string) string {
	if value == nil {
		return "none"
	}
	return *value
}

func numberOrNone(value *int64) string {
	if value == nil {
		return "none"
	}
	return fmt.Sprintf("%d", *value)
}
