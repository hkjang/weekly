package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"
)

type contextKey string

const (
	principalContext contextKey = "principal"
	traceContext     contextKey = "trace"
)

type principal struct {
	ID             int64    `json:"id"`
	Username       string   `json:"username"`
	DisplayName    string   `json:"displayName"`
	Email          string   `json:"email"`
	Role           string   `json:"role"`
	OrganizationID *int64   `json:"organizationId"`
	KeyVersion     int      `json:"keyVersion"`
	AuthType       string   `json:"-"`
	Scopes         []string `json:"-"`
}

func currentPrincipal(ctx context.Context) *principal {
	p, _ := ctx.Value(principalContext).(*principal)
	return p
}

func traceIDFromContext(ctx context.Context) string {
	if trace, ok := ctx.Value(traceContext).(string); ok {
		return trace
	}
	return ""
}

func (a *App) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8)
		_, _ = rand.Read(buf)
		trace := fmt.Sprintf("%x", buf)
		ctx := context.WithValue(r.Context(), traceContext, trace)
		w.Header().Set("X-Trace-ID", trace)
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("request panic", "trace", trace, "error", recovered, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "서버 오류가 발생했습니다.")
			}
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

type metricsWriter struct {
	http.ResponseWriter
	status int
}

func (w *metricsWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (a *App) requestMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		mw := &metricsWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(mw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		duration := time.Since(started).Milliseconds()
		if duration < 0 {
			duration = 0
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = a.db.Exec(ctx, `INSERT INTO api_request_metrics(bucket, method, route, status, request_count, duration_ms_sum, duration_ms_max)
			VALUES(date_trunc('hour', now()),$1,$2,$3,1,$4,$4)
			ON CONFLICT(bucket,method,route,status) DO UPDATE SET request_count=api_request_metrics.request_count+1,
			duration_ms_sum=api_request_metrics.duration_ms_sum+EXCLUDED.duration_ms_sum,
			duration_ms_max=GREATEST(api_request_metrics.duration_ms_max,EXCLUDED.duration_ms_max)`, r.Method, route, mw.status, duration)
	})
}

func (a *App) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "로그인이 필요합니다.")
			return
		}
		if p.AuthType == "api_key" && !apiKeyRequestAllowed(p, r) {
			writeError(w, http.StatusForbidden, "API_KEY_SCOPE_DENIED", "이 API 키로 요청한 작업을 수행할 수 없습니다.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContext, p)))
	})
}

func apiKeyRequestAllowed(p *principal, r *http.Request) bool {
	if r.URL.Path == "/mcp" {
		return contains(p.Scopes, "mcp:read")
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch {
	case r.URL.Path == "/api/v1/me" || r.URL.Path == "/api/v1/version":
		return true
	case strings.HasPrefix(r.URL.Path, "/api/v1/reports") || strings.HasPrefix(r.URL.Path, "/api/v1/team/reports") || strings.HasPrefix(r.URL.Path, "/api/v1/rollups") || strings.HasPrefix(r.URL.Path, "/api/v1/search"):
		return contains(p.Scopes, "reports:read")
	case strings.HasPrefix(r.URL.Path, "/api/v1/analytics"):
		return contains(p.Scopes, "analytics:read")
	default:
		return false
	}
}

func (a *App) requireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, role := range roles {
		allowed[role] = true
	}
	return func(next http.Handler) http.Handler {
		return a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := currentPrincipal(r.Context())
			if p == nil || !allowed[p.Role] {
				writeError(w, http.StatusForbidden, "FORBIDDEN", "요청을 수행할 권한이 없습니다.")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

func (a *App) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := currentPrincipal(r.Context())
		if p != nil && p.AuthType == "session" {
			origin := r.Header.Get("Origin")
			if origin == "" {
				origin = r.Header.Get("Referer")
			}
			parsed, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
				writeError(w, http.StatusForbidden, "CSRF_REJECTED", "요청 출처를 확인할 수 없습니다.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
