package app

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type confluenceCandidateView struct {
	ID              int64                  `json:"id"`
	WeekStart       string                 `json:"weekStart"`
	NormalizedTitle string                 `json:"normalizedTitle"`
	Category        string                 `json:"category"`
	CurrentResult   string                 `json:"currentResult"`
	NextPlan        string                 `json:"nextPlan"`
	Issue           string                 `json:"issue"`
	Confidence      float64                `json:"confidence"`
	RuleScore       int                    `json:"ruleScore"`
	Status          string                 `json:"status"`
	UserEdited      bool                   `json:"userEdited"`
	Sources         []confluenceSourceView `json:"sources"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
}

type confluenceSourceView struct {
	PageID          string     `json:"pageId"`
	Title           string     `json:"title"`
	SpaceKey        string     `json:"spaceKey"`
	PageURL         string     `json:"pageUrl"`
	PageVersion     int        `json:"pageVersion"`
	ActivityType    string     `json:"activityType"`
	SourceUpdatedAt *time.Time `json:"sourceUpdatedAt"`
}

func (a *App) currentConfluenceCandidates(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	cfg, err := a.loadConfluenceSettings(r.Context())
	if err != nil {
		writeError(w, 500, "CONFLUENCE_CONFIGURATION_ERROR", "Confluence 설정을 읽을 수 없습니다.")
		return
	}
	if cfg.Enabled {
		var lastSuccess *time.Time
		_ = a.db.QueryRow(r.Context(), `SELECT last_success_at FROM confluence_sync_state WHERE system_type='CONFLUENCE'`).Scan(&lastSuccess)
		if lastSuccess == nil || time.Since(*lastSuccess) >= time.Duration(cfg.SyncIntervalMinutes)*time.Minute {
			a.wakeConfluenceWorker()
		}
	}
	week := currentWeekStart(time.Now().In(a.serviceLocation(r.Context())), a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format("2006-01-02")
	candidates, err := a.loadCandidateViews(r, p.ID, week, "DETECTED")
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "Confluence 자동 초안을 조회할 수 없습니다.")
		return
	}
	var syncStatus, syncError string
	var lastSuccess, lastAttempt *time.Time
	_ = a.db.QueryRow(r.Context(), `SELECT status,last_success_at,last_attempt_at,error_message FROM confluence_sync_state WHERE system_type='CONFLUENCE'`).Scan(&syncStatus, &lastSuccess, &lastAttempt, &syncError)
	writeData(w, 200, map[string]any{
		"enabled": cfg.Enabled, "weekStart": week, "candidates": candidates,
		"sync": map[string]any{"status": syncStatus, "lastSuccessAt": lastSuccess, "lastAttemptAt": lastAttempt, "errorMessage": syncError},
	})
}

func (a *App) loadCandidateViews(r *http.Request, userID int64, week, status string) ([]confluenceCandidateView, error) {
	rows, err := a.db.Query(r.Context(), `SELECT id,week_start::text,normalized_title,category,current_result,next_plan,issue,confidence,rule_score,status,user_edited,created_at,updated_at
		FROM report_candidates WHERE user_id=$1 AND week_start=$2 AND status=$3 ORDER BY updated_at DESC,id DESC`, userID, week, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]confluenceCandidateView, 0)
	for rows.Next() {
		var item confluenceCandidateView
		if err := rows.Scan(&item.ID, &item.WeekStart, &item.NormalizedTitle, &item.Category, &item.CurrentResult, &item.NextPlan, &item.Issue, &item.Confidence, &item.RuleScore, &item.Status, &item.UserEdited, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Sources, err = a.candidateSourceViews(r, item.ID, userID)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (a *App) candidateSourceViews(r *http.Request, candidateID, userID int64) ([]confluenceSourceView, error) {
	rows, err := a.db.Query(r.Context(), `SELECT p.page_id,p.title,p.space_key,p.page_url,cs.page_version,cs.activity_type,cs.source_updated_at
		FROM candidate_sources cs JOIN confluence_pages p ON p.id=cs.confluence_page_id JOIN report_candidates c ON c.id=cs.candidate_id
		WHERE cs.candidate_id=$1 AND c.user_id=$2 ORDER BY p.updated_at_source DESC NULLS LAST,p.id`, candidateID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]confluenceSourceView, 0)
	for rows.Next() {
		var item confluenceSourceView
		if err := rows.Scan(&item.PageID, &item.Title, &item.SpaceKey, &item.PageURL, &item.PageVersion, &item.ActivityType, &item.SourceUpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (a *App) updateConfluenceCandidate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		NormalizedTitle string `json:"normalizedTitle"`
		Category        string `json:"category"`
		CurrentResult   string `json:"currentResult"`
		NextPlan        string `json:"nextPlan"`
		Issue           string `json:"issue"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.NormalizedTitle = strings.TrimSpace(input.NormalizedTitle)
	if input.NormalizedTitle == "" || runeLength(input.NormalizedTitle) > 240 || runeLength(input.Category) > 80 || runeLength(input.CurrentResult)+runeLength(input.NextPlan)+runeLength(input.Issue) > 60000 {
		writeError(w, 400, "INVALID_CANDIDATE", "자동 초안의 제목 또는 내용 길이가 올바르지 않습니다.")
		return
	}
	p := currentPrincipal(r.Context())
	command, err := a.db.Exec(r.Context(), `UPDATE report_candidates SET normalized_title=$3,category=$4,current_result=$5,next_plan=$6,issue=$7,user_edited=true,updated_at=now()
		WHERE id=$1 AND user_id=$2 AND status='DETECTED'`, id, p.ID, input.NormalizedTitle, strings.TrimSpace(input.Category), strings.TrimSpace(input.CurrentResult), strings.TrimSpace(input.NextPlan), strings.TrimSpace(input.Issue))
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "자동 초안을 저장할 수 없습니다.")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, 404, "CANDIDATE_NOT_FOUND", "수정할 자동 초안이 없습니다.")
		return
	}
	a.audit(r, p, "confluence.candidate.update", "report_candidate", strconv.FormatInt(id, 10), nil)
	writeData(w, 200, map[string]any{"id": id, "updated": true})
}

