package app

import (
	"fmt"
	"net/http"
	"testing"
)

// Editing or deleting a report that does not exist answers exactly what editing
// somebody else's answers. That is deliberate: told apart, the two replies let
// anyone walk the identifiers and learn which reports exist and roughly how many
// there are. The rule is not "refuse" — it is "refuse the same way".

// guards: updateReport
func TestAMissingReportIsRefusedLikeSomebodyElses(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("existence_author", "USER", nil)
	stranger := server.createUser("existence_stranger", "USER", nil)
	reportID, version := server.draft(author, "2026-08-24", "있는 보고서")

	body := map[string]any{"summary": "고쳐 버림", "version": version, "items": []any{}}
	theirs := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID), body, stranger)
	missing := server.request(http.MethodPut, "/api/v1/reports/999999", body, stranger)

	if theirs.Code != missing.Code {
		t.Errorf("somebody else's report answered %d and a missing one %d — the difference names what exists",
			theirs.Code, missing.Code)
	}
	if refusal(t, theirs) != refusal(t, missing) {
		t.Errorf("the two refusals read differently:\n  남의 것: %s\n  없는 것: %s",
			refusal(t, theirs), refusal(t, missing))
	}
	if theirs.Code == http.StatusOK {
		t.Fatal("a stranger edited a report they do not own")
	}
}

// guards: deleteReport
func TestDeletingAMissingReportIsRefusedLikeSomebodyElses(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("existence_author", "USER", nil)
	stranger := server.createUser("existence_stranger", "USER", nil)
	reportID, version := server.draft(author, "2026-08-24", "있는 보고서")

	theirs := server.request(http.MethodDelete,
		fmt.Sprintf("/api/v1/reports/%d?version=%d", reportID, version), nil, stranger)
	missing := server.request(http.MethodDelete,
		fmt.Sprintf("/api/v1/reports/999999?version=%d", version), nil, stranger)

	if theirs.Code != missing.Code {
		t.Errorf("somebody else's report answered %d and a missing one %d", theirs.Code, missing.Code)
	}
	if refusal(t, theirs) != refusal(t, missing) {
		t.Errorf("the two refusals read differently:\n  남의 것: %s\n  없는 것: %s",
			refusal(t, theirs), refusal(t, missing))
	}
	if theirs.Code == http.StatusOK {
		t.Fatal("a stranger deleted a report they do not own")
	}

	// And the report the stranger was refused is still there.
	if own := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d", reportID), nil, author); own.Code != http.StatusOK {
		t.Errorf("the author's report is gone after a refused delete: %d", own.Code)
	}
}
