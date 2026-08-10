package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"builtAt"`
}

type Options struct {
	Logger          *slog.Logger
	Web             fs.FS
	Build           BuildInfo
	DefaultPPTX     []byte
	DefaultPPTXName string
}

type App struct {
	db              *pgxpool.Pool
	logger          *slog.Logger
	web             fs.FS
	build           BuildInfo
	box             *secretBox
	mux             *http.ServeMux
	defaultPPTX     []byte
	defaultPPTXName string
	importWake      chan struct{}
	confluenceWake  chan struct{}
}

func New(ctx context.Context, options Options) (*App, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	env, err := loadEnvironment()
	if err != nil {
		return nil, err
	}
	db, err := openDatabase(ctx, env.PostgresDSN)
	if err != nil {
		return nil, err
	}
	box, legacyBox, keySource, err := loadSecretBoxes(env.EncryptionKey)
	if err != nil {
		db.Close()
		return nil, err
	}
	migration, migrationErr := migrateSecretSettings(ctx, db, box, legacyBox)
	if migrationErr != nil {
		db.Close()
		return nil, fmt.Errorf("migrate encrypted settings: %w", migrationErr)
	}
	if migration.Migrated > 0 {
		logger.Info("secret settings re-encrypted", "count", migration.Migrated)
	}
	if len(migration.Unavailable) > 0 {
		logger.Error("secret settings cannot be decrypted and must be re-entered", "keys", migration.Unavailable)
	}
	logger.Info("secret encryption initialized", "key_source", keySource)
	defaultPPTX := options.DefaultPPTX
	defaultPPTXName := options.DefaultPPTXName
	if len(defaultPPTX) == 0 {
		defaultPPTX, err = referenceStylePPTX()
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("create built-in PPTX template: %w", err)
		}
		defaultPPTXName = "Weekly-AI엔지니어링-4슬라이드-기본.pptx"
	} else if defaultPPTXName == "" {
		defaultPPTXName = "1월5주간업무보고_AI엔지니어링.pptx"
	}
	a := &App{db: db, logger: logger, web: options.Web, build: options.Build, box: box, mux: http.NewServeMux(), defaultPPTX: defaultPPTX, defaultPPTXName: defaultPPTXName, importWake: make(chan struct{}, 1), confluenceWake: make(chan struct{}, 1)}
	if err := a.bootstrapAdmin(ctx, env.BootstrapAdmin, env.BootstrapPassword); err != nil {
		db.Close()
		return nil, err
	}
	a.routes()
	go a.maintenance(ctx)
	go a.importWorker(ctx)
	go a.confluenceWorker(ctx)
	return a, nil
}

func (a *App) Close() { a.db.Close() }

func (a *App) Handler() http.Handler {
	return a.recoverMiddleware(a.securityHeaders(a.requestMetrics(a.mux)))
}

func (a *App) routes() {
	a.mux.HandleFunc("GET /healthz", a.health)
	a.mux.HandleFunc("GET /readyz", a.ready)
	a.mux.HandleFunc("GET /api/v1/version", a.versionInfo)
	a.mux.HandleFunc("GET /api/v1/auth/providers", a.authProviders)
	a.mux.HandleFunc("POST /api/v1/auth/login", a.login)
	a.mux.HandleFunc("POST /api/v1/auth/logout", a.logout)
	a.mux.HandleFunc("GET /api/v1/auth/oidc/start", a.oidcStart)
	a.mux.HandleFunc("GET /api/v1/auth/oidc/callback", a.oidcCallback)
	a.mux.Handle("GET /api/v1/me", a.requireAuth(http.HandlerFunc(a.me)))

	a.mux.Handle("GET /api/v1/reports/current", a.requireAuth(http.HandlerFunc(a.currentReport)))
	a.mux.Handle("GET /api/v1/reports", a.requireAuth(http.HandlerFunc(a.listReports)))
	a.mux.Handle("GET /api/v1/reports/{id}", a.requireAuth(http.HandlerFunc(a.getReport)))
	a.mux.Handle("POST /api/v1/reports", a.requireAuth(a.csrf(http.HandlerFunc(a.createReport))))
	a.mux.Handle("PUT /api/v1/reports/{id}", a.requireAuth(a.csrf(http.HandlerFunc(a.updateReport))))
	a.mux.Handle("DELETE /api/v1/reports/{id}", a.requireAuth(a.csrf(http.HandlerFunc(a.deleteReport))))
	a.mux.Handle("POST /api/v1/reports/{id}/clone", a.requireAuth(a.csrf(http.HandlerFunc(a.cloneReport))))
	a.mux.Handle("POST /api/v1/reports/{id}/submit", a.requireAuth(a.csrf(http.HandlerFunc(a.submitReport))))
	a.mux.Handle("POST /api/v1/reports/{id}/approve", a.requireRole("TEAM_LEADER", "ORG_MANAGER", "ADMIN")(a.csrf(http.HandlerFunc(a.approveReport))))
	a.mux.Handle("POST /api/v1/reports/{id}/reject", a.requireRole("TEAM_LEADER", "ORG_MANAGER", "ADMIN")(a.csrf(http.HandlerFunc(a.rejectReport))))
	a.mux.Handle("POST /api/v1/reports/{id}/comments", a.requireAuth(a.csrf(http.HandlerFunc(a.addComment))))
	a.mux.Handle("GET /api/v1/reports/{id}/export.pptx", a.requireAuth(http.HandlerFunc(a.exportReportPPTX)))
	a.mux.Handle("GET /api/v1/team/reports", a.requireRole("TEAM_LEADER", "ORG_MANAGER", "ADMIN")(http.HandlerFunc(a.teamReports)))
	a.mux.Handle("POST /api/v1/ai/reports/parse-text", a.requireAuth(a.csrf(http.HandlerFunc(a.parseAIText))))
	a.mux.Handle("POST /api/v1/import/pptx", a.requireAuth(a.csrf(http.HandlerFunc(a.uploadImportPPTX))))
	a.mux.Handle("GET /api/v1/import/history", a.requireAuth(http.HandlerFunc(a.listImportJobs)))
	a.mux.Handle("GET /api/v1/import/{id}", a.requireAuth(http.HandlerFunc(a.getImportJob)))
	a.mux.Handle("POST /api/v1/import/{id}/analyze", a.requireAuth(a.csrf(http.HandlerFunc(a.retryImportJob))))
	a.mux.Handle("POST /api/v1/import/{id}/confirm", a.requireAuth(a.csrf(http.HandlerFunc(a.confirmImportJob))))
	a.mux.Handle("GET /api/v1/reports/current/candidates", a.requireAuth(http.HandlerFunc(a.currentConfluenceCandidates)))
	a.mux.Handle("PATCH /api/v1/report-candidates/{id}", a.requireAuth(a.csrf(http.HandlerFunc(a.updateConfluenceCandidate))))
	a.mux.Handle("DELETE /api/v1/report-candidates/{id}", a.requireAuth(a.csrf(http.HandlerFunc(a.deleteConfluenceCandidate))))
	a.mux.Handle("GET /api/v1/report-candidates/{id}/sources", a.requireAuth(http.HandlerFunc(a.confluenceCandidateSources)))
	a.mux.Handle("POST /api/v1/report-candidates/accept", a.requireAuth(a.csrf(http.HandlerFunc(a.acceptConfluenceCandidates))))

	a.mux.Handle("GET /api/v1/keys", a.requireAuth(http.HandlerFunc(a.listKeys)))
	a.mux.Handle("POST /api/v1/keys", a.requireAuth(a.csrf(http.HandlerFunc(a.createKey))))
	a.mux.Handle("POST /api/v1/keys/rotate", a.requireAuth(a.csrf(http.HandlerFunc(a.rotateKeys))))
	a.mux.Handle("DELETE /api/v1/keys/{id}", a.requireAuth(a.csrf(http.HandlerFunc(a.revokeKey))))

	a.mux.Handle("GET /api/v1/admin/settings", a.requireRole("ADMIN")(http.HandlerFunc(a.adminSettings)))
	a.mux.Handle("PUT /api/v1/admin/settings", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.updateSettings))))
	a.mux.Handle("POST /api/v1/admin/settings/oidc/test", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.testOIDC))))
	a.mux.Handle("POST /api/v1/admin/settings/ai/test", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.testAI))))
	a.mux.Handle("POST /api/v1/admin/settings/confluence/test", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.testConfluence))))
	a.mux.Handle("GET /api/v1/admin/users", a.requireRole("ADMIN")(http.HandlerFunc(a.adminUsers)))
	a.mux.Handle("POST /api/v1/admin/users", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.createUser))))
	a.mux.Handle("PUT /api/v1/admin/users/{id}", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.updateUser))))
	a.mux.Handle("GET /api/v1/admin/organizations", a.requireRole("ADMIN")(http.HandlerFunc(a.adminOrganizations)))
	a.mux.Handle("POST /api/v1/admin/organizations", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.createOrganization))))
	a.mux.Handle("GET /api/v1/admin/audit", a.requireRole("ADMIN")(http.HandlerFunc(a.auditLogs)))
	a.mux.Handle("GET /api/v1/admin/pptx-template", a.requireRole("ADMIN")(http.HandlerFunc(a.pptxTemplateInfo)))
	a.mux.Handle("POST /api/v1/admin/pptx-template", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.uploadPPTXTemplate))))
	a.mux.Handle("DELETE /api/v1/admin/pptx-template", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.resetPPTXTemplate))))
	a.mux.Handle("POST /api/v1/admin/confluence/sync", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.forceConfluenceSync))))
	a.mux.Handle("GET /api/v1/admin/confluence/sync/status", a.requireRole("ADMIN")(http.HandlerFunc(a.adminConfluenceStatus)))
	a.mux.Handle("GET /api/v1/admin/confluence/users/mappings", a.requireRole("ADMIN")(http.HandlerFunc(a.adminConfluenceMappings)))
	a.mux.Handle("GET /api/v1/admin/confluence/users/unmapped", a.requireRole("ADMIN")(http.HandlerFunc(a.adminUnmappedConfluenceUsers)))
	a.mux.Handle("PUT /api/v1/admin/confluence/users/{id}/mapping", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.updateConfluenceMapping))))

	a.mux.Handle("GET /api/v1/analytics/overview", a.requireRole("TEAM_LEADER", "ORG_MANAGER", "ADMIN")(http.HandlerFunc(a.analyticsOverview)))
	a.mux.Handle("GET /api/v1/analytics/endpoints", a.requireRole("ADMIN")(http.HandlerFunc(a.analyticsEndpoints)))
	a.mux.Handle("POST /mcp", a.requireAuth(http.HandlerFunc(a.mcp)))
	a.mux.Handle("GET /mcp", a.requireAuth(http.HandlerFunc(a.mcpGet)))
	a.mux.HandleFunc("/", a.serveSPA)
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "데이터베이스에 연결할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *App) versionInfo(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, a.build)
}

func (a *App) serveSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mcp") {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "요청한 API가 없습니다.")
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	body, err := fs.ReadFile(a.web, name)
	if err != nil {
		name = "index.html"
		body, err = fs.ReadFile(a.web, name)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "화면을 찾을 수 없습니다.")
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	_, _ = w.Write(body)
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Success bool      `json:"success"`
	Data    any       `json:"data"`
	Error   *apiError `json:"error,omitempty"`
	TraceID string    `json:"traceId"`
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Success: true, Data: data, TraceID: w.Header().Get("X-Trace-ID")})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Success: false, Error: &apiError{Code: code, Message: message}, TraceID: w.Header().Get("X-Trace-ID")})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "요청 형식이 올바르지 않습니다.")
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "식별자가 올바르지 않습니다.")
		return 0, false
	}
	return id, true
}

func (a *App) maintenance(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = a.db.Exec(ctx, `DELETE FROM user_sessions WHERE expires_at < now(); DELETE FROM oidc_login_states WHERE expires_at < now()`)
			retention := a.settingInt(ctx, "analytics.retention_days", 90)
			_, _ = a.db.Exec(ctx, `DELETE FROM api_request_metrics WHERE bucket < now() - ($1 || ' days')::interval`, retention)
			a.cleanupImportSources(ctx)
		}
	}
}

func asString(value any) string { return strings.TrimSpace(fmt.Sprint(value)) }

var errNotFound = errors.New("not found")
