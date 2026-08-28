package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Screen captures attached to a report become their own slides in the exported
// deck, either before or after the written content.

// stateDirectoryAttachments is resolved at startup like the other state paths.
var stateDirectoryAttachments = stateDirectory + "/attachments"

const (
	placementBefore = "BEFORE"
	placementAfter  = "AFTER"
)

// imageFormats maps the formats decoded by the standard library to the parts a
// PPTX package needs. Anything else is rejected rather than trusted.
var imageFormats = map[string]struct{ ContentType, Extension string }{
	"png":  {"image/png", "png"},
	"jpeg": {"image/jpeg", "jpeg"},
	"gif":  {"image/gif", "gif"},
}

type attachmentView struct {
	ID        int64     `json:"id"`
	Filename  string    `json:"filename"`
	Caption   string    `json:"caption"`
	Placement string    `json:"placement"`
	SortOrder int       `json:"sortOrder"`
	SizeBytes int64     `json:"sizeBytes"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	CreatedAt time.Time `json:"createdAt"`
	// Available reports whether the stored file is still readable. A row can
	// outlive its file: the images live on the state volume while the rows live
	// in PostgreSQL, so a deployment that upgrades without mounting
	// /var/lib/weekly keeps every row and loses every file. Saying so here lets
	// the screen explain the gap instead of showing a broken image.
	Available bool `json:"available"`
}

type storedAttachment struct {
	attachmentView
	StoredPath  string
	ContentType string
	Extension   string
}

func (a *App) attachmentLimits(ctx context.Context) (maxPerReport int, maxBytes int64) {
	maxPerReport = a.settingInt(ctx, "attachment.max_per_report", 20)
	if maxPerReport < 1 || maxPerReport > 100 {
		maxPerReport = 20
	}
	megabytes := a.settingInt(ctx, "attachment.max_file_mb", 10)
	if megabytes < 1 || megabytes > 50 {
		megabytes = 10
	}
	return maxPerReport, int64(megabytes) << 20
}

// reportOwner returns the owner of a report, or an error when it is missing.
func (a *App) reportOwner(ctx context.Context, id int64) (int64, error) {
	var owner int64
	if err := a.db.QueryRow(ctx, `SELECT user_id FROM weekly_reports WHERE id=$1`, id).Scan(&owner); err != nil {
		return 0, errNotFound
	}
	return owner, nil
}

func (a *App) listAttachments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	if !a.canViewReport(r.Context(), p, id) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "이 보고서의 첨부 이미지를 조회할 권한이 없습니다.")
		return
	}
	items, err := a.loadAttachments(r.Context(), id)
	if err != nil {
		a.logger.Error("list attachments", "error", err, "reportId", id)
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "첨부 이미지를 조회할 수 없습니다.")
		return
	}
	result := make([]attachmentView, 0, len(items))
	for _, item := range items {
		result = append(result, item.attachmentView)
	}
	writeData(w, http.StatusOK, result)
}

func (a *App) loadAttachments(ctx context.Context, reportID int64) ([]storedAttachment, error) {
	rows, err := a.db.Query(ctx, `SELECT id,original_filename,caption,placement,sort_order,size_bytes,width,height,created_at,stored_path,content_type,extension
		FROM report_attachments WHERE report_id=$1
		ORDER BY CASE placement WHEN 'BEFORE' THEN 0 ELSE 1 END, sort_order, id`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []storedAttachment{}
	for rows.Next() {
		var item storedAttachment
		if err := rows.Scan(&item.ID, &item.Filename, &item.Caption, &item.Placement, &item.SortOrder,
			&item.SizeBytes, &item.Width, &item.Height, &item.CreatedAt,
			&item.StoredPath, &item.ContentType, &item.Extension); err != nil {
			return nil, err
		}
		_, statErr := os.Stat(safeAttachmentPath(item.StoredPath))
		item.Available = statErr == nil
		result = append(result, item)
	}
	return result, rows.Err()
}

// checkAttachmentIntegrity reports rows whose image file is gone.
//
// It runs at start-up because the usual cause is a deployment mistake that is
// invisible until someone opens an old report: the state volume was not
// mounted, so the files are gone while every row survived in PostgreSQL. A 404
// on one image is a puzzle; this line in the boot log is an answer.
func (a *App) checkAttachmentIntegrity(ctx context.Context) {
	rows, err := a.db.Query(ctx, `SELECT stored_path FROM report_attachments`)
	if err != nil {
		return
	}
	defer rows.Close()
	total, missing := 0, 0
	for rows.Next() {
		var storedPath string
		if err := rows.Scan(&storedPath); err != nil {
			return
		}
		total++
		if _, statErr := os.Stat(safeAttachmentPath(storedPath)); statErr != nil {
			missing++
		}
	}
	if rows.Err() != nil || total == 0 {
		return
	}
	if missing == 0 {
		a.logger.Info("attachment files verified", "attachments", total)
		return
	}
	a.logger.Warn("attachment files are missing; captures will not display or export",
		"missing", missing, "attachments", total, "directory", stateDirectoryAttachments,
		"hint", "확인: /var/lib/weekly 를 영속 볼륨으로 마운트했는지. 파일이 유실된 첨부는 다시 업로드해야 합니다.")
}

// uploadAttachments accepts one or more images for the caller's own report.
func (a *App) uploadAttachments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	owner, err := a.reportOwner(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "REPORT_NOT_FOUND", "보고서를 찾을 수 없습니다.")
		return
	}
	if owner != p.ID {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인의 보고서에만 이미지를 첨부할 수 있습니다.")
		return
	}
	maxPerReport, maxBytes := a.attachmentLimits(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes*int64(maxPerReport)+(1<<20))
	if err := r.ParseMultipartForm(maxBytes + (1 << 20)); err != nil {
		// Two causes with two remedies, and Go says which: a request past the
		// reader's cap arrives as *http.MaxBytesError, anything else is a body
		// that did not parse. "올바르지 않거나 초과했습니다" left the reader to
		// guess between sending fewer images and sending them again.
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusBadRequest, "UPLOAD_TOO_LARGE",
				fmt.Sprintf("한 번에 올린 이미지 전체가 허용 용량을 넘습니다. 한 개당 %dMB, 보고서당 %d개까지이며 나눠서 올리면 됩니다.",
					maxBytes>>20, maxPerReport))
			return
		}
		writeError(w, http.StatusBadRequest, "INVALID_UPLOAD",
			"업로드 형식을 읽을 수 없습니다. 전송이 중간에 끊겼을 수 있으니 다시 시도해 주세요.")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "NO_FILES", "첨부할 이미지를 선택하세요.")
		return
	}
	var existing int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM report_attachments WHERE report_id=$1`, id).Scan(&existing); err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "첨부 이미지를 조회할 수 없습니다.")
		return
	}
	if existing+len(files) > maxPerReport {
		writeError(w, http.StatusBadRequest, "TOO_MANY_ATTACHMENTS",
			fmt.Sprintf("보고서당 이미지는 최대 %d개까지 첨부할 수 있습니다. 현재 %d개.", maxPerReport, existing))
		return
	}
	placement := strings.ToUpper(strings.TrimSpace(r.FormValue("placement")))
	if placement != placementBefore {
		placement = placementAfter
	}
	if err := os.MkdirAll(filepath.Join(stateDirectoryAttachments, strconv.FormatInt(id, 10)), 0o700); err != nil {
		a.logger.Error("create attachment directory", "error", err)
		writeError(w, http.StatusInternalServerError, "STORAGE_ERROR", "이미지 저장소를 만들 수 없습니다.")
		return
	}
	var nextOrder int
	_ = a.db.QueryRow(r.Context(), `SELECT coalesce(max(sort_order),-1)+1 FROM report_attachments WHERE report_id=$1 AND placement=$2`, id, placement).Scan(&nextOrder)

	created := []attachmentView{}
	for _, header := range files {
		if header.Size > maxBytes {
			writeError(w, http.StatusBadRequest, "FILE_TOO_LARGE",
				fmt.Sprintf("%s의 크기가 허용 한도(%dMB)를 초과했습니다.", header.Filename, maxBytes>>20))
			return
		}
		stream, openErr := header.Open()
		if openErr != nil {
			writeError(w, http.StatusBadRequest, "INVALID_UPLOAD", "업로드한 파일을 읽을 수 없습니다.")
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(stream, maxBytes+1))
		stream.Close()
		if readErr != nil || int64(len(body)) > maxBytes {
			writeError(w, http.StatusBadRequest, "FILE_TOO_LARGE", "업로드한 파일이 너무 큽니다.")
			return
		}
		// Decoding is the check: an image that cannot be decoded is not an image,
		// whatever its name or declared content type says.
		config, format, decodeErr := image.DecodeConfig(strings.NewReader(string(body)))
		if decodeErr != nil {
			writeError(w, http.StatusBadRequest, "UNSUPPORTED_IMAGE",
				fmt.Sprintf("%s은 PNG, JPEG 또는 GIF 이미지가 아닙니다.", header.Filename))
			return
		}
		kind, supported := imageFormats[format]
		if !supported || config.Width <= 0 || config.Height <= 0 {
			writeError(w, http.StatusBadRequest, "UNSUPPORTED_IMAGE",
				fmt.Sprintf("%s 형식(%s)은 지원하지 않습니다. PNG, JPEG, GIF만 첨부할 수 있습니다.", header.Filename, format))
			return
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(body))
		relative := filepath.Join(strconv.FormatInt(id, 10), sum+"."+kind.Extension)
		absolute := filepath.Join(stateDirectoryAttachments, relative)
		if err := writeAttachmentFile(absolute, body); err != nil {
			a.logger.Error("store attachment", "error", err, "reportId", id, "trace", traceIDFromContext(r.Context()))
			if errors.Is(err, syscall.ENOSPC) {
				// Naming the cause, because the person who can fix it is not the
				// person who sees the message. "이미지를 저장할 수 없습니다" sent an
				// administrator looking at the upload code; the volume was full.
				writeError(w, http.StatusInsufficientStorage, "STORAGE_FULL",
					"서버 저장 공간이 가득 차 이미지를 저장하지 못했습니다. 관리자에게 /var/lib/weekly 볼륨 용량을 확인하도록 알려 주세요.")
				return
			}
			writeError(w, http.StatusInternalServerError, "STORAGE_ERROR", "이미지를 저장할 수 없습니다.")
			return
		}
		var attachmentID int64
		var createdAt time.Time
		err = a.db.QueryRow(r.Context(), `INSERT INTO report_attachments
			(report_id,user_id,original_filename,stored_path,content_type,extension,size_bytes,width,height,sha256,placement,sort_order)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id,created_at`,
			id, p.ID, trimRunes(filepath.Base(header.Filename), 255), relative, kind.ContentType, kind.Extension,
			len(body), config.Width, config.Height, sum, placement, nextOrder).Scan(&attachmentID, &createdAt)
		if err != nil {
			a.logger.Error("insert attachment", "error", err, "reportId", id, "trace", traceIDFromContext(r.Context()))
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "이미지 정보를 저장할 수 없습니다.")
			return
		}
		created = append(created, attachmentView{
			ID: attachmentID, Filename: trimRunes(filepath.Base(header.Filename), 255), Placement: placement,
			SortOrder: nextOrder, SizeBytes: int64(len(body)), Width: config.Width, Height: config.Height,
			CreatedAt: createdAt, Available: true,
		})
		nextOrder++
	}
	a.audit(r, p, "report.attachment_upload", "report", strconv.FormatInt(id, 10), map[string]any{"count": len(created), "placement": placement})
	writeData(w, http.StatusCreated, created)
}

