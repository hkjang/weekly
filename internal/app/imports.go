package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const importDirectory = stateDirectory + "/imports"

type importJobView struct {
	ID             int64            `json:"id"`
	Status         string           `json:"status"`
	TotalFiles     int              `json:"totalFiles"`
	ProcessedFiles int              `json:"processedFiles"`
	FailedFiles    int              `json:"failedFiles"`
	CreatedAt      time.Time        `json:"createdAt"`
	StartedAt      *time.Time       `json:"startedAt"`
	CompletedAt    *time.Time       `json:"completedAt"`
	ConfirmedAt    *time.Time       `json:"confirmedAt"`
	Files          []importFileView `json:"files,omitempty"`
}

type importFileView struct {
	ID                  int64           `json:"id"`
	OriginalFilename    string          `json:"originalFilename"`
	FileHash            string          `json:"fileHash"`
	SizeBytes           int64           `json:"sizeBytes"`
	Status              string          `json:"status"`
	DetectedWeekStart   string          `json:"detectedWeekStart"`
	DetectedWeekEnd     string          `json:"detectedWeekEnd"`
	Confidence          float64         `json:"confidence"`
	DateSource          string          `json:"dateSource"`
	Result              *aiWeeklyResult `json:"result,omitempty"`
	ErrorMessage        string          `json:"errorMessage,omitempty"`
	DuplicateOf         *int64          `json:"duplicateOf,omitempty"`
	ReportID            *int64          `json:"reportId,omitempty"`
	ConflictReportID    *int64          `json:"conflictReportId,omitempty"`
	ConflictReportState string          `json:"conflictReportStatus,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	AnalyzedAt          *time.Time      `json:"analyzedAt"`
	ConfirmedAt         *time.Time      `json:"confirmedAt"`
}

func (a *App) uploadImportPPTX(w http.ResponseWriter, r *http.Request) {
	if _, err := a.aiConfig(r.Context(), true); err != nil {
		writeError(w, http.StatusServiceUnavailable, "AI_UNAVAILABLE", "관리자가 AI Gateway를 설정하고 활성화해야 합니다.")
		return
	}
	p := currentPrincipal(r.Context())
	maximumFiles := a.settingInt(r.Context(), "import.max_files", 20)
	maximumFileBytes := int64(a.settingInt(r.Context(), "import.max_file_mb", 25)) << 20
	maximumRequest := maximumFileBytes*int64(maximumFiles) + 4<<20
	if maximumRequest > 500<<20 {
		maximumRequest = 500 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximumRequest)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_IMPORT_UPLOAD", "PPTX 업로드 크기 또는 형식이 올바르지 않습니다.")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	headers := r.MultipartForm.File["files"]
	if len(headers) == 0 {
		headers = r.MultipartForm.File["file"]
	}
	if len(headers) == 0 || len(headers) > maximumFiles {
		writeError(w, http.StatusBadRequest, "IMPORT_FILES_REQUIRED", fmt.Sprintf("PPTX 파일을 1~%d개 선택하세요.", maximumFiles))
		return
	}
	var jobID int64
	if err := a.db.QueryRow(r.Context(), `INSERT INTO import_jobs(user_id,total_files) VALUES($1,$2) RETURNING id`, p.ID, len(headers)).Scan(&jobID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 작업을 만들 수 없습니다.")
		return
	}
	jobDirectory := filepath.Join(importDirectory, strconv.FormatInt(jobID, 10))
	if err := os.MkdirAll(jobDirectory, 0o700); err != nil {
		_, _ = a.db.Exec(r.Context(), `UPDATE import_jobs SET status='FAILED',failed_files=total_files,completed_at=now() WHERE id=$1`, jobID)
		writeError(w, http.StatusInternalServerError, "IMPORT_STORAGE_ERROR", "Import 저장소를 만들 수 없습니다.")
		return
	}
	queued := 0
	failed := 0
	for _, header := range headers {
		name := trimRunes(filepath.Base(strings.TrimSpace(header.Filename)), 255)
		if name == "" {
			name = "unnamed.pptx"
		}
		if !strings.EqualFold(filepath.Ext(name), ".pptx") {
			a.insertFailedImportFile(r.Context(), jobID, name, header.Size, "PPTX 파일만 업로드할 수 있습니다.")
			failed++
			continue
		}
		file, err := header.Open()
		if err != nil {
			a.insertFailedImportFile(r.Context(), jobID, name, header.Size, "업로드 파일을 열 수 없습니다.")
			failed++
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(file, maximumFileBytes+1))
		file.Close()
		if readErr != nil || int64(len(body)) == 0 || int64(len(body)) > maximumFileBytes {
			a.insertFailedImportFile(r.Context(), jobID, name, int64(len(body)), "파일이 비어 있거나 관리자 크기 제한을 초과했습니다.")
			failed++
			continue
		}
		sum := sha256.Sum256(body)
		hash := fmt.Sprintf("%x", sum)
		var duplicateID int64
		duplicateErr := a.db.QueryRow(r.Context(), `SELECT f.id FROM import_files f JOIN import_jobs j ON j.id=f.import_job_id
			WHERE j.user_id=$1 AND f.file_hash=$2 AND f.status NOT IN ('FAILED','SKIPPED') ORDER BY f.id LIMIT 1`, p.ID, hash).Scan(&duplicateID)
		if duplicateErr == nil {
			_, _ = a.db.Exec(r.Context(), `INSERT INTO import_files(import_job_id,original_filename,file_hash,size_bytes,status,duplicate_of,error_message)
				VALUES($1,$2,$3,$4,'DUPLICATE',$5,'동일한 SHA-256 파일이 이미 등록되어 있습니다.')`, jobID, name, hash, len(body), duplicateID)
			continue
		}
		if !errors.Is(duplicateErr, pgx.ErrNoRows) {
			a.insertFailedImportFile(r.Context(), jobID, name, int64(len(body)), "중복 파일을 검사할 수 없습니다.")
			failed++
			continue
		}
		var fileID int64
		if err := a.db.QueryRow(r.Context(), `INSERT INTO import_files(import_job_id,original_filename,file_hash,size_bytes) VALUES($1,$2,$3,$4) RETURNING id`, jobID, name, hash, len(body)).Scan(&fileID); err != nil {
			failed++
			continue
		}
		path := filepath.Join(jobDirectory, strconv.FormatInt(fileID, 10)+".pptx")
		if err := writeImportFile(path, body); err != nil {
			_, _ = a.db.Exec(r.Context(), `UPDATE import_files SET status='FAILED',error_message=$1 WHERE id=$2`, "파일을 안전하게 저장할 수 없습니다.", fileID)
			failed++
			continue
		}
		if _, err := a.db.Exec(r.Context(), `UPDATE import_files SET stored_path=$1 WHERE id=$2`, path, fileID); err != nil {
			_ = os.Remove(path)
			_, _ = a.db.Exec(r.Context(), `UPDATE import_files SET status='FAILED',error_message='Import 원본 경로를 저장할 수 없습니다.' WHERE id=$1`, fileID)
			failed++
			continue
		}
		queued++
	}
	if queued == 0 {
		_, _ = a.db.Exec(r.Context(), `UPDATE import_jobs SET status=CASE WHEN $2 >= total_files THEN 'FAILED' WHEN $2 > 0 THEN 'PARTIAL' ELSE 'READY' END,
			processed_files=total_files,failed_files=$2,completed_at=now() WHERE id=$1`, jobID, failed)
	} else {
		_, _ = a.db.Exec(r.Context(), `UPDATE import_jobs SET failed_files=$2 WHERE id=$1`, jobID, failed)
		a.wakeImportWorker()
	}
	a.audit(r, p, "import.upload", "import_job", strconv.FormatInt(jobID, 10), map[string]any{"files": len(headers), "queued": queued, "failed": failed})
	writeData(w, http.StatusAccepted, map[string]any{"id": jobID, "status": map[bool]string{true: "PENDING", false: "READY"}[queued > 0]})
}

func (a *App) insertFailedImportFile(ctx context.Context, jobID int64, name string, size int64, message string) {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%d", jobID, name, time.Now().UnixNano())))
	_, _ = a.db.Exec(ctx, `INSERT INTO import_files(import_job_id,original_filename,file_hash,size_bytes,status,error_message)
		VALUES($1,$2,$3,$4,'FAILED',$5)`, jobID, name, fmt.Sprintf("%x", hash), size, message)
}

func writeImportFile(path string, body []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "upload-*.pptx")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(body)
	}
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporaryPath, path)
}

func (a *App) listImportJobs(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	rows, err := a.db.Query(r.Context(), `SELECT id,status,total_files,processed_files,failed_files,created_at,started_at,completed_at,confirmed_at
		FROM import_jobs WHERE user_id=$1 ORDER BY created_at DESC LIMIT 100`, p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Import 이력을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := []importJobView{}
	for rows.Next() {
		var item importJobView
		if err := rows.Scan(&item.ID, &item.Status, &item.TotalFiles, &item.ProcessedFiles, &item.FailedFiles, &item.CreatedAt, &item.StartedAt, &item.CompletedAt, &item.ConfirmedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Import 이력을 조회할 수 없습니다.")
			return
		}
		result = append(result, item)
	}
	writeData(w, http.StatusOK, result)
}

func (a *App) getImportJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := a.loadImportJob(r.Context(), currentPrincipal(r.Context()).ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "IMPORT_NOT_FOUND", "Import 작업을 찾을 수 없습니다.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "Import 작업을 조회할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, result)
}

func (a *App) loadImportJob(ctx context.Context, userID, jobID int64) (importJobView, error) {
	var result importJobView
	err := a.db.QueryRow(ctx, `SELECT id,status,total_files,processed_files,failed_files,created_at,started_at,completed_at,confirmed_at
		FROM import_jobs WHERE id=$1 AND user_id=$2`, jobID, userID).
		Scan(&result.ID, &result.Status, &result.TotalFiles, &result.ProcessedFiles, &result.FailedFiles, &result.CreatedAt, &result.StartedAt, &result.CompletedAt, &result.ConfirmedAt)
	if err != nil {
		return result, err
	}
	rows, err := a.db.Query(ctx, `SELECT f.id,f.original_filename,f.file_hash,f.size_bytes,f.status,f.detected_week_start,f.detected_week_end,
		coalesce(f.confidence,0),coalesce(f.date_source,''),f.parsed_result,f.error_message,f.duplicate_of,f.report_id,f.created_at,f.analyzed_at,f.confirmed_at,
		r.id,coalesce(r.status,'')
		FROM import_files f JOIN import_jobs j ON j.id=f.import_job_id
		LEFT JOIN weekly_reports r ON r.user_id=j.user_id AND r.week_start=f.detected_week_start
			AND (f.report_id IS NULL OR r.id<>f.report_id)
		WHERE f.import_job_id=$1 ORDER BY f.id`, jobID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.Files = []importFileView{}
	for rows.Next() {
		var item importFileView
		var weekStart, weekEnd *time.Time
		var parsed []byte
		if err := rows.Scan(&item.ID, &item.OriginalFilename, &item.FileHash, &item.SizeBytes, &item.Status, &weekStart, &weekEnd,
			&item.Confidence, &item.DateSource, &parsed, &item.ErrorMessage, &item.DuplicateOf, &item.ReportID, &item.CreatedAt, &item.AnalyzedAt, &item.ConfirmedAt,
			&item.ConflictReportID, &item.ConflictReportState); err != nil {
			return result, err
		}
		if weekStart != nil {
			item.DetectedWeekStart = weekStart.Format("2006-01-02")
		}
		if weekEnd != nil {
			item.DetectedWeekEnd = weekEnd.Format("2006-01-02")
		}
		if len(parsed) > 2 && string(parsed) != "{}" {
			var value aiWeeklyResult
			if json.Unmarshal(parsed, &value) == nil {
				item.Result = &value
			}
		}
		result.Files = append(result.Files, item)
	}
	return result, rows.Err()
}

func (a *App) retryImportJob(w http.ResponseWriter, r *http.Request) {
	if _, err := a.aiConfig(r.Context(), true); err != nil {
		writeError(w, http.StatusServiceUnavailable, "AI_UNAVAILABLE", "관리자가 AI Gateway를 설정하고 활성화해야 합니다.")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 재분석을 시작할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	command, err := tx.Exec(r.Context(), `UPDATE import_files f SET status='QUEUED',error_message='',analyzed_at=NULL
		FROM import_jobs j WHERE f.import_job_id=j.id AND j.id=$1 AND j.user_id=$2 AND f.status='FAILED' AND f.stored_path IS NOT NULL`, id, p.ID)
	if err != nil || command.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "NO_RETRYABLE_IMPORT", "재분석할 수 있는 파일이 없습니다.")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE import_jobs SET status='PENDING',processed_files=0,failed_files=0,started_at=NULL,completed_at=NULL WHERE id=$1`, id)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 재분석을 시작할 수 없습니다.")
		return
	}
	a.wakeImportWorker()
	a.audit(r, p, "import.retry", "import_job", strconv.FormatInt(id, 10), nil)
	writeData(w, http.StatusAccepted, map[string]any{"id": id, "status": "PENDING"})
}

