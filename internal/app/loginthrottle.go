package app

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Rate limiting for local logins.
//
// Until now nothing stood between a caller and an unlimited number of password
// guesses at /api/v1/auth/login. A fixed 250ms pause on failure slows a single
// sequential guesser and does nothing at all to a parallel one.
//
// The counter lives in PostgreSQL rather than in process memory. In memory it
// would reset on every restart, which hands an attacker a way to clear it, and
// it would count separately in each replica.

type loginThrottle struct {
	// Blocked is true when the attempt must be refused without checking the
	// password at all.
	Blocked bool
	// RetryAfter is how long the window still has to run. Reported to the caller
	// so a locked-out user knows to wait rather than to keep trying.
	RetryAfter time.Duration
	// Failures is the count within the window, used to scale the delay on a
	// wrong password.
	Failures int
}

// loginThrottleFor reports whether this attempt may proceed.
//
// Two counters, with different defaults on purpose. The account counter is on by
// default: guessing one person's password is the attack this exists to stop, and
// the only user it can inconvenience is the owner of that account.
//
// The address counter is off by default. In the deployments this product is
// built for, everyone arrives through one office NAT or one reverse proxy, so a
// shared address is the norm and a threshold low enough to matter would lock out
// a whole floor over other people's typos. An operator who knows their network
// gives out distinct client addresses can turn it on.
func (a *App) loginThrottleFor(ctx context.Context, username, address string) loginThrottle {
	limit := a.settingInt(ctx, "auth.max_login_attempts", 10)
	addressLimit := a.settingInt(ctx, "auth.max_login_attempts_per_ip", 0)
	minutes := a.settingInt(ctx, "auth.lockout_minutes", 15)
	if minutes <= 0 || (limit <= 0 && addressLimit <= 0) {
		return loginThrottle{}
	}
	window := time.Duration(minutes) * time.Minute

	var accountFailures, addressFailures int
	var oldest *time.Time
	err := a.db.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE lower(username)=lower($1)),
		count(*) FILTER (WHERE $2 <> '' AND host(ip_address)=$2),
		min(created_at) FILTER (WHERE lower(username)=lower($1))
		FROM login_attempts WHERE created_at > now() - make_interval(mins => $3)`,
		username, address, minutes).Scan(&accountFailures, &addressFailures, &oldest)
	if err != nil {
		// A counter that cannot be read must not become a way in. Failing closed
		// on the whole endpoint would be worse — one database hiccup would lock
		// everybody out — so the attempt proceeds and the failure is logged by
		// the caller.
		a.logger.Warn("read login attempts", "error", err)
		return loginThrottle{}
	}

	result := loginThrottle{Failures: accountFailures}
	if limit > 0 && accountFailures >= limit {
		result.Blocked = true
	}
	if addressLimit > 0 && addressFailures >= addressLimit {
		result.Blocked = true
	}
	if result.Blocked && oldest != nil {
		if remaining := time.Until(oldest.Add(window)); remaining > 0 {
			result.RetryAfter = remaining
		}
	}
	return result
}

func (a *App) recordLoginFailure(ctx context.Context, username, address string) {
	var ip *string
	if address != "" {
		ip = &address
	}
	if _, err := a.db.Exec(ctx, `INSERT INTO login_attempts(username, ip_address) VALUES($1, $2)`,
		trimRunes(strings.TrimSpace(username), 120), ip); err != nil {
		a.logger.Warn("record login failure", "error", err)
	}
}

func (a *App) clearLoginFailures(ctx context.Context, username string) {
	if _, err := a.db.Exec(ctx, `DELETE FROM login_attempts WHERE lower(username)=lower($1)`, username); err != nil {
		a.logger.Warn("clear login failures", "error", err)
	}
}

// loginFailureDelay grows with the number of recent failures so that guessing
// gets slower long before it gets blocked, and stays bounded so a flood of
// attempts cannot tie up every connection in the pool.
func loginFailureDelay(failures int) time.Duration {
	delay := 250 * time.Millisecond
	for range failures {
		delay *= 2
		if delay >= 2*time.Second {
			return 2 * time.Second
		}
	}
	return delay
}

// pruneLoginAttempts drops rows past any window an operator could set. The table
// is a counter and must not grow into a second, unmanaged audit log.
func (a *App) pruneLoginAttempts(ctx context.Context) {
	if _, err := a.db.Exec(ctx, `DELETE FROM login_attempts WHERE created_at < now() - interval '1 day'`); err != nil {
		a.logger.Warn("prune login attempts", "error", err)
	}
}

func retryAfterSeconds(d time.Duration) int {
	seconds := int(d.Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func writeLoginBlocked(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
	minutes := max(1, int(retryAfter.Round(time.Minute).Minutes()))
	writeError(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS",
		"로그인 시도가 너무 많습니다. "+strconv.Itoa(minutes)+"분 후에 다시 시도하세요.")
}