func (a *App) deleteConfluenceCandidate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	command, err := a.db.Exec(r.Context(), `UPDATE report_candidates SET status='IGNORED',user_edited=true,updated_at=now() WHERE id=$1 AND user_id=$2 AND status='DETECTED'`, id, p.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "자동 초안을 제외할 수 없습니다.")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, 404, "CANDIDATE_NOT_FOUND", "제외할 자동 초안이 없습니다.")
		return
	}
	a.audit(r, p, "confluence.candidate.ignore", "report_candidate", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) confluenceCandidateSources(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sources, err := a.candidateSourceViews(r, id, currentPrincipal(r.Context()).ID)
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "Confluence 출처를 조회할 수 없습니다.")
		return
	}
	if len(sources) == 0 {
		writeError(w, 404, "CANDIDATE_NOT_FOUND", "자동 초안 또는 출처가 없습니다.")
		return
	}
	writeData(w, 200, sources)
}

func (a *App) acceptConfluenceCandidates(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs      []int64 `json:"ids"`
		ReportID int64   `json:"reportId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ReportID <= 0 || len(input.IDs) == 0 || len(input.IDs) > 100 {
		writeError(w, 400, "INVALID_CANDIDATES", "반영할 자동 초안과 보고서를 확인하세요.")
		return
	}
	seenIDs := map[int64]bool{}
	uniqueIDs := make([]int64, 0, len(input.IDs))
	for _, id := range input.IDs {
		if id <= 0 || seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		writeError(w, 400, "INVALID_CANDIDATES", "반영할 자동 초안을 확인하세요.")
		return
	}
	input.IDs = uniqueIDs
	p := currentPrincipal(r.Context())
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "자동 초안을 반영할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var ownerID int64
	var reportWeek, reportStatus string
	if err := tx.QueryRow(r.Context(), `SELECT user_id,week_start::text,status FROM weekly_reports WHERE id=$1`, input.ReportID).Scan(&ownerID, &reportWeek, &reportStatus); err != nil || ownerID != p.ID {
		writeError(w, 404, "REPORT_NOT_FOUND", "주간보고를 찾을 수 없습니다.")
		return
	}
	if reportStatus != "DRAFT" && reportStatus != "REVISION_REQUESTED" {
		writeError(w, 409, "REPORT_NOT_EDITABLE", "현재 상태의 주간보고에는 자동 초안을 반영할 수 없습니다.")
		return
	}
	command, err := tx.Exec(r.Context(), `UPDATE report_candidates SET status='ACCEPTED',accepted_report_id=$3,updated_at=now()
		WHERE id=ANY($1) AND user_id=$2 AND accepted_report_id IS NULL AND week_start=$4 AND status='DETECTED'`, input.IDs, p.ID, input.ReportID, reportWeek)
	if err != nil || command.RowsAffected() != int64(len(input.IDs)) {
		writeError(w, 409, "CANDIDATE_STATE_CHANGED", "일부 자동 초안의 상태가 변경되었습니다. 새로고침 후 다시 시도하세요.")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE weekly_reports SET source_type='CONFLUENCE_AI',source_ref='report-candidates',updated_at=now() WHERE id=$1`, input.ReportID)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 500, "DATABASE_ERROR", "자동 초안을 반영할 수 없습니다.")
		return
	}
	a.audit(r, p, "confluence.candidate.accept", "weekly_report", strconv.FormatInt(input.ReportID, 10), map[string]any{"candidateIds": input.IDs})
	writeData(w, 200, map[string]any{"accepted": len(input.IDs), "reportId": input.ReportID})
}

