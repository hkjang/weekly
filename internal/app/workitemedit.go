package app

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Correcting what the title normalizer decided.
//
// Work item identity is derived, not declared: a title is normalized and that
// key is the task. It is a good default and it is sometimes wrong — two tasks
// reported under one name become one history, and one task reported under two
// spellings becomes two. Until now there was no way to say so, which left an
// automatic judgement standing as fact and quietly wrong ageing, handover and
// duplicate-detection results built on top of it.
//
// Both corrections have to survive the next save, and that is the hard part.
// Merge survives because the source row keeps its normalized key and points at
// the target, so resolving the old title lands on the target. Split survives
// because the moved snapshots are pinned, and a pinned snapshot keeps the
// identity its author chose until the title itself changes.

type workItemEditResponse struct {
	WorkItemID int64 `json:"workItemId"`
	// MovedItems is how many weekly snapshots changed hands, which is the only
	// number that tells the author whether the correction did what they meant.
	MovedItems int64 `json:"movedItems"`
}

// workItemOwner returns the owner of a work item, or errNotFound.
func (a *App) workItemOwner(ctx context.Context, id int64) (int64, *int64, error) {
	var owner int64
	var mergedInto *int64
	err := a.db.QueryRow(ctx, `SELECT user_id, merged_into_id FROM work_items WHERE id=$1`, id).Scan(&owner, &mergedInto)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, errNotFound
	}
	return owner, mergedInto, err
}

// canEditWorkItem allows the owner, and an administrator acting on their behalf.
// A team leader is deliberately excluded: this rewrites someone else's reporting
// history, and reviewing a report is not the same authority as rewriting it.
func canEditWorkItem(p *principal, owner int64) bool {
	return p.ID == owner || p.Role == "ADMIN"
}

func (a *App) mergeWorkItem(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		IntoID int64 `json:"intoId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.IntoID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_TARGET", "합칠 대상 업무를 지정하세요.")
		return
	}
	if input.IntoID == sourceID {
		writeError(w, http.StatusBadRequest, "INVALID_TARGET", "같은 업무끼리는 합칠 수 없습니다.")
		return
	}
	p := currentPrincipal(r.Context())
	sourceOwner, _, err := a.workItemOwner(r.Context(), sourceID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 조회할 수 없습니다.")
		return
	}
	targetOwner, targetMerged, err := a.workItemOwner(r.Context(), input.IntoID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "합칠 대상 업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 조회할 수 없습니다.")
		return
	}
	if sourceOwner != targetOwner {
		// Work item identity is per owner, and so is the unique key behind it.
		// Merging across owners would claim one person's history for another.
		writeError(w, http.StatusBadRequest, "DIFFERENT_OWNER", "담당자가 다른 업무끼리는 합칠 수 없습니다.")
		return
	}
	if !canEditWorkItem(p, sourceOwner) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인 업무만 정리할 수 있습니다.")
		return
	}
	if targetMerged != nil {
		writeError(w, http.StatusBadRequest, "TARGET_ALREADY_MERGED", "이미 다른 업무로 합쳐진 업무에는 합칠 수 없습니다.")
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무를 합칠 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())

	tag, err := tx.Exec(r.Context(), `UPDATE report_items SET work_item_id=$1, updated_at=now() WHERE work_item_id=$2`, input.IntoID, sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무를 합칠 수 없습니다.")
		return
	}
	moved := tag.RowsAffected()
	// The source keeps its row and its key so the old title still resolves, and
	// anything that already pointed at the source is re-aimed at the target so
	// the pointer chain never grows past one hop.
	if _, err := tx.Exec(r.Context(), `UPDATE work_items SET merged_into_id=$1, updated_at=now() WHERE id=$2`, input.IntoID, sourceID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무를 합칠 수 없습니다.")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE work_items SET merged_into_id=$1, updated_at=now() WHERE merged_into_id=$2`, input.IntoID, sourceID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무를 합칠 수 없습니다.")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무를 합칠 수 없습니다.")
		return
	}
	a.audit(r, p, "work_item.merge", "work_item", strconv.FormatInt(sourceID, 10),
		map[string]any{"intoId": input.IntoID, "movedItems": moved, "ownerId": sourceOwner})
	writeData(w, http.StatusOK, workItemEditResponse{WorkItemID: input.IntoID, MovedItems: moved})
}

