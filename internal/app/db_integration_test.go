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
	// sourceSlides travels back with the confirmation so the evidence survives
	// the edit. Before v0.44 the analysis knew which slides an item came from
	// and the confirmation dropped them, so a finished report could never say
	// where its lines came from.
	confirmBody := `{"files":[{"id":` + strconv.FormatInt(importFileID, 10) + `,"selected":true,"weekStart":"2026-08-17","summary":"Import 계약 검증","strategy":"CREATE","items":[{"category":"개발","title":"Import 확정","currentResult":"계약 수정 완료, 회귀 검증 완료.","nextPlan":"운영 확인","issue":"","progress":90,"confidence":0.9,"sourceSlides":[3,4]}]}]}`
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

	// The evidence is on the item, not only on the report. source_type says the
	// report came from a PPTX; this says which file and which slides this
	// particular line rests on.
	var sourceKind, sourceTitle, sourceDetail, sourceReference string
	if err := db.QueryRow(ctx, `SELECT s.kind, s.title, s.detail, s.reference
		FROM report_item_sources s JOIN report_items i ON i.id = s.report_item_id
		JOIN weekly_reports r ON r.id = i.report_id
		WHERE r.user_id=$1 AND r.week_start='2026-08-17'`, userID).
		Scan(&sourceKind, &sourceTitle, &sourceDetail, &sourceReference); err != nil {
		t.Fatalf("the imported item kept no evidence: %v", err)
	}
	if sourceKind != "PPTX" || sourceTitle != "legacy-confirm.pptx" ||
		sourceDetail != "슬라이드 3, 4" || sourceReference != strconv.FormatInt(importFileID, 10) {
		t.Fatalf("unexpected evidence: kind=%q title=%q detail=%q reference=%q",
			sourceKind, sourceTitle, sourceDetail, sourceReference)
	}

	// A Confluence draft's pages must reach the line it becomes. The candidate
	// carried them in candidate_sources and accepting it never copied them
	// across, so the finished report could not say which page a sentence rested
	// on. The copy happens at save time, because that is when the item exists.
	var pageDBID int64
	if err := db.QueryRow(ctx, `INSERT INTO confluence_pages(page_id,space_key,title,title_hash,page_url,page_version,updated_at_source)
		VALUES('PAGE-EV','TEAM','설계 검토 회의록',md5('설계 검토 회의록')||md5('salt'),'https://wiki.example/PAGE-EV',7, now())
		ON CONFLICT (page_id) DO UPDATE SET title=EXCLUDED.title RETURNING id`).Scan(&pageDBID); err != nil {
		t.Fatal(err)
	}
	var candidateID int64
	if err := db.QueryRow(ctx, `INSERT INTO report_candidates(user_id,week_start,normalized_title,category,current_result,confidence,rule_score,status)
		VALUES($1,'2026-08-24','설계 검토','개발','초안 검토 완료',0.8,10,'DETECTED') RETURNING id`, userID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO candidate_sources(candidate_id,confluence_page_id,page_version,activity_type,source_updated_at)
		VALUES($1,$2,7,'CREATED_AND_MODIFIED', now()) ON CONFLICT DO NOTHING`, candidateID, pageDBID); err != nil {
		t.Fatal(err)
	}
	var evidenceReport int64
	if err := db.QueryRow(ctx, `INSERT INTO weekly_reports(user_id,week_start,summary) VALUES($1,'2026-08-24','근거 계보 검증') RETURNING id`,
		userID).Scan(&evidenceReport); err != nil {
		t.Fatal(err)
	}
	evidenceTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.persistReportItems(ctx, evidenceTx, evidenceReport, userID,
		[]reportItem{{Category: "개발", Title: "설계 검토", CurrentResult: "초안 검토 완료", CandidateID: &candidateID}}); err != nil {
		evidenceTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := evidenceTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var evKind, evTitle, evDetail, evReference, evURL string
	if err := db.QueryRow(ctx, `SELECT s.kind, s.title, s.detail, s.reference, s.url
		FROM report_item_sources s JOIN report_items i ON i.id = s.report_item_id
		WHERE i.report_id = $1`, evidenceReport).Scan(&evKind, &evTitle, &evDetail, &evReference, &evURL); err != nil {
		t.Fatalf("the accepted candidate left no evidence on the item: %v", err)
	}
	if evKind != "CONFLUENCE" || evTitle != "설계 검토 회의록" || evReference != "PAGE-EV" ||
		evDetail != "v7 · 작성 후 수정" || evURL != "https://wiki.example/PAGE-EV" {
		t.Fatalf("unexpected Confluence evidence: kind=%q title=%q detail=%q reference=%q url=%q",
			evKind, evTitle, evDetail, evReference, evURL)
	}

	// The organisation subtree branch of canViewPerson, against a real tree. A
	// leader may open the people under them and nobody else, and this must agree
	// exactly with what /team/members offers — a picker that lists someone the
	// handover screen then refuses is a picker that lies.
	var leadOrg, otherOrg int64
	if err := db.QueryRow(ctx, `INSERT INTO organizations(name,code) VALUES('스코프 본부','SCOPE-LEAD')
		ON CONFLICT (code) DO UPDATE SET name=EXCLUDED.name RETURNING id`).Scan(&leadOrg); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO organizations(name,code) VALUES('남의 본부','SCOPE-OTHER')
		ON CONFLICT (code) DO UPDATE SET name=EXCLUDED.name RETURNING id`).Scan(&otherOrg); err != nil {
		t.Fatal(err)
	}
	var childOrg int64
	if err := db.QueryRow(ctx, `INSERT INTO organizations(name,code,parent_id) VALUES('스코프 하위팀','SCOPE-CHILD',$1)
		ON CONFLICT (code) DO UPDATE SET parent_id=EXCLUDED.parent_id RETURNING id`, leadOrg).Scan(&childOrg); err != nil {
		t.Fatal(err)
	}
	person := func(username string, org int64) int64 {
		var id int64
		if err := db.QueryRow(ctx, `INSERT INTO users(username,display_name,role,organization_id) VALUES($1,$1,'USER',$2)
			ON CONFLICT (username) DO UPDATE SET organization_id=EXCLUDED.organization_id RETURNING id`, username, org).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sameTeam := person("scope-same", leadOrg)
	belowTeam := person("scope-below", childOrg)
	elsewhere := person("scope-other", otherOrg)
	leader := &principal{ID: userID, Username: "clone-test", Role: "TEAM_LEADER", OrganizationID: &leadOrg}
	for _, visibility := range []struct {
		target int64
		want   bool
		note   string
	}{
		{sameTeam, true, "같은 조직"},
		{belowTeam, true, "하위 조직"},
		{elsewhere, false, "다른 조직"},
	} {
		got, err := application.canViewPerson(ctx, leader, visibility.target)
		if err != nil {
			t.Fatalf("canViewPerson(%s): %v", visibility.note, err)
		}
		if got != visibility.want {
			t.Fatalf("canViewPerson(%s) = %v, want %v", visibility.note, got, visibility.want)
		}
	}

	// A report with no work items must not be submittable. The editor has always
	// refused to save one, but that rule lived in the browser: POST
	// /api/v1/reports with an empty body created one and it went through
	// submission, review and approval like any other. An empty submission is the
	// one that tells a leader the week is finished.
	var emptyID int64
	if err := db.QueryRow(ctx, `INSERT INTO weekly_reports(user_id,week_start,summary,status) VALUES($1,'2026-09-07','','DRAFT') RETURNING id`, userID).Scan(&emptyID); err != nil {
		t.Fatal(err)
	}
	submit := func(reportID int64) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/reports/"+strconv.FormatInt(reportID, 10)+"/submit", nil)
		request.SetPathValue("id", strconv.FormatInt(reportID, 10))
		request = request.WithContext(context.WithValue(request.Context(), principalContext, &principal{ID: userID, Username: "clone-test", Role: "USER"}))
		recorder := httptest.NewRecorder()
		application.submitReport(recorder, request)
		return recorder
	}
	if response := submit(emptyID); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "EMPTY_REPORT") {
		t.Fatalf("submitting an empty report: status=%d body=%s", response.Code, response.Body.String())
	}
	var stillDraft string
	if err := db.QueryRow(ctx, `SELECT status FROM weekly_reports WHERE id=$1`, emptyID).Scan(&stillDraft); err != nil || stillDraft != "DRAFT" {
		t.Fatalf("a refused submission must not move the report: status=%q err=%v", stillDraft, err)
	}
	// One item is enough to make it a report.
	if _, err := db.Exec(ctx, `INSERT INTO report_items(report_id,category,title,current_result,next_plan,issue,progress) VALUES($1,'개발','제출 검증','진행','계속','',30)`, emptyID); err != nil {
		t.Fatal(err)
	}
	if response := submit(emptyID); response.Code != http.StatusOK {
		t.Fatalf("submitting a report with one item: status=%d body=%s", response.Code, response.Body.String())
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
