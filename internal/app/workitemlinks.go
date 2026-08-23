package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Declaring that one task waits on another.
//
// A weekly report can say a task is stalled. It has no way to say the task is
// stalled because another team has not finished something, so every status
// meeting rediscovers that by asking around the room.
//
// Direction is recorded once. "A depends on B" and "B blocks A" are one fact
// seen from two ends; storing both kinds would let them disagree, and a graph
// that contradicts itself is worse than none. Each screen reads the single row
// from where it stands.
//
// Declaring is one-sided on purpose. The owner of the waiting task states what
// they are waiting for — a claim about their own work — and needs no permission
// from the other team, because a dependency that requires the blocker's consent
// is a dependency nobody records. The blocker sees it from their side and can
// dispute it, which is the point of carrying the reason with it.

type workItemLink struct {
	ID    int64  `json:"id"`
	Note  string `json:"note"`
	Ready bool   `json:"ready"`
	// The other end, whichever end that is for the caller.
	WorkItemID       int64     `json:"workItemId"`
	Title            string    `json:"title"`
	DisplayName      string    `json:"displayName"`
	OrganizationName string    `json:"organizationName"`
	Progress         int       `json:"progress"`
	Completed        bool      `json:"completed"`
	LastWeek         string    `json:"lastWeek"`
	CreatedAt        time.Time `json:"createdAt"`
}

type workItemLinkView struct {
	// Blockers are what this task is waiting for; Blocking is what waits on it.
	Blockers []workItemLink `json:"blockers"`
	Blocking []workItemLink `json:"blocking"`
}

// linkQuery reads one side of the graph. The two %s are column names, never
// values: `far` names the end being described and `near` the end being matched.
// Both are supplied by linkSide below, which is a closed set — a column name
// cannot be parameterised, so the only safe rule is that it never comes from a
// request.
const linkQuery = `SELECT l.id, l.note, l.created_at, wi.id, wi.title,
		coalesce(u.display_name,''), coalesce(o.name,''),
		coalesce(latest.progress,0), coalesce(latest.week_start::text,'')
	FROM work_item_links l
	JOIN work_items wi ON wi.id = l.%s
	LEFT JOIN users u ON u.id = wi.user_id
	LEFT JOIN organizations o ON o.id = u.organization_id
	LEFT JOIN LATERAL (
		SELECT i.progress, r.week_start FROM report_items i
		JOIN weekly_reports r ON r.id = i.report_id
		WHERE i.work_item_id = wi.id ORDER BY r.week_start DESC, i.id DESC LIMIT 1
	) latest ON true
	WHERE l.%s = $1
	ORDER BY l.id`

// linkSide is the pair of columns for one direction, and the only source of
// the names interpolated into linkQuery.
type linkSide struct{ far, near string }

var (
	// What this task is waiting for: match on the blocked end, describe the
	// blocker.
	sideBlockers = linkSide{far: "blocker_id", near: "blocked_id"}
	// What waits on this task: the same row read the other way round.
	sideBlocking = linkSide{far: "blocked_id", near: "blocker_id"}
)