func (a *App) splitWorkItem(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Title         string  `json:"title"`
		Category      string  `json:"category"`
		ReportItemIDs []int64 `json:"reportItemIds"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "INVALID_TITLE", "분리한 업무의 제목을 입력하세요.")
		return
	}
	key := candidateTitleKey(title)
	if key == "" {
		writeError(w, http.StatusBadRequest, "INVALID_TITLE", "제목에 사용할 수 있는 글자가 없습니다.")
		return
	}
	if len(input.ReportItemIDs) == 0 {
		writeError(w, http.StatusBadRequest, "NO_ITEMS_SELECTED", "분리할 주차를 하나 이상 선택하세요.")
		return
	}
	p := currentPrincipal(r.Context())
	owner, _, err := a.workItemOwner(r.Context(), sourceID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 조회할 수 없습니다.")
		return
	}
	if !canEditWorkItem(p, owner) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인 업무만 정리할 수 있습니다.")
		return
	}

	var selected, total int
	if err := a.db.QueryRow(r.Context(), `SELECT
		count(*) FILTER (WHERE id = ANY($2)),
		count(*)
		FROM report_items WHERE work_item_id=$1`, sourceID, input.ReportItemIDs).Scan(&selected, &total); err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무 주차를 조회할 수 없습니다.")
		return
	}
	if selected != len(input.ReportItemIDs) {
		writeError(w, http.StatusBadRequest, "ITEM_NOT_IN_WORK_ITEM", "선택한 주차 중 이 업무에 속하지 않는 것이 있습니다.")
		return
	}
	if selected == total {
		// Moving everything is a rename, not a split, and it would leave an
		// empty task behind whose key still captures the old title.
		writeError(w, http.StatusBadRequest, "SPLIT_TAKES_ALL",
			"모든 주차를 분리할 수는 없습니다. 이름만 바꾸려면 보고서에서 업무 제목을 수정하세요.")
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무를 분리할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())

	var targetID int64
	var targetMerged *int64
	err = tx.QueryRow(r.Context(), `SELECT id, merged_into_id FROM work_items WHERE user_id=$1 AND normalized_key=$2`,
		owner, trimRunes(key, 240)).Scan(&targetID, &targetMerged)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(r.Context(), `INSERT INTO work_items(user_id,title,normalized_key,category)
			VALUES($1,$2,$3,$4) RETURNING id`,
			owner, trimRunes(title, 240), trimRunes(key, 240), trimRunes(strings.TrimSpace(input.Category), 80)).Scan(&targetID); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "분리한 업무를 만들 수 없습니다.")
			return
		}
	case err != nil:
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 조회할 수 없습니다.")
		return
	case targetID == sourceID:
		writeError(w, http.StatusBadRequest, "SAME_TITLE", "원래 업무와 같은 제목으로는 분리할 수 없습니다.")
		return
	case targetMerged != nil:
		writeError(w, http.StatusBadRequest, "TITLE_ALREADY_MERGED",
			"그 제목의 업무는 다른 업무로 합쳐져 있습니다. 다른 제목을 사용하세요.")
		return
	}

	// Pinned, because identity is otherwise re-derived from the title on every
	// save and the next edit of these reports would pull them straight back.
	tag, err := tx.Exec(r.Context(), `UPDATE report_items SET work_item_id=$1, work_item_pinned=true, updated_at=now()
		WHERE id = ANY($2) AND work_item_id=$3`, targetID, input.ReportItemIDs, sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무를 분리할 수 없습니다.")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "업무를 분리할 수 없습니다.")
		return
	}
	moved := tag.RowsAffected()
	a.audit(r, p, "work_item.split", "work_item", strconv.FormatInt(sourceID, 10),
		map[string]any{"newWorkItemId": targetID, "movedItems": moved, "ownerId": owner, "title": title})
	writeData(w, http.StatusOK, workItemEditResponse{WorkItemID: targetID, MovedItems: moved})
}