func (a *App) adminConfluenceStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.loadConfluenceSettings(r.Context())
	if err != nil {
		writeError(w, 500, "CONFIGURATION_ERROR", "Confluence 설정을 읽을 수 없습니다.")
		return
	}
	var status, errorMessage string
	var lastSuccess, lastAttempt, currentStarted *time.Time
	var scanned, changed, created, failed int
	err = a.db.QueryRow(r.Context(), `SELECT status,last_success_at,last_attempt_at,current_started_at,error_message,pages_scanned,pages_changed,candidates_created,pages_failed
		FROM confluence_sync_state WHERE system_type='CONFLUENCE'`).Scan(&status, &lastSuccess, &lastAttempt, &currentStarted, &errorMessage, &scanned, &changed, &created, &failed)
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "동기화 상태를 조회할 수 없습니다.")
		return
	}
	errorRows, _ := a.db.Query(r.Context(), `SELECT id,page_id,phase,status_code,error_message,created_at FROM confluence_sync_errors ORDER BY created_at DESC LIMIT 20`)
	errorsView := make([]map[string]any, 0)
	if errorRows != nil {
		defer errorRows.Close()
		for errorRows.Next() {
			var id int64
			var pageID *string
			var phase, message string
			var code *int
			var createdAt time.Time
			if errorRows.Scan(&id, &pageID, &phase, &code, &message, &createdAt) == nil {
				errorsView = append(errorsView, map[string]any{"id": id, "pageId": pageID, "phase": phase, "statusCode": code, "message": message, "createdAt": createdAt})
			}
		}
	}
	var mapped, unmapped int
	_ = a.db.QueryRow(r.Context(), `SELECT count(*) FROM user_external_accounts WHERE system_type='CONFLUENCE' AND active=true`).Scan(&mapped)
	_ = a.db.QueryRow(r.Context(), `SELECT count(*) FROM users u WHERE u.active=true AND NOT EXISTS(SELECT 1 FROM user_external_accounts e WHERE e.user_id=u.id AND e.system_type='CONFLUENCE' AND e.active=true)`).Scan(&unmapped)
	writeData(w, 200, map[string]any{
		"enabled": cfg.Enabled, "status": status, "lastSuccessAt": lastSuccess, "lastAttemptAt": lastAttempt, "currentStartedAt": currentStarted,
		"errorMessage": errorMessage, "pagesScanned": scanned, "pagesChanged": changed, "candidatesCreated": created, "pagesFailed": failed,
		"mappedUsers": mapped, "unmappedUsers": unmapped, "recentErrors": errorsView,
	})
}

func (a *App) forceConfluenceSync(w http.ResponseWriter, r *http.Request) {
	if !a.settingBool(r.Context(), "confluence.enabled", false) {
		writeError(w, 409, "CONFLUENCE_DISABLED", "관리자 설정에서 Confluence 자동화를 먼저 활성화하세요.")
		return
	}
	var status string
	_ = a.db.QueryRow(r.Context(), `SELECT status FROM confluence_sync_state WHERE system_type='CONFLUENCE'`).Scan(&status)
	if status == "RUNNING" {
		writeData(w, 202, map[string]any{"queued": false, "status": status})
		return
	}
	a.wakeConfluenceWorker()
	a.audit(r, currentPrincipal(r.Context()), "confluence.sync.request", "confluence", "global", nil)
	writeData(w, 202, map[string]any{"queued": true, "status": "QUEUED"})
}