// serveAttachment streams the stored image for the report preview.
func (a *App) serveAttachment(w http.ResponseWriter, r *http.Request) {
	reportID, attachmentID, ok := attachmentPathIDs(w, r)
	if !ok {
		return
	}
	if !a.canViewReport(r.Context(), currentPrincipal(r.Context()), reportID) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "이 이미지를 조회할 권한이 없습니다.")
		return
	}
	var storedPath, contentType string
	err := a.db.QueryRow(r.Context(), `SELECT stored_path,content_type FROM report_attachments WHERE id=$1 AND report_id=$2`,
		attachmentID, reportID).Scan(&storedPath, &contentType)
	if err != nil {
		writeError(w, http.StatusNotFound, "ATTACHMENT_NOT_FOUND", "첨부 이미지를 찾을 수 없습니다.")
		return
	}
	body, err := os.ReadFile(safeAttachmentPath(storedPath))
	if err != nil {
		// A distinct code, because the two 404s here have different causes and
		// different fixes: no row means a stale link, a missing file means the
		// stored image is gone and has to be attached again.
		a.logger.Error("read attachment", "error", err, "attachmentId", attachmentID, "storedPath", storedPath)
		writeError(w, http.StatusNotFound, "ATTACHMENT_FILE_MISSING",
			"첨부 이미지 파일이 서버에 없습니다. 이미지를 다시 첨부해 주세요.")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(body)
}

