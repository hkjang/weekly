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

	var version int
	if err := db.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 6 {
		t.Fatalf("migration version: got=%d err=%v", version, err)
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
