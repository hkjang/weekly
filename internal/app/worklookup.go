package app

import (
	"net/http"
	"strings"
)

// Finding the work you are waiting on, so you can say so.
//
// Declaring a dependency names somebody else's task, and you cannot name what
// you cannot find. The existing work search answers a different question —
// "has anyone dealt with this before" — and to answer it, it carries the issue
// text, the resolution and the reasoning from other people's reports. That is
// more than a declaration needs, and more than everyone should see.
//
// This returns the four fields the declaration itself will display once made:
// which task, who owns it, which organisation, and the identifier to link to.
// Nothing from the body of anybody's report.
//
// Scope is the whole deployment, deliberately. A dependency that stays inside
// one team is settled by the two people in it; the ones worth recording cross
// an organisation, and limiting the lookup to the caller's own organisation
// would exclude exactly those. The trade is that any signed-in person can see
// that a task by this name exists and who owns it. That is the same thing the
// declaration puts on screen, so the lookup reveals nothing the feature does
// not already publish — but it is a widening, and an operator should know it.
type workLookupHit struct {
	WorkItemID       int64  `json:"workItemId"`
	Title            string `json:"title"`
	DisplayName      string `json:"displayName"`
	OrganizationName string `json:"organizationName"`
}

type workLookupResponse struct {
	Query string          `json:"query"`
	Hits  []workLookupHit `json:"hits"`
	Total int             `json:"total"`
	Limit int             `json:"limit"`
}

// workLookupLimit is how many names come back. A picker is read, not paged: if
// the right task is not in the first handful, the answer is a better query.
const workLookupLimit = 8

func (a *App) lookupWorkItems(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if runeLength(query) < 2 {
		writeData(w, http.StatusOK, workLookupResponse{Query: query, Hits: []workLookupHit{}, Limit: workLookupLimit})
		return
	}
	pattern := "%" + strings.ToLower(query) + "%"

	var total int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM work_items w
		WHERE w.merged_into_id IS NULL AND lower(w.title) LIKE $1`, pattern).Scan(&total); err != nil {
		a.logger.Error("work lookup count", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 찾을 수 없습니다.")
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT w.id, w.title, coalesce(u.display_name,''), coalesce(o.name,'')
		FROM work_items w
		LEFT JOIN users u ON u.id = w.user_id
		LEFT JOIN organizations o ON o.id = u.organization_id
		WHERE w.merged_into_id IS NULL AND lower(w.title) LIKE $1
		ORDER BY length(w.title), w.title, w.id
		LIMIT $2`, pattern, workLookupLimit)
	if err != nil {
		a.logger.Error("work lookup", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 찾을 수 없습니다.")
		return
	}
	defer rows.Close()
	hits := []workLookupHit{}
	for rows.Next() {
		var hit workLookupHit
		if err := rows.Scan(&hit.WorkItemID, &hit.Title, &hit.DisplayName, &hit.OrganizationName); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 찾을 수 없습니다.")
			return
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "업무를 찾을 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, workLookupResponse{Query: query, Hits: hits, Total: total, Limit: workLookupLimit})
}