type confirmImportItem struct {
	Category      string  `json:"category"`
	Title         string  `json:"title"`
	CurrentResult string  `json:"currentResult"`
	NextPlan      string  `json:"nextPlan"`
	Issue         string  `json:"issue"`
	Progress      int     `json:"progress"`
	Confidence    float64 `json:"confidence,omitempty"`
}

type confirmImportFile struct {
	ID        int64               `json:"id"`
	Selected  bool                `json:"selected,omitempty"`
	WeekStart string              `json:"weekStart"`
	Summary   string              `json:"summary"`
	Strategy  string              `json:"strategy"`
	Items     []confirmImportItem `json:"items"`
}

func (a *App) confirmImportJob(w http.ResponseWriter, r *http.Request) {
	jobID, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Files []confirmImportFile `json:"files"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Files) == 0 || len(input.Files) > 100 {
		writeError(w, http.StatusBadRequest, "IMPORT_SELECTION_REQUIRED", "확정할 Import 파일을 선택하세요.")
		return
	}
	p := currentPrincipal(r.Context())
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 데이터를 저장할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var owns bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM import_jobs WHERE id=$1 AND user_id=$2 AND status IN ('READY','PARTIAL'))`, jobID, p.ID).Scan(&owns); err != nil || !owns {
		writeError(w, http.StatusConflict, "IMPORT_NOT_READY", "확정할 수 있는 Import 작업이 아닙니다.")
		return
	}
	confirmed := 0
	skipped := 0
	for _, file := range input.Files {
		file.Strategy = strings.ToUpper(strings.TrimSpace(file.Strategy))
		if file.Strategy == "" {
			file.Strategy = "CREATE"
		}
		if file.Strategy != "CREATE" && file.Strategy != "REPLACE" && file.Strategy != "MERGE" && file.Strategy != "SKIP" {
			writeError(w, http.StatusBadRequest, "INVALID_IMPORT_STRATEGY", "Import 중복 처리 방식이 올바르지 않습니다.")
			return
		}
		var status string
		if err := tx.QueryRow(r.Context(), `SELECT f.status FROM import_files f JOIN import_jobs j ON j.id=f.import_job_id
			WHERE f.id=$1 AND f.import_job_id=$2 AND j.user_id=$3 FOR UPDATE`, file.ID, jobID, p.ID).Scan(&status); err != nil || (status != "READY" && status != "NEEDS_REVIEW") {
			writeError(w, http.StatusConflict, "IMPORT_FILE_NOT_READY", "선택한 파일은 확정할 수 있는 상태가 아닙니다.")
			return
		}
		if file.Strategy == "SKIP" {
			_, err = tx.Exec(r.Context(), `UPDATE import_files SET status='SKIPPED',confirmed_at=now() WHERE id=$1`, file.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 건너뛰기를 저장할 수 없습니다.")
				return
			}
			skipped++
			continue
		}
		week, parseErr := time.Parse("2006-01-02", file.WeekStart)
		items := make([]reportItem, 0, len(file.Items))
		for index, item := range file.Items {
			items = append(items, reportItem{Category: item.Category, Title: item.Title, CurrentResult: item.CurrentResult, NextPlan: item.NextPlan, Issue: item.Issue, Progress: item.Progress, SortOrder: index})
		}
		if parseErr != nil || len(items) == 0 || validateItems(items) != nil {
			writeError(w, http.StatusBadRequest, "INVALID_IMPORT_DATA", "주차와 업무 항목을 확인하세요.")
			return
		}
		var reportID int64
		var existingStatus, existingSummary string
		existingErr := tx.QueryRow(r.Context(), `SELECT id,status,summary FROM weekly_reports WHERE user_id=$1 AND week_start=$2 FOR UPDATE`, p.ID, week).
			Scan(&reportID, &existingStatus, &existingSummary)
		if existingErr == nil && file.Strategy == "CREATE" {
			writeError(w, http.StatusConflict, "REPORT_EXISTS", file.WeekStart+" 주차 보고서가 이미 있습니다. 병합·교체·건너뛰기를 선택하세요.")
			return
		}
		if errors.Is(existingErr, pgx.ErrNoRows) {
			err = tx.QueryRow(r.Context(), `INSERT INTO weekly_reports(user_id,week_start,status,summary,source_type,source_ref)
				VALUES($1,$2,'CLOSED',$3,'PPTX_IMPORT',$4) RETURNING id`, p.ID, week, trimRunes(file.Summary, 10000), strconv.FormatInt(file.ID, 10)).Scan(&reportID)
			if err == nil {
				err = insertImportedItems(r.Context(), tx, reportID, items, 0)
			}
			if err == nil {
				_, err = tx.Exec(r.Context(), `INSERT INTO report_status_history(report_id,actor_id,to_status,comment) VALUES($1,$2,'CLOSED','PPTX 과거 보고 Import')`, reportID, p.ID)
			}
		} else if existingErr != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "기존 보고서를 확인할 수 없습니다.")
			return
		} else if file.Strategy == "REPLACE" {
			_, err = tx.Exec(r.Context(), `UPDATE weekly_reports SET summary=$1,status='CLOSED',source_type='PPTX_IMPORT',source_ref=$2,version=version+1,updated_at=now() WHERE id=$3 AND user_id=$4`, trimRunes(file.Summary, 10000), strconv.FormatInt(file.ID, 10), reportID, p.ID)
			if err == nil {
				_, err = tx.Exec(r.Context(), `DELETE FROM report_items WHERE report_id=$1`, reportID)
			}
			if err == nil {
				err = insertImportedItems(r.Context(), tx, reportID, items, 0)
			}
		} else {
			err = mergeImportedItems(r.Context(), tx, reportID, items)
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE weekly_reports SET summary=$1,status='CLOSED',source_type='PPTX_IMPORT',source_ref=$2,version=version+1,updated_at=now() WHERE id=$3`,
					trimRunes(mergeText(existingSummary, file.Summary), 10000), strconv.FormatInt(file.ID, 10), reportID)
			}
		}
		if err == nil && existingErr == nil && existingStatus != "CLOSED" {
			_, err = tx.Exec(r.Context(), `INSERT INTO report_status_history(report_id,actor_id,from_status,to_status,comment)
				VALUES($1,$2,$3,'CLOSED',$4)`, reportID, p.ID, existingStatus, "PPTX 과거 보고 Import "+file.Strategy)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 보고서를 저장할 수 없습니다.")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE import_files SET status='CONFIRMED',detected_week_start=$1,detected_week_end=$2,report_id=$3,confirmed_at=now() WHERE id=$4`, week, week.AddDate(0, 0, 6), reportID, file.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 연결 이력을 저장할 수 없습니다.")
			return
		}
		confirmed++
	}
	var remaining int
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM import_files WHERE import_job_id=$1 AND status IN ('READY','NEEDS_REVIEW')`, jobID).Scan(&remaining); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 상태를 확인할 수 없습니다.")
		return
	}
	if remaining == 0 {
		_, err = tx.Exec(r.Context(), `UPDATE import_jobs SET status='CONFIRMED',confirmed_at=now() WHERE id=$1`, jobID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Import 확정을 완료할 수 없습니다.")
		return
	}
	a.audit(r, p, "import.confirm", "import_job", strconv.FormatInt(jobID, 10), map[string]any{"confirmed": confirmed, "skipped": skipped})
	writeData(w, http.StatusOK, map[string]any{"id": jobID, "confirmed": confirmed, "skipped": skipped, "remaining": remaining})
}

func insertImportedItems(ctx context.Context, tx pgx.Tx, reportID int64, items []reportItem, offset int) error {
	for index, item := range items {
		_, err := tx.Exec(ctx, `INSERT INTO report_items(report_id,category,title,current_result,next_plan,issue,progress,sort_order)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, reportID, trimRunes(strings.TrimSpace(item.Category), 80), trimRunes(strings.TrimSpace(item.Title), 240),
			trimRunes(strings.TrimSpace(item.CurrentResult), 20000), trimRunes(strings.TrimSpace(item.NextPlan), 20000), trimRunes(strings.TrimSpace(item.Issue), 20000), item.Progress, offset+index)
		if err != nil {
			return err
		}
	}
	return nil
}