func (a *App) updateAttachment(w http.ResponseWriter, r *http.Request) {
	reportID, attachmentID, ok := attachmentPathIDs(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	owner, err := a.reportOwner(r.Context(), reportID)
	if err != nil || owner != p.ID {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인의 보고서 이미지만 수정할 수 있습니다.")
		return
	}
	var input struct {
		Caption   *string `json:"caption"`
		Placement *string `json:"placement"`
		SortOrder *int    `json:"sortOrder"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Caption != nil && runeLength(*input.Caption) > 240 {
		writeError(w, http.StatusBadRequest, "INVALID_CAPTION", "이미지 설명은 240자 이하로 입력하세요.")
		return
	}
	placement := ""
	if input.Placement != nil {
		placement = strings.ToUpper(strings.TrimSpace(*input.Placement))
		if placement != placementBefore && placement != placementAfter {
			writeError(w, http.StatusBadRequest, "INVALID_PLACEMENT", "삽입 위치는 BEFORE 또는 AFTER여야 합니다.")
			return
		}
	}
	command, err := a.db.Exec(r.Context(), `UPDATE report_attachments SET
		caption=coalesce($1,caption),
		placement=coalesce($2,placement),
		sort_order=coalesce($3,sort_order)
		WHERE id=$4 AND report_id=$5`,
		input.Caption, nullableString(placement), input.SortOrder, attachmentID, reportID)
	if err != nil {
		a.logger.Error("update attachment", "error", err, "attachmentId", attachmentID, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "이미지 정보를 저장할 수 없습니다.")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "ATTACHMENT_NOT_FOUND", "첨부 이미지를 찾을 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, map[string]int64{"id": attachmentID})
}

func (a *App) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	reportID, attachmentID, ok := attachmentPathIDs(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	owner, err := a.reportOwner(r.Context(), reportID)
	if err != nil || owner != p.ID {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인의 보고서 이미지만 삭제할 수 있습니다.")
		return
	}
	var storedPath string
	err = a.db.QueryRow(r.Context(), `DELETE FROM report_attachments WHERE id=$1 AND report_id=$2 RETURNING stored_path`,
		attachmentID, reportID).Scan(&storedPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "ATTACHMENT_NOT_FOUND", "첨부 이미지를 찾을 수 없습니다.")
		return
	}
	// The same bytes can back two rows after a duplicate upload, so only remove
	// the file once nothing references it any more.
	var remaining int
	_ = a.db.QueryRow(r.Context(), `SELECT count(*) FROM report_attachments WHERE stored_path=$1`, storedPath).Scan(&remaining)
	if remaining == 0 {
		if err := os.Remove(safeAttachmentPath(storedPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.logger.Warn("remove attachment file", "error", err, "attachmentId", attachmentID)
		}
	}
	a.audit(r, p, "report.attachment_delete", "report", strconv.FormatInt(reportID, 10), map[string]any{"attachmentId": attachmentID})
	w.WriteHeader(http.StatusNoContent)
}

func attachmentPathIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	reportID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || reportID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "보고서 식별자가 올바르지 않습니다.")
		return 0, 0, false
	}
	attachmentID, err := strconv.ParseInt(r.PathValue("attachmentId"), 10, 64)
	if err != nil || attachmentID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "이미지 식별자가 올바르지 않습니다.")
		return 0, 0, false
	}
	return reportID, attachmentID, true
}

// safeAttachmentPath keeps a stored path inside the attachment directory even if
// the column were ever tampered with.
func safeAttachmentPath(stored string) string {
	clean := filepath.Clean("/" + strings.ReplaceAll(stored, `\`, "/"))
	return filepath.Join(stateDirectoryAttachments, filepath.FromSlash(clean))
}

// attachmentSlides turns stored captures into one slide each, sized to the deck.
//
// open reports whether the image is there and hands back a way to read it later.
// Geometry comes from the stored width and height, so nothing here needs the
// bytes; deferring the read is what keeps a whole export's worth of images from
// being resident at once.
func attachmentSlides(items []storedAttachment, canvasWidth, canvasHeight int, open func(storedAttachment) (func() ([]byte, error), bool)) []builtSlide {
	slides := make([]builtSlide, 0, len(items))
	for index, item := range items {
		margin := canvasWidth / 25
		headerHeight := canvasHeight / 9
		caption := strings.TrimSpace(item.Caption)
		title := caption
		if title == "" {
			title = item.Filename
		}
		load, ok := open(item)
		if !ok {
			// The file is gone from the state volume — the deployment logs that
			// once, at startup, to an operator. The author downloading the deck
			// is a different person with a different question: five captures
			// went in and three came out, and dropping the slide answered it
			// with silence. The page stays, with its title and its place in the
			// order, and says what happened to the picture.
			shapes := textBox(2, "CaptureTitle", margin, margin, canvasWidth-2*margin, headerHeight,
				shapeStyle{}, []textRun{{Text: title, Size: 1600, Bold: true, Color: "0F172A"}})
			shapes += textBox(3, "CaptureMissing", margin, margin+headerHeight,
				canvasWidth-2*margin, canvasHeight/6, shapeStyle{Fill: "FEF3C7"},
				[]textRun{{Text: "이미지 파일을 찾을 수 없어 담지 못했습니다. 이 캡처를 다시 올려 주세요.",
					Size: 1400, Color: "92400E"}})
			slides = append(slides, builtSlide{Shapes: shapes})
			continue
		}
		relID := "rId2"
		media := slideMedia{
			Name:        fmt.Sprintf("weekly-capture-%d.%s", item.ID, item.Extension),
			ContentType: item.ContentType,
			Extension:   item.Extension,
			Load:        load,
			RelID:       relID,
		}
		shapes := textBox(2, "CaptureTitle", margin, margin, canvasWidth-2*margin, headerHeight,
			shapeStyle{}, []textRun{{Text: title, Size: 1600, Bold: true, Color: "0F172A"}})
		boxY := margin + headerHeight
		boxHeight := canvasHeight - boxY - margin
		shapes += pictureFrame(3, fmt.Sprintf("Capture %d", index+1), relID,
			margin, boxY, canvasWidth-2*margin, boxHeight, item.Width, item.Height)
		slides = append(slides, builtSlide{Shapes: shapes, Media: []slideMedia{media}})
	}
	return slides
}

// attachmentReadable checks the file is there without reading it, and returns a
// reader for the moment the package actually needs the bytes.
func (a *App) attachmentReadable(item storedAttachment) (func() ([]byte, error), bool) {
	path := safeAttachmentPath(item.StoredPath)
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return nil, false
	}
	return func() ([]byte, error) { return os.ReadFile(path) }, true
}

// attachCaptureSlides adds the report's capture slides around the rendered deck.
func (a *App) attachCaptureSlides(ctx context.Context, deck []byte, reportID int64) []byte {
	items, err := a.loadAttachments(ctx, reportID)
	if err != nil || len(items) == 0 {
		if err != nil {
			a.logger.Error("load attachments for export", "error", err, "reportId", reportID)
		}
		return deck
	}
	width, height := presentationSlideSize(deck)
	before := []storedAttachment{}
	after := []storedAttachment{}
	for _, item := range items {
		if item.Placement == placementBefore {
			before = append(before, item)
		} else {
			after = append(after, item)
		}
	}
	// One pass for both ends. Two calls rebuilt the whole package twice, and by
	// the second call that package already carried every embedded image.
	beforeSlides := attachmentSlides(before, width, height, a.attachmentReadable)
	afterSlides := attachmentSlides(after, width, height, a.attachmentReadable)
	result := deck
	if len(beforeSlides) > 0 || len(afterSlides) > 0 {
		updated, appendErr := appendSlidesToPPTX(result, beforeSlides, afterSlides)
		if appendErr != nil {
			// An export without the captures is still useful, so the written
			// report is never blocked by an image problem.
			a.logger.Error("append capture slides", "error", appendErr, "reportId", reportID)
			return result
		}
		result = updated
	}
	return result
}

// cleanupAttachmentFiles removes stored images whose rows are gone, which
// happens when a report is deleted and the cascade drops its attachments.
func (a *App) cleanupAttachmentFiles(ctx context.Context) {
	entries, err := os.ReadDir(stateDirectoryAttachments)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		reportID, convErr := strconv.ParseInt(entry.Name(), 10, 64)
		if convErr != nil {
			continue
		}
		var remaining int
		if err := a.db.QueryRow(ctx, `SELECT count(*) FROM report_attachments WHERE report_id=$1`, reportID).Scan(&remaining); err != nil {
			continue
		}
		if remaining > 0 {
			continue
		}
		if err := os.RemoveAll(filepath.Join(stateDirectoryAttachments, entry.Name())); err != nil {
			a.logger.Warn("remove orphaned attachment directory", "error", err, "reportId", reportID)
		}
	}
}

// writeAttachmentFile puts the bytes in place, or leaves nothing behind.
//
// os.WriteFile creates the destination and writes into it, so a disk that fills
// part way through leaves a truncated file under the final name — unreferenced,
// because the row is only inserted after the write succeeds, and therefore
// invisible to every cleanup path the product has. Measured: one failed upload
// left 319 KB on a 320 KB volume, and the volume stayed full forever.
//
// Writing to a temporary neighbour and renaming is what the PPTX template and
// the import store already do. The rename is atomic within a directory, so the
// final name never names a half-written file either.
func writeAttachmentFile(path string, body []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "upload-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(body)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