func (a *App) testConfluence(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.loadConfluenceSettings(r.Context())
	if err != nil {
		writeError(w, 400, "CONFLUENCE_CONFIGURATION_INVALID", "Confluence 설정을 읽을 수 없습니다.")
		return
	}
	client, err := cfg.client()
	if err != nil {
		writeError(w, 400, "CONFLUENCE_CONFIGURATION_INVALID", "Base URL과 인증 설정을 확인하세요.")
		return
	}
	result, err := client.SearchChangedPages(r.Context(), time.Now().Add(-24*time.Hour), 0, 1)
	if err != nil {
		writeError(w, 502, "CONFLUENCE_CONNECTION_FAILED", safeConfluenceError(err))
		return
	}
	writeData(w, 200, map[string]any{"ok": true, "baseUrl": cfg.BaseURL, "sampleCount": result.Size})
}

func (a *App) adminConfluenceMappings(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT u.id,u.username,u.display_name,coalesce(u.email,''),e.external_username,e.mapping_source,e.active
		FROM users u LEFT JOIN user_external_accounts e ON e.user_id=u.id AND e.system_type='CONFLUENCE' WHERE u.active=true ORDER BY u.display_name,u.id`)
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "사용자 매핑을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var username, displayName, email string
		var external, source *string
		var active *bool
		if err := rows.Scan(&id, &username, &displayName, &email, &external, &source, &active); err != nil {
			writeError(w, 500, "QUERY_FAILED", "사용자 매핑을 조회할 수 없습니다.")
			return
		}
		suggested, suggestionSource := confluenceMappingSuggestion(username, email)
		result = append(result, map[string]any{"userId": id, "username": username, "displayName": displayName, "email": email, "externalUsername": external, "mappingSource": source, "active": active, "suggestedUsername": suggested, "suggestionSource": suggestionSource})
	}
	writeData(w, 200, result)
}

func (a *App) adminUnmappedConfluenceUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT u.id,u.username,u.display_name,coalesce(u.email,'') FROM users u WHERE u.active=true AND NOT EXISTS(
		SELECT 1 FROM user_external_accounts e WHERE e.user_id=u.id AND e.system_type='CONFLUENCE' AND e.active=true) ORDER BY u.display_name`)
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "미매핑 사용자를 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var username, name, email string
		if rows.Scan(&id, &username, &name, &email) == nil {
			suggested, source := confluenceMappingSuggestion(username, email)
			result = append(result, map[string]any{"userId": id, "username": username, "displayName": name, "email": email, "suggestedUsername": suggested, "suggestionSource": source})
		}
	}
	writeData(w, 200, result)
}

func confluenceMappingSuggestion(username, email string) (string, string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if at := strings.IndexByte(email, '@'); at > 0 {
		return email[:at], "EMAIL_LOCALPART"
	}
	return strings.ToLower(strings.TrimSpace(username)), "USERNAME"
}

func (a *App) updateConfluenceMapping(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExternalUsername string `json:"externalUsername"`
		Active           bool   `json:"active"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ExternalUsername = strings.TrimSpace(input.ExternalUsername)
	if input.ExternalUsername == "" || len(input.ExternalUsername) > 255 || strings.ContainsAny(input.ExternalUsername, "\r\n\t") {
		writeError(w, 400, "INVALID_EXTERNAL_USERNAME", "Confluence 사용자 아이디가 올바르지 않습니다.")
		return
	}
	var exists bool
	if err := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND active=true)`, userID).Scan(&exists); err != nil || !exists {
		writeError(w, 404, "USER_NOT_FOUND", "사용자를 찾을 수 없습니다.")
		return
	}
	p := currentPrincipal(r.Context())
	_, err := a.db.Exec(r.Context(), `INSERT INTO user_external_accounts(user_id,system_type,external_username,mapping_source,active,updated_by)
		VALUES($1,'CONFLUENCE',$2,'EXPLICIT',$3,$4) ON CONFLICT(user_id,system_type) DO UPDATE SET external_username=EXCLUDED.external_username,
		mapping_source='EXPLICIT',active=EXCLUDED.active,updated_by=EXCLUDED.updated_by,updated_at=now()`, userID, input.ExternalUsername, input.Active, p.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "idx_external_account_system_username") {
			writeError(w, 409, "EXTERNAL_USERNAME_EXISTS", "이미 다른 사용자에게 연결된 Confluence 아이디입니다.")
		} else {
			writeError(w, 500, "DATABASE_ERROR", "사용자 매핑을 저장할 수 없습니다.")
		}
		return
	}
	a.audit(r, p, "confluence.mapping.update", "user", fmt.Sprintf("%d", userID), map[string]any{"externalUsername": input.ExternalUsername, "active": input.Active})
	writeData(w, 200, map[string]any{"userId": userID, "externalUsername": input.ExternalUsername, "active": input.Active})
}