func mergeImportedItems(ctx context.Context, tx pgx.Tx, reportID int64, items []reportItem) error {
	var sortOrder int
	_ = tx.QueryRow(ctx, `SELECT coalesce(max(sort_order),-1)+1 FROM report_items WHERE report_id=$1`, reportID).Scan(&sortOrder)
	for _, item := range items {
		var id int64
		var current, next, issue string
		var progress int
		err := tx.QueryRow(ctx, `SELECT id,current_result,next_plan,issue,progress FROM report_items
			WHERE report_id=$1 AND lower(trim(category))=lower(trim($2)) AND lower(trim(title))=lower(trim($3)) ORDER BY id LIMIT 1`, reportID, item.Category, item.Title).
			Scan(&id, &current, &next, &issue, &progress)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := insertImportedItems(ctx, tx, reportID, []reportItem{item}, sortOrder); err != nil {
				return err
			}
			sortOrder++
			continue
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE report_items SET current_result=$1,next_plan=$2,issue=$3,progress=$4,updated_at=now() WHERE id=$5`,
			mergeText(current, item.CurrentResult), mergeText(next, item.NextPlan), mergeText(issue, item.Issue), maximum(progress, item.Progress), id)
		if err != nil {
			return err
		}
	}
	return nil
}

func mergeText(existing, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	if incoming == "" || strings.Contains(existing, incoming) {
		return existing
	}
	if existing == "" {
		return incoming
	}
	return existing + "\n" + incoming
}

func maximum(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func (a *App) wakeImportWorker() {
	select {
	case a.importWake <- struct{}{}:
	default:
	}
}

func (a *App) importWorker(ctx context.Context) {
	_, _ = a.db.Exec(ctx, `UPDATE import_jobs SET status='PENDING',started_at=NULL WHERE status='PROCESSING'; UPDATE import_files SET status='QUEUED' WHERE status='PROCESSING'`)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	a.wakeImportWorker()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.importWake:
		case <-ticker.C:
		}
		for a.processNextImportJob(ctx) {
		}
	}
}

func (a *App) processNextImportJob(ctx context.Context) bool {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)
	var jobID int64
	err = tx.QueryRow(ctx, `SELECT id FROM import_jobs WHERE status='PENDING' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		return false
	}
	if _, err = tx.Exec(ctx, `UPDATE import_jobs SET status='PROCESSING',started_at=coalesce(started_at,now()) WHERE id=$1`, jobID); err != nil || tx.Commit(ctx) != nil {
		return false
	}
	a.processImportJob(ctx, jobID)
	return true
}