func (a *App) readLinks(ctx context.Context, workItemID int64, side linkSide) ([]workItemLink, error) {
	rows, err := a.db.Query(ctx, fmt.Sprintf(linkQuery, side.far, side.near), workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []workItemLink{}
	for rows.Next() {
		var item workItemLink
		if err := rows.Scan(&item.ID, &item.Note, &item.CreatedAt, &item.WorkItemID, &item.Title,
			&item.DisplayName, &item.OrganizationName, &item.Progress, &item.LastWeek); err != nil {
			return nil, err
		}
		item.Completed = item.Progress >= 100
		item.Ready = item.Completed
		result = append(result, item)
	}
	return result, rows.Err()
}

func (a *App) listWorkItemLinks(w http.ResponseWriter, r *http.Request) {
	workItemID, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	_, visible, err := a.workItemViewer(r.Context(), p, workItemID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		a.logger.Error("list links", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "의존 관계를 조회할 수 없습니다.")
		return
	}
	if !visible {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조회 권한 범위 밖의 업무입니다.")
		return
	}
	view := workItemLinkView{}
	if view.Blockers, err = a.readLinks(r.Context(), workItemID, sideBlockers); err != nil {
		a.logger.Error("list links", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "의존 관계를 조회할 수 없습니다.")
		return
	}
	if view.Blocking, err = a.readLinks(r.Context(), workItemID, sideBlocking); err != nil {
		a.logger.Error("list links", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "의존 관계를 조회할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}

// dependencyPath returns the chain from `from` to `to` if one already exists,
// walking the edges that are already declared.
//
// Registering an edge blocker -> blocked closes a cycle exactly when blocked
// already reaches blocker. Refusing without saying which chain closes it leaves
// the person guessing at a graph they cannot see, so the path comes back with
// the refusal.
func (a *App) dependencyPath(ctx context.Context, from, to int64) ([]string, error) {
	rows, err := a.db.Query(ctx, `
		WITH RECURSIVE walk(id, path, titles, depth) AS (
			-- Both terms must agree on the column type, and work_items.title is
			-- varchar(240) while the recursive concatenation widens to varchar.
			-- Cast both ends to text so the union has one type; PostgreSQL
			-- refuses the query outright otherwise.
			SELECT $1::bigint, ARRAY[$1::bigint], ARRAY[(SELECT title::text FROM work_items WHERE id=$1)], 0
			UNION ALL
			SELECT l.blocked_id, walk.path || l.blocked_id, walk.titles || wi.title::text, walk.depth + 1
			FROM walk
			JOIN work_item_links l ON l.blocker_id = walk.id
			JOIN work_items wi ON wi.id = l.blocked_id
			WHERE NOT l.blocked_id = ANY(walk.path) AND walk.depth < 32
		)
		SELECT titles FROM walk WHERE id = $2 ORDER BY depth LIMIT 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var titles []string
	if err := rows.Scan(&titles); err != nil {
		return nil, err
	}
	return titles, rows.Err()
}

func (a *App) createWorkItemLink(w http.ResponseWriter, r *http.Request) {
	blockedID, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	owner, _, err := a.workItemOwner(r.Context(), blockedID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "의존 관계를 등록할 수 없습니다.")
		return
	}
	// Declaring what your own work waits on is a statement about your own work,
	// so it follows the same rule as correcting it.
	if !canEditWorkItem(p, owner) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "본인 업무의 선행 관계만 등록할 수 있습니다.")
		return
	}
	var input struct {
		BlockerID int64  `json:"blockerId"`
		Note      string `json:"note"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Note = strings.TrimSpace(input.Note)
	if input.BlockerID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_LINK", "선행 업무를 선택하세요.")
		return
	}
	if input.BlockerID == blockedID {
		writeError(w, http.StatusBadRequest, "INVALID_LINK", "업무는 자기 자신을 기다릴 수 없습니다.")
		return
	}
	if runeLength(input.Note) > 2000 {
		writeError(w, http.StatusBadRequest, "INVALID_LINK", "사유는 2000자 이하로 입력하세요.")
		return
	}
	// The blocker has to exist, but it deliberately does not have to be visible
	// to this person: waiting on another organisation's work is the case this
	// feature exists for, and the title is already readable from the insight
	// screens anyone here can open.
	if _, _, err := a.workItemOwner(r.Context(), input.BlockerID); err != nil {
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, "BLOCKER_NOT_FOUND", "선행 업무를 찾을 수 없습니다.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "의존 관계를 등록할 수 없습니다.")
		return
	}
	path, err := a.dependencyPath(r.Context(), blockedID, input.BlockerID)
	if err != nil {
		a.logger.Error("dependency cycle check", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "의존 관계를 등록할 수 없습니다.")
		return
	}
	if len(path) > 0 {
		writeError(w, http.StatusConflict, "DEPENDENCY_CYCLE",
			"순환 의존이 됩니다: "+strings.Join(path, " → ")+" → (이 업무). 먼저 이 사슬 중 하나를 끊으세요.")
		return
	}

	var id int64
	err = a.db.QueryRow(r.Context(), `INSERT INTO work_item_links(blocker_id, blocked_id, note, created_by)
		VALUES($1,$2,$3,$4)
		ON CONFLICT (blocker_id, blocked_id) DO NOTHING RETURNING id`,
		input.BlockerID, blockedID, input.Note, p.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "LINK_EXISTS", "이미 등록된 선행 관계입니다.")
		return
	}
	if err != nil {
		a.logger.Error("create link", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "의존 관계를 등록할 수 없습니다.")
		return
	}
	a.audit(r, p, "work_item.link", "work_item", strconv.FormatInt(blockedID, 10),
		map[string]any{"blockerId": input.BlockerID})
	writeData(w, http.StatusCreated, map[string]int64{"id": id})
}

// deleteWorkItemLink removes a declaration. Either end may remove it: the
// waiting side because they declared it, and the blocking side because a
// dependency asserted about their work that is not real should not need their
// agreement to take back.
func (a *App) deleteWorkItemLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("linkId"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "식별자가 올바르지 않습니다.")
		return
	}
	p := currentPrincipal(r.Context())
	var blockerOwner, blockedOwner int64
	err = a.db.QueryRow(r.Context(), `SELECT b.user_id, d.user_id
		FROM work_item_links l JOIN work_items b ON b.id=l.blocker_id JOIN work_items d ON d.id=l.blocked_id
		WHERE l.id=$1`, id).Scan(&blockerOwner, &blockedOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "LINK_NOT_FOUND", "의존 관계를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "의존 관계를 삭제할 수 없습니다.")
		return
	}
	if !canEditWorkItem(p, blockedOwner) && !canEditWorkItem(p, blockerOwner) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "관계된 업무의 담당자만 삭제할 수 있습니다.")
		return
	}
	if _, err := a.db.Exec(r.Context(), `DELETE FROM work_item_links WHERE id=$1`, id); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "의존 관계를 삭제할 수 없습니다.")
		return
	}
	a.audit(r, p, "work_item.unlink", "work_item_link", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}
