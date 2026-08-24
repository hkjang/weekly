package app

import "sync"

// Reporting a standing condition without filling the log with it.
//
// A cap that truncates a list is not an event, it is a state: the deployment is
// bigger than a limit somebody chose. Logging it per request writes that state
// out once for every reader, and on 300 people a Monday morning produced sixty
// identical lines in three and a half seconds — which were, during that whole
// burst, the only lines the service wrote. Anything that had actually gone
// wrong would have been buried in a report that everything was working.
//
// So each condition is written once per process and then held. Restarting says
// it again, which is when an operator is looking anyway. The numbers behind it
// are not lost: every truncating response already carries its own total and
// limit, and the request metrics table counts the calls.
type conditionLog struct {
	mu    sync.Mutex
	seen  map[string]bool
	inner logger
}

// logger is the slice of *slog.Logger this uses, kept narrow so a test can
// stand in for it.
type logger interface {
	Info(msg string, args ...any)
}

func newConditionLog(inner logger) *conditionLog {
	return &conditionLog{seen: map[string]bool{}, inner: inner}
}

// once writes msg the first time this key is reported and never again.
//
// The key is the condition, not the values: "the report list is truncating" is
// one condition however many different totals it happens with.
func (c *conditionLog) once(key, msg string, args ...any) {
	if c == nil || c.inner == nil {
		return
	}
	c.mu.Lock()
	already := c.seen[key]
	if !already {
		c.seen[key] = true
	}
	c.mu.Unlock()
	if already {
		return
	}
	c.inner.Info(msg, append(args, "repeats", "no — 이 조건은 프로세스당 한 번만 기록합니다")...)
}
