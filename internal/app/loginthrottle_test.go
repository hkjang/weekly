package app

import (
	"testing"
	"time"
)

func TestLoginFailureDelayGrowsAndStaysBounded(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 250 * time.Millisecond},
		{1, 500 * time.Millisecond},
		{2, time.Second},
		{3, 2 * time.Second},
		// The cap matters as much as the growth: without it a burst of attempts
		// would hold connections open long enough to be its own outage.
		{9, 2 * time.Second},
		{1000, 2 * time.Second},
	}
	for _, item := range cases {
		if got := loginFailureDelay(item.failures); got != item.want {
			t.Errorf("loginFailureDelay(%d)=%v want=%v", item.failures, got, item.want)
		}
	}
}

func TestRetryAfterSecondsNeverReportsZero(t *testing.T) {
	// Retry-After: 0 reads as "try again immediately", which is the opposite of
	// what a block means.
	if got := retryAfterSeconds(0); got != 1 {
		t.Errorf("retryAfterSeconds(0)=%d want=1", got)
	}
	if got := retryAfterSeconds(90 * time.Second); got != 90 {
		t.Errorf("retryAfterSeconds(90s)=%d want=90", got)
	}
}