func (a *App) processImportJob(ctx context.Context, jobID int64) {
	rows, err := a.db.Query(ctx, `SELECT id,original_filename,stored_path FROM import_files WHERE import_job_id=$1 AND status='QUEUED' ORDER BY id`, jobID)
	if err != nil {
		_, _ = a.db.Exec(ctx, `UPDATE import_jobs SET status='FAILED',completed_at=now() WHERE id=$1`, jobID)
		return
	}
	type pendingFile struct {
		id         int64
		name, path string
	}
	files := []pendingFile{}
	for rows.Next() {
		var id int64
		var name string
		var path *string
		if rows.Scan(&id, &name, &path) == nil && path != nil {
			files = append(files, pendingFile{id: id, name: name, path: *path})
		}
	}
	rows.Close()
	for _, file := range files {
		a.processImportFile(ctx, jobID, file.id, file.name, file.path)
	}
	var processed, failed int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status NOT IN ('QUEUED','PROCESSING')),
		count(*) FILTER (WHERE status='FAILED') FROM import_files WHERE import_job_id=$1`, jobID).Scan(&processed, &failed)
	status := "READY"
	if failed > 0 {
		status = "PARTIAL"
	}
	var usable int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM import_files WHERE import_job_id=$1 AND status IN ('READY','NEEDS_REVIEW')`, jobID).Scan(&usable)
	if usable == 0 && failed > 0 {
		status = "FAILED"
	}
	_, _ = a.db.Exec(ctx, `UPDATE import_jobs SET status=$1,processed_files=$2,failed_files=$3,completed_at=now() WHERE id=$4`, status, processed, failed, jobID)
}

