package app

import "testing"

// A job whose files were every one of them a duplicate of something already
// imported sat on the history list as 검토 가능 for good: confirming answered
// IMPORT_FILE_NOT_READY, confirming nothing answered IMPORT_SELECTION_REQUIRED,
// and re-analysing answered NO_RETRYABLE_IMPORT. Reproduced on a deployment.

// guards: importJobStatus
func TestAJobWithNothingToReviewDoesNotAskToBeReviewed(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		total, failed, usable int
		want                  string
	}{
		{"보통", 3, 0, 3, "READY"},
		{"일부 실패", 3, 1, 2, "PARTIAL"},
		{"전부 실패", 3, 3, 0, "FAILED"},
		{"전부 중복", 2, 0, 0, "NOTHING_TO_IMPORT"},
		{"하나 실패·하나 중복이면 볼 것은 없어도 전부 실패는 아닙니다", 2, 1, 0, "PARTIAL"},
		{"하나만 살아남음", 3, 2, 1, "PARTIAL"},
		{"빈 작업", 0, 0, 0, "READY"},
	} {
		if got := importJobStatus(tc.total, tc.failed, tc.usable); got != tc.want {
			t.Errorf("%s: importJobStatus(%d,%d,%d) = %q, want %q", tc.name, tc.total, tc.failed, tc.usable, got, tc.want)
		}
	}
}

// The status the operator is invited to act on must be one confirmImportJob
// actually accepts, or the list sends them at a door that is locked.
func TestOnlyTheStatusesConfirmAcceptsInviteAction(t *testing.T) {
	for _, status := range []string{"READY", "PARTIAL"} {
		if importJobStatus(1, 0, 1) != "READY" && status == "READY" {
			t.Fatalf("READY is no longer reachable")
		}
	}
	if importJobStatus(2, 0, 0) == "READY" || importJobStatus(2, 0, 0) == "PARTIAL" {
		t.Errorf("a job with nothing to review is still invited to be confirmed")
	}
}
