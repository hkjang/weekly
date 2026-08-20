package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestDatabaseMigrationsAndSecretRotation(t *testing.T) {
	dsn := os.Getenv("WEEKLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEEKLY_TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()
	db, err := openDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Derived from the embedded files rather than written out, because a
	// literal here goes stale the moment a migration is added and the test only
	// runs when a DSN is configured, so nobody finds out for several releases.
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != len(entries) {
		t.Fatalf("migration version: got=%d want=%d err=%v", version, len(entries), err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM users WHERE username='clone-test'`); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := db.QueryRow(ctx, `INSERT INTO users(username,display_name,role) VALUES('clone-test','Clone Test','USER') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var sourceID int64
	if err := db.QueryRow(ctx, `INSERT INTO weekly_reports(user_id,week_start,summary) VALUES($1,'2026-08-03','원본 요약') RETURNING id`, userID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO report_items(report_id,category,title,current_result,next_plan,issue,progress) VALUES($1,'개발','복제 통합 테스트','완료','배포','',80)`, sourceID); err != nil {
		t.Fatal(err)
	}
	application := &App{db: db}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/reports/"+strconv.FormatInt(sourceID, 10)+"/clone", strings.NewReader(`{"targetWeekStart":"2026-08-10","mode":"STRUCTURE"}`))
	request.SetPathValue("id", strconv.FormatInt(sourceID, 10))
	request = request.WithContext(context.WithValue(request.Context(), principalContext, &principal{ID: userID, Username: "clone-test", Role: "USER"}))
	response := httptest.NewRecorder()
	application.cloneReport(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("clone handler: status=%d body=%s", response.Code, response.Body.String())
	}
	var clonedSummary, clonedStatus, sourceType, sourceRef, currentResult string
	if err := db.QueryRow(ctx, `SELECT r.summary,r.status,r.source_type,coalesce(r.source_ref,''),i.current_result
		FROM weekly_reports r JOIN report_items i ON i.report_id=r.id WHERE r.user_id=$1 AND r.week_start='2026-08-10'`, userID).
		Scan(&clonedSummary, &clonedStatus, &sourceType, &sourceRef, &currentResult); err != nil {
		t.Fatal(err)
	}
	if clonedSummary != "" || clonedStatus != "DRAFT" || sourceType != "CLONED" || sourceRef != "report:"+strconv.FormatInt(sourceID, 10) || currentResult != "" {
		t.Fatalf("unexpected cloned report: summary=%q status=%q source=%q ref=%q result=%q", clonedSummary, clonedStatus, sourceType, sourceRef, currentResult)
	}

	var importJobID, importFileID int64
	if err := db.QueryRow(ctx, `INSERT INTO import_jobs(user_id,status,total_files,processed_files) VALUES($1,'READY',1,1) RETURNING id`, userID).Scan(&importJobID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO import_files(import_job_id,original_filename,file_hash,size_bytes,status,detected_week_start,detected_week_end,confidence)
		VALUES($1,'legacy-confirm.pptx',$2,1024,'READY','2026-08-17','2026-08-23',0.95) RETURNING id`, importJobID, strings.Repeat("a", 64)).Scan(&importFileID); err != nil {
		t.Fatal(err)
	}
	confirmBody := `{"files":[{"id":` + strconv.FormatInt(importFileID, 10) + `,"selected":true,"weekStart":"2026-08-17","summary":"Import 계약 검증","strategy":"CREATE","items":[{"category":"개발","title":"Import 확정","currentResult":"계약 수정 완료, 회귀 검증 완료.","nextPlan":"운영 확인","issue":"","progress":90,"confidence":0.9}]}]}`
	request = httptest.NewRequest(http.MethodPost, "/api/v1/import/"+strconv.FormatInt(importJobID, 10)+"/confirm", strings.NewReader(confirmBody))
	request.SetPathValue("id", strconv.FormatInt(importJobID, 10))
	request = request.WithContext(context.WithValue(request.Context(), principalContext, &principal{ID: userID, Username: "clone-test", Role: "USER"}))
	response = httptest.NewRecorder()
	application.confirmImportJob(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("confirm import handler: status=%d body=%s", response.Code, response.Body.String())
	}
	var importedStatus, importedSource, importedResult string
	if err := db.QueryRow(ctx, `SELECT r.status,r.source_type,i.current_result FROM weekly_reports r JOIN report_items i ON i.report_id=r.id
		WHERE r.user_id=$1 AND r.week_start='2026-08-17'`, userID).Scan(&importedStatus, &importedSource, &importedResult); err != nil {
		t.Fatal(err)
	}
	if importedStatus != "CLOSED" || importedSource != "PPTX_IMPORT" || importedResult != "• 계약 수정 완료\n• 회귀 검증 완료." {
		t.Fatalf("unexpected imported report: status=%q source=%q result=%q", importedStatus, importedSource, importedResult)
	}

	if _, err := db.Exec(ctx, `UPDATE app_settings SET value=CASE key WHEN 'ai.enabled' THEN 'true' WHEN 'ai.endpoint' THEN 'http://127.0.0.1/unused' WHEN 'ai.model' THEN 'test-model' ELSE value END
		WHERE key IN ('ai.enabled','ai.endpoint','ai.model'); UPDATE app_settings SET value='' WHERE key='ai.api_key'`); err != nil {
		t.Fatal(err)
	}
	var retryJobID, readyFileID, failedFileID int64
	if err := db.QueryRow(ctx, `INSERT INTO import_jobs(user_id,status,total_files,processed_files,failed_files) VALUES($1,'PARTIAL',2,2,1) RETURNING id`, userID).Scan(&retryJobID); err != nil {
		t.Fatal(err)
	}
	retryPath := func(fileID int64) string {
		return "/var/lib/weekly/imports/" + strconv.FormatInt(retryJobID, 10) + "/" + strconv.FormatInt(fileID, 10) + ".pptx"
	}
	if err := db.QueryRow(ctx, `INSERT INTO import_files(import_job_id,original_filename,stored_path,file_hash,size_bytes,status)
		VALUES($1,'ready.pptx','pending',$2,1024,'READY') RETURNING id`, retryJobID, strings.Repeat("b", 64)).Scan(&readyFileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE import_files SET stored_path=$1 WHERE id=$2`, retryPath(readyFileID), readyFileID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO import_files(import_job_id,original_filename,stored_path,file_hash,size_bytes,status)
		VALUES($1,'failed.pptx','pending',$2,1024,'FAILED') RETURNING id`, retryJobID, strings.Repeat("c", 64)).Scan(&failedFileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE import_files SET stored_path=$1 WHERE id=$2`, retryPath(failedFileID), failedFileID); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/import/"+strconv.FormatInt(retryJobID, 10)+"/analyze", strings.NewReader(`{"fileIds":[`+strconv.FormatInt(readyFileID, 10)+`]}`))
	request.SetPathValue("id", strconv.FormatInt(retryJobID, 10))
	request = request.WithContext(context.WithValue(request.Context(), principalContext, &principal{ID: userID, Username: "clone-test", Role: "USER"}))
	response = httptest.NewRecorder()
	application.retryImportJob(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("selected import retry: status=%d body=%s", response.Code, response.Body.String())
	}
	var readyStatus, failedStatus, retryJobStatus string
	var processedFiles, failedFiles int
	if err := db.QueryRow(ctx, `SELECT (SELECT status FROM import_files WHERE id=$1),(SELECT status FROM import_files WHERE id=$2),status,processed_files,failed_files
		FROM import_jobs WHERE id=$3`, readyFileID, failedFileID, retryJobID).Scan(&readyStatus, &failedStatus, &retryJobStatus, &processedFiles, &failedFiles); err != nil {
		t.Fatal(err)
	}
	if readyStatus != "QUEUED" || failedStatus != "FAILED" || retryJobStatus != "PENDING" || processedFiles != 1 || failedFiles != 1 {
		t.Fatalf("selected retry states: ready=%s failed=%s job=%s processed=%d failedCount=%d", readyStatus, failedStatus, retryJobStatus, processedFiles, failedFiles)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/import/"+strconv.FormatInt(retryJobID, 10)+"/analyze", strings.NewReader(`{}`))
	request.SetPathValue("id", strconv.FormatInt(retryJobID, 10))
	request = request.WithContext(context.WithValue(request.Context(), principalContext, &principal{ID: userID, Username: "clone-test", Role: "USER"}))
	response = httptest.NewRecorder()
	application.retryImportJob(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("legacy failed import retry: status=%d body=%s", response.Code, response.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT status FROM import_files WHERE id=$1`, failedFileID).Scan(&failedStatus); err != nil || failedStatus != "QUEUED" {
		t.Fatalf("legacy failed status=%s err=%v", failedStatus, err)
	}

	legacy, _ := newSecretBox(make([]byte, 32))
	activeKey := make([]byte, 32)
	activeKey[0] = 1
	active, _ := newSecretBox(activeKey)
	stored, _ := legacy.Encrypt("upgrade-safe-key")
	if _, err := db.Exec(ctx, `UPDATE app_settings SET value=$1 WHERE key='ai.api_key'`, stored); err != nil {
		t.Fatal(err)
	}
	result, err := migrateSecretSettings(ctx, db, active, legacy)
	if err != nil || result.Migrated != 1 || len(result.Unavailable) != 0 {
		t.Fatalf("secret migration: result=%+v err=%v", result, err)
	}
	if err := db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key='ai.api_key'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	plain, err := active.Decrypt(stored)
	if err != nil || plain != "upgrade-safe-key" {
		t.Fatalf("migrated secret: plain=%q err=%v", plain, err)
	}
}

// TestEmbeddingStalenessIsDetected pins the rule that decides which items get
// re-embedded. It has to run against PostgreSQL because the decision is a SQL
// predicate over a digest PostgreSQL computes; there is nothing to assert in Go.
func TestEmbeddingStalenessIsDetected(t *testing.T) {
	dsn := os.Getenv("WEEKLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WEEKLY_TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()
	db, err := openDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var vectorInstalled bool
	if err := db.QueryRow(ctx, `SELECT to_regtype('vector') IS NOT NULL`).Scan(&vectorInstalled); err != nil {
		t.Fatal(err)
	}
	if !vectorInstalled {
		t.Skip("pgvector is not installed")
	}

	if _, err := db.Exec(ctx, `DELETE FROM users WHERE username='embed-stale-test'`); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := db.QueryRow(ctx, `INSERT INTO users(username,display_name,role) VALUES('embed-stale-test','Embed Stale','USER') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	var reportID int64
	if err := db.QueryRow(ctx, `INSERT INTO weekly_reports(user_id,week_start,summary) VALUES($1,'2026-08-17','') RETURNING id`, userID).Scan(&reportID); err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err := db.QueryRow(ctx, `INSERT INTO report_items(report_id,category,title,current_result,next_plan,issue,progress)
		VALUES($1,'개발','임베딩 갱신 검증','1차 완료','2차 진행','',50) RETURNING id`, reportID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}

	application := &App{db: db}
	const model = "embed-stale-test-model"
	contains := func(step string, want bool) {
		t.Helper()
		items, err := application.pendingEmbeddings(ctx, model, 500)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range items {
			if item.id == itemID {
				found = true
			}
		}
		if found != want {
			t.Fatalf("%s: pending=%v want=%v", step, found, want)
		}
	}

	contains("never embedded", true)
	if _, err := db.Exec(ctx, `INSERT INTO report_item_embeddings(report_item_id,embedding,model,dimensions,content_hash,updated_at)
		SELECT i.id,'[1,2,3]'::vector,$2,3,
			encode(sha256(convert_to(concat_ws(E'\n', i.title, i.category, i.current_result, i.next_plan, i.issue), 'UTF8')), 'hex'), now()
		FROM report_items i WHERE i.id=$1`, itemID, model); err != nil {
		t.Fatal(err)
	}
	contains("embedded and unchanged", false)
	if _, err := db.Exec(ctx, `UPDATE report_items SET next_plan='2차 보류' WHERE id=$1`, itemID); err != nil {
		t.Fatal(err)
	}
	contains("edited after embedding", true)
	if _, err := db.Exec(ctx, `UPDATE report_item_embeddings SET model='other-model' WHERE report_item_id=$1`, itemID); err != nil {
		t.Fatal(err)
	}
	contains("embedded under a different model", true)
}