func (a *App) processImportFile(ctx context.Context, jobID, fileID int64, filename, path string) {
	_, _ = a.db.Exec(ctx, `UPDATE import_files SET status='PROCESSING',error_message='' WHERE id=$1 AND status='QUEUED'`, fileID)
	if !safeImportPath(path, jobID) {
		a.failImportFile(ctx, fileID, "저장된 Import 파일 경로가 올바르지 않습니다.")
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		a.failImportFile(ctx, fileID, "저장된 PPTX 파일을 읽을 수 없습니다.")
		return
	}
	cfg, err := a.aiConfig(ctx, true)
	if err != nil {
		a.failImportFile(ctx, fileID, "AI Gateway가 설정되거나 활성화되지 않았습니다.")
		return
	}
	extracted, err := extractPPTX(body, cfg.MaxInput)
	if err != nil {
		a.failImportFile(ctx, fileID, err.Error())
		return
	}
	location := a.serviceLocation(ctx)
	detected := detectPPTXWeek(filename, extracted.Normalized, a.setting(ctx, "workflow.week_start", "MONDAY"), location)
	input := "원본 파일명: " + filename + "\n"
	if !detected.Start.IsZero() {
		input += "결정적 파서 날짜 후보: " + detected.Start.Format("2006-01-02") + " ~ " + detected.End.Format("2006-01-02") + "\n"
	}
	input += extracted.Normalized
	input = trimRunes(input, cfg.MaxInput)
	result, raw, err := callWeeklyAI(ctx, cfg, "PPTX_NORMALIZED", input)
	if err != nil {
		a.failImportFileWithRaw(ctx, fileID, extracted.Normalized, err.Error())
		return
	}
	dateSource := detected.Source
	confidence := detected.Confidence
	weekStart := detected.Start
	weekEnd := detected.End
	if weekStart.IsZero() && result.WeekStart != "" {
		weekStart, _ = time.ParseInLocation("2006-01-02", result.WeekStart, location)
		weekEnd = weekStart.AddDate(0, 0, 6)
		confidence = result.DateConfidence
		dateSource = "ai_inference"
	}
	if !weekStart.IsZero() {
		result.WeekStart = weekStart.Format("2006-01-02")
		result.DateConfidence = confidence
	}
	status := "READY"
	if weekStart.IsZero() || confidence < 0.75 || minimumItemConfidence(result.ReportItems) < 0.6 {
		status = "NEEDS_REVIEW"
		if weekStart.IsZero() {
			result.Warnings = append(result.Warnings, "보고 주차를 확인하지 못했습니다. 확정 전에 날짜를 입력하세요.")
		}
	}
	parsed, _ := json.Marshal(result)
	var startValue, endValue any
	if !weekStart.IsZero() {
		startValue, endValue = weekStart, weekEnd
	}
	_, err = a.db.Exec(ctx, `UPDATE import_files SET status=$1,detected_week_start=$2,detected_week_end=$3,confidence=$4,date_source=$5,
		raw_text=$6,parsed_result=$7,ai_response=$8,error_message='',analyzed_at=now() WHERE id=$9`, status, startValue, endValue, confidence, dateSource,
		trimRunes(extracted.Normalized, 200000), parsed, trimRunes(raw, 200000), fileID)
	if err != nil {
		a.failImportFile(ctx, fileID, "분석 결과를 저장할 수 없습니다.")
	}
}

