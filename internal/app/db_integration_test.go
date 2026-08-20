package app

import (
	"context"
	"io"
	"log/slog"
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
	// The status history references the actor without a cascade, so the user row
	// cannot go first. Without this the test passes once and fails on every rerun
	// against the same database.
	if _, err := db.Exec(ctx, `DELETE FROM report_status_history WHERE actor_id IN (SELECT id FROM users WHERE username='clone-test')`); err != nil {
		t.Fatal(err)
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

// TestLoginThrottleBlocksAndClears runs against PostgreSQL because the counter
// is a query over a window, not a value Go holds.
func TestLoginThrottleBlocksAndClears(t *testing.T) {
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
	application := &App{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	const username = "throttle-test"
	const address = "203.0.113.7"
	if _, err := db.Exec(ctx, `DELETE FROM login_attempts WHERE lower(username)=lower($1)`, username); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, `DELETE FROM login_attempts WHERE lower(username)=lower($1)`, username)

	limit := application.settingInt(ctx, "auth.max_login_attempts", 0)
	if limit <= 0 {
		t.Fatalf("auth.max_login_attempts should ship enabled, got %d", limit)
	}
	for attempt := 1; attempt <= limit; attempt++ {
		if throttle := application.loginThrottleFor(ctx, username, address); throttle.Blocked {
			t.Fatalf("blocked after %d failures, limit is %d", attempt-1, limit)
		}
		application.recordLoginFailure(ctx, username, address)
	}
	throttle := application.loginThrottleFor(ctx, username, address)
	if !throttle.Blocked {
		t.Fatalf("not blocked after %d failures", limit)
	}
	if throttle.RetryAfter <= 0 {
		t.Fatalf("blocked without a retry window: %v", throttle.RetryAfter)
	}
	// Case must not matter, or an attacker gets a fresh budget per spelling.
	if upper := application.loginThrottleFor(ctx, "THROTTLE-TEST", address); !upper.Blocked {
		t.Fatal("a different letter case escaped the limit")
	}
	// A different account is unaffected: the account limit must not become an
	// accidental site-wide outage.
	if other := application.loginThrottleFor(ctx, "throttle-test-other", address); other.Blocked {
		t.Fatal("an unrelated account was blocked by the account limit")
	}
	application.clearLoginFailures(ctx, username)
	if after := application.loginThrottleFor(ctx, username, address); after.Blocked || after.Failures != 0 {
		t.Fatalf("a successful login did not clear the counter: %+v", after)
	}
}

// TestWorkItemMergeAndSplitSurviveTheNextSave is the point of the feature. Both
// corrections are easy to make and easy to lose: identity is re-derived from the
// title every time a report is saved, so a correction that does not change what
// resolution returns is undone by the author's next edit.
func TestWorkItemMergeAndSplitSurviveTheNextSave(t *testing.T) {
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
	application := &App{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if _, err := db.Exec(ctx, `DELETE FROM users WHERE username='split-test'`); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := db.QueryRow(ctx, `INSERT INTO users(username,display_name,role) VALUES('split-test','Split Test','USER') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)

	weeks := []string{"2026-07-06", "2026-07-13", "2026-07-20"}
	reportIDs := make([]int64, len(weeks))
	for index, week := range weeks {
		if err := db.QueryRow(ctx, `INSERT INTO weekly_reports(user_id,week_start,summary) VALUES($1,$2,'') RETURNING id`, userID, week).Scan(&reportIDs[index]); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := application.persistReportItems(ctx, tx, reportIDs[index], userID,
			[]reportItem{{Category: "개발", Title: "통합 인증 개선", CurrentResult: "진행", Progress: 30 * (index + 1)}}); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	workItemOf := func(reportID int64) (int64, bool) {
		t.Helper()
		var id *int64
		var pinned bool
		if err := db.QueryRow(ctx, `SELECT work_item_id, work_item_pinned FROM report_items WHERE report_id=$1`, reportID).Scan(&id, &pinned); err != nil {
			t.Fatal(err)
		}
		if id == nil {
			t.Fatalf("report %d has no work item", reportID)
		}
		return *id, pinned
	}
	resave := func(reportID int64, title string) {
		t.Helper()
		var itemID int64
		if err := db.QueryRow(ctx, `SELECT id FROM report_items WHERE report_id=$1`, reportID).Scan(&itemID); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := application.persistReportItems(ctx, tx, reportID, userID,
			[]reportItem{{ID: itemID, Category: "개발", Title: title, CurrentResult: "다시 저장", Progress: 55}}); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	call := func(handler http.HandlerFunc, id int64, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/work-items/"+strconv.FormatInt(id, 10), strings.NewReader(body))
		request.SetPathValue("id", strconv.FormatInt(id, 10))
		request = request.WithContext(context.WithValue(request.Context(), principalContext,
			&principal{ID: userID, Username: "split-test", Role: "USER"}))
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}

	original, _ := workItemOf(reportIDs[0])
	for _, reportID := range reportIDs[1:] {
		if got, _ := workItemOf(reportID); got != original {
			t.Fatalf("the three weeks did not start as one task: %d vs %d", got, original)
		}
	}

	// Splitting every week would be a rename; it has to be refused.
	var allItems []string
	for _, reportID := range reportIDs {
		var itemID int64
		if err := db.QueryRow(ctx, `SELECT id FROM report_items WHERE report_id=$1`, reportID).Scan(&itemID); err != nil {
			t.Fatal(err)
		}
		allItems = append(allItems, strconv.FormatInt(itemID, 10))
	}
	if response := call(application.splitWorkItem, original, `{"title":"전부 이동","reportItemIds":[`+strings.Join(allItems, ",")+`]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("splitting every week should be refused, got %d %s", response.Code, response.Body.String())
	}

	var lastItemID int64
	if err := db.QueryRow(ctx, `SELECT id FROM report_items WHERE report_id=$1`, reportIDs[2]).Scan(&lastItemID); err != nil {
		t.Fatal(err)
	}
	response := call(application.splitWorkItem, original, `{"title":"통합 인증 2단계","category":"개발","reportItemIds":[`+strconv.FormatInt(lastItemID, 10)+`]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("split: %d %s", response.Code, response.Body.String())
	}
	split, pinned := workItemOf(reportIDs[2])
	if split == original || !pinned {
		t.Fatalf("split did not move the week: id=%d original=%d pinned=%v", split, original, pinned)
	}

	// The whole point: saving the same report again must not undo it.
	resave(reportIDs[2], "통합 인증 개선")
	if after, stillPinned := workItemOf(reportIDs[2]); after != split || !stillPinned {
		t.Fatalf("the next save undid the split: id=%d want=%d pinned=%v", after, split, stillPinned)
	}
	// Renaming is a statement about what the task is, so the pin lets go.
	resave(reportIDs[2], "완전히 다른 업무")
	if after, stillPinned := workItemOf(reportIDs[2]); after == split || stillPinned {
		t.Fatalf("renaming did not release the pin: id=%d split=%d pinned=%v", after, split, stillPinned)
	}
	resave(reportIDs[2], "통합 인증 개선")

	// Merge the second week's task away and check the old title now resolves to
	// the target rather than recreating the source.
	response = call(application.mergeWorkItem, split, `{"intoId":`+strconv.FormatInt(original, 10)+`}`)
	if response.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", response.Code, response.Body.String())
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveWorkItem(ctx, tx, userID, "통합 인증 2단계", "개발")
	if err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if resolved == nil || *resolved != original {
		t.Fatalf("the merged-away title did not resolve to the target: got=%v want=%d", resolved, original)
	}
}
