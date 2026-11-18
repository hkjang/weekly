package app

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// 메일 발송 제외 조직.
//
// An organisation can be marked so that nobody in it — or in anything beneath
// it — receives mail from this service. The check is made twice for every
// queued kind: when the row is queued, so the queue does not fill with mail
// that will never go, and again when the worker is about to send, because a
// person can be moved into the organisation, or the organisation marked, while
// the row waits for a relay that is down.
//
// The SQL side is organization_mail_blocked() (migration 032), so every queue
// asks the same question the same way.

// executiveOfficeName is the organisation this was asked for. A new
// organisation with this name starts out excluded, so an 임원실 that the
// department sync creates tomorrow is not the one that gets mail.
const executiveOfficeName = "임원실"

var organizationNameSpace = regexp.MustCompile(`\s+`)

// defaultMailBlocked says whether a new organisation of this name starts out
// excluded from mail.
func defaultMailBlocked(name string) bool {
	return organizationNameSpace.ReplaceAllString(strings.TrimSpace(name), "") == executiveOfficeName
}

// mailBlockedReason is what a writer reads on a report delivery the worker
// refused to send.
const mailBlockedReason = "소속 조직이 메일 발송 제외 조직이라 보내지 않았습니다."

// mailBlockedOrganization names the nearest organisation that excludes this
// person from mail, or "" when none does.
func (a *App) mailBlockedOrganization(ctx context.Context, userID int64) (string, error) {
	var name string
	err := a.db.QueryRow(ctx, `WITH RECURSIVE up(id, depth) AS (
			SELECT organization_id, 0 FROM users WHERE id=$1 AND organization_id IS NOT NULL
			UNION ALL
			SELECT o.parent_id, up.depth+1 FROM organizations o JOIN up ON o.id=up.id
			WHERE o.parent_id IS NOT NULL AND up.depth < `+strconv.Itoa(orgSubtreeDepth)+`)
		SELECT o.name FROM up JOIN organizations o ON o.id=up.id
		WHERE o.mail_blocked ORDER BY up.depth LIMIT 1`, userID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// refuseBlockedMailRecipient answers a test send for somebody whose
// organisation receives no mail, and reports whether it did. A test is still
// a mail to that address, and "never" was the instruction.
func (a *App) refuseBlockedMailRecipient(w http.ResponseWriter, r *http.Request, userID int64) bool {
	organization, err := a.mailBlockedOrganization(r.Context(), userID)
	if err != nil {
		a.logger.Error("check mail exclusion", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "메일 발송 제외 여부를 확인할 수 없습니다.")
		return true
	}
	if organization == "" {
		return false
	}
	writeError(w, http.StatusConflict, "MAIL_BLOCKED_ORGANIZATION",
		"소속 조직("+organization+")이 메일 발송 제외 조직이라 이 계정으로는 메일을 보내지 않습니다.")
	return true
}