func (a *App) failImportFile(ctx context.Context, fileID int64, message string) {
	a.failImportFileWithRaw(ctx, fileID, "", message)
}

func (a *App) failImportFileWithRaw(ctx context.Context, fileID int64, rawText, message string) {
	_, _ = a.db.Exec(ctx, `UPDATE import_files SET status='FAILED',raw_text=$1,error_message=$2,analyzed_at=now() WHERE id=$3`,
		trimRunes(rawText, 200000), trimRunes(message, 1000), fileID)
}

func minimumItemConfidence(items []aiReportItem) float64 {
	minimum := 1.0
	for _, item := range items {
		if item.Confidence < minimum {
			minimum = item.Confidence
		}
	}
	return minimum
}

func safeImportPath(path string, jobID int64) bool {
	clean := filepath.Clean(path)
	root := filepath.Join(importDirectory, strconv.FormatInt(jobID, 10)) + string(os.PathSeparator)
	return strings.HasPrefix(clean, root) && strings.EqualFold(filepath.Ext(clean), ".pptx")
}

func (a *App) cleanupImportSources(ctx context.Context) {
	retention := a.settingInt(ctx, "import.retention_days", 365)
	rows, err := a.db.Query(ctx, `SELECT f.id,f.import_job_id,f.stored_path
		FROM import_files f
		WHERE f.stored_path IS NOT NULL
		  AND f.status IN ('CONFIRMED','SKIPPED','FAILED')
		  AND coalesce(f.confirmed_at,f.analyzed_at,f.created_at) < now() - ($1 || ' days')::interval
		ORDER BY f.id LIMIT 500`, retention)
	if err != nil {
		return
	}
	type expiredSource struct {
		id, jobID int64
		path      string
	}
	expired := make([]expiredSource, 0, 500)
	for rows.Next() {
		var item expiredSource
		if rows.Scan(&item.id, &item.jobID, &item.path) == nil {
			expired = append(expired, item)
		}
	}
	rows.Close()
	for _, item := range expired {
		if !safeImportPath(item.path, item.jobID) {
			continue
		}
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		_, _ = a.db.Exec(ctx, `UPDATE import_files SET stored_path=NULL WHERE id=$1 AND stored_path=$2`, item.id, item.path)
	}
}
