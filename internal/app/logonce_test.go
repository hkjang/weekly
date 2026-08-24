package app

import (
	"strings"
	"sync"
	"testing"
)

type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingLogger) Info(msg string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, msg)
}

// A cap that truncates is a state, not an event. Written per request it
// produced sixty identical lines in three and a half seconds under load — and
// those were the only lines the service wrote, so anything that had actually
// gone wrong would have been buried in a report that everything was fine.
// guards: once=100
func TestConditionIsLoggedOncePerProcess(t *testing.T) {
	sink := &recordingLogger{}
	conditions := newConditionLog(sink)
	for call := 0; call < 500; call++ {
		conditions.once("report-list-truncated", "report list truncated", "total", 400+call)
	}
	if len(sink.lines) != 1 {
		t.Fatalf("wrote %d lines for one condition, want 1", len(sink.lines))
	}
	if sink.lines[0] != "report list truncated" {
		t.Errorf("wrote %q", sink.lines[0])
	}

	// A condition worth reporting must never be the reason a request dies. The
	// nil receiver is reachable wherever an App was built without a logger.
	var missing *conditionLog
	missing.once("no-logger", "there is nobody to tell")
	(&conditionLog{}).once("no-inner", "there is nobody to tell")
}

// Two different conditions are two different things to know about. Holding one
// must not silence the other.
// guards: once
func TestDifferentConditionsAreEachReported(t *testing.T) {
	sink := &recordingLogger{}
	conditions := newConditionLog(sink)
	for call := 0; call < 50; call++ {
		conditions.once("report-list-truncated", "report list truncated")
		conditions.once("work-graph-truncated", "work graph truncated")
	}
	if len(sink.lines) != 2 {
		t.Fatalf("wrote %d lines for two conditions, want 2: %v", len(sink.lines), sink.lines)
	}
	joined := strings.Join(sink.lines, "|")
	if !strings.Contains(joined, "report list truncated") || !strings.Contains(joined, "work graph truncated") {
		t.Errorf("one of the two conditions never got out: %v", sink.lines)
	}
}

// Requests arrive concurrently, so the latch has to hold under a race — this
// runs with -race in CI.
// guards: once
func TestConditionLogIsSafeUnderConcurrentRequests(t *testing.T) {
	sink := &recordingLogger{}
	conditions := newConditionLog(sink)
	var wg sync.WaitGroup
	for caller := 0; caller < 200; caller++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conditions.once("same", "the same condition")
		}()
	}
	wg.Wait()
	if len(sink.lines) != 1 {
		t.Errorf("200 concurrent callers wrote %d lines, want 1", len(sink.lines))
	}
}
