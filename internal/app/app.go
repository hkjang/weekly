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
	capabilities    databaseCapabilities
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
	// Losing the key orphans the OIDC client secret, the AI API key and the
	// Confluence password at once. Starting anyway leaves a service that looks
	// healthy but cannot log anyone in through SSO, so the decision to discard
	// them has to be the operator's, not a silent side effect of a restart.
	storedSecrets, err := countEncryptedSecrets(ctx, db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("inspect encrypted settings: %w", err)
	}
	box, legacyBox, keySource, err := loadSecretBoxes(env.EncryptionKey, storedSecrets == 0 || env.AllowSecretReset)
	if err != nil {
		db.Close()
		if errors.Is(err, errSecretKeyMissing) {
			return nil, fmt.Errorf("%w: %d개의 비밀 설정이 저장되어 있지만 이를 복호화할 키가 없습니다. "+
				"%s 볼륨을 복구하거나, 기존에 사용하던 WEEKLY_ENCRYPTION_KEY를 설정하십시오. "+
				"비밀 설정을 모두 다시 입력할 것이라면 WEEKLY_ALLOW_SECRET_RESET=true로 기동하십시오",
				err, storedSecrets, stateDirectory)
		}
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
	if len(migration.Unavailable) > 0 && !env.AllowSecretReset {
		db.Close()
		return nil, fmt.Errorf("%d개의 비밀 설정을 현재 암호화 키로 복호화할 수 없습니다: %s. "+
			"이 설정을 암호화할 때 사용한 WEEKLY_ENCRYPTION_KEY를 설정하거나 %s 볼륨을 복구하십시오. "+
			"해당 값을 모두 다시 입력할 것이라면 WEEKLY_ALLOW_SECRET_RESET=true로 기동하십시오",
			len(migration.Unavailable), strings.Join(migration.Unavailable, ", "), stateDirectory)
	}
	if len(migration.Unavailable) > 0 {
		logger.Warn("secret settings cannot be decrypted and must be re-entered",
			"keys", migration.Unavailable, "reason", "WEEKLY_ALLOW_SECRET_RESET=true")
	}
	logger.Info("secret encryption initialized", "key_source", keySource, "stored_secrets", storedSecrets)
	if keySource == "state_volume" && storedSecrets > 0 {
		logger.Warn("secret encryption key lives only in the state volume; set WEEKLY_ENCRYPTION_KEY so upgrades survive a lost volume",
			"state_directory", stateDirectory)
	}
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
	a.capabilities = detectCapabilities(ctx, db)
	logger.Info("database capabilities detected", "pg_trgm", a.capabilities.Trigram, "pgvector", a.capabilities.Vector)
	// The password queue's size and what it reserves, because an operator
	// tuning the container's memory limit has no other way to see the figure
	// the process chose for itself.
	applyContainerMemoryLimit(logger)
	logger.Info("password hashing pool sized", "workers", cap(passwordWork),
		"reserved_mib", cap(passwordWork)*argonBytes>>20,
		"container_limit_mib", cgroupMemoryLimit()>>20)
	a.routes()
	// Off the startup path on purpose. Both of these walk the whole corpus, and
	// waiting for them meant the process did not answer /healthz until they were
	// done — 34 seconds for 50,000 report items, past the point where the
	// default Kubernetes liveness probe restarts the pod. Neither is needed for
	// the service to answer correctly: the backfill only adds links that the
	// runtime path already creates for new saves, and the integrity check only
	// reports.
	go a.startupTasks(ctx)
	go a.maintenance(ctx)
	go a.importWorker(ctx)
	go a.confluenceWorker(ctx)
	go a.embeddingWorker(ctx)
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
	a.mux.Handle("GET /api/v1/reports/previous", a.requireAuth(http.HandlerFunc(a.previousWeekPlan)))
	a.mux.Handle("POST /api/v1/reports/quality", a.requireAuth(a.csrf(http.HandlerFunc(a.reportQuality))))
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
	a.mux.Handle("GET /api/v1/reports/{id}/attachments", a.requireAuth(http.HandlerFunc(a.listAttachments)))
	a.mux.Handle("POST /api/v1/reports/{id}/attachments", a.requireAuth(a.csrf(http.HandlerFunc(a.uploadAttachments))))
	a.mux.Handle("GET /api/v1/reports/{id}/attachments/{attachmentId}", a.requireAuth(http.HandlerFunc(a.serveAttachment)))
	a.mux.Handle("PATCH /api/v1/reports/{id}/attachments/{attachmentId}", a.requireAuth(a.csrf(http.HandlerFunc(a.updateAttachment))))
	a.mux.Handle("DELETE /api/v1/reports/{id}/attachments/{attachmentId}", a.requireAuth(a.csrf(http.HandlerFunc(a.deleteAttachment))))
	a.mux.Handle("GET /api/v1/search", a.requireAuth(http.HandlerFunc(a.searchReports)))
	a.mux.Handle("GET /api/v1/team/reports", a.requireRole("TEAM_LEADER", "ORG_MANAGER", "ADMIN")(http.HandlerFunc(a.teamReports)))
	a.mux.Handle("GET /api/v1/team/members", a.requireRole("TEAM_LEADER", "ORG_MANAGER", "ADMIN")(http.HandlerFunc(a.teamMembers)))
	a.mux.Handle("GET /api/v1/work-items", a.requireAuth(http.HandlerFunc(a.listWorkItems)))
	a.mux.Handle("GET /api/v1/work-items/{id}", a.requireAuth(http.HandlerFunc(a.getWorkItem)))
	a.mux.Handle("POST /api/v1/work-items/{id}/merge", a.requireAuth(a.csrf(http.HandlerFunc(a.mergeWorkItem))))
	a.mux.Handle("GET /api/v1/work-items/{id}/decisions", a.requireAuth(http.HandlerFunc(a.listWorkItemDecisions)))
	a.mux.Handle("POST /api/v1/work-items/{id}/decisions", a.requireAuth(a.csrf(http.HandlerFunc(a.createWorkItemDecision))))
	a.mux.Handle("POST /api/v1/work-items/{id}/decisions/suggest", a.requireAuth(a.csrf(http.HandlerFunc(a.suggestDecisions))))
	a.mux.Handle("GET /api/v1/evidence/uses", a.requireAuth(http.HandlerFunc(a.evidenceUses)))
	a.mux.Handle("GET /api/v1/work-items/{id}/links", a.requireAuth(http.HandlerFunc(a.listWorkItemLinks)))
	a.mux.Handle("POST /api/v1/work-items/{id}/links", a.requireAuth(a.csrf(http.HandlerFunc(a.createWorkItemLink))))
	a.mux.Handle("DELETE /api/v1/work-item-links/{linkId}", a.requireAuth(a.csrf(http.HandlerFunc(a.deleteWorkItemLink))))
	a.mux.Handle("GET /api/v1/decisions/open", a.requireAuth(http.HandlerFunc(a.openFollowUps)))
	a.mux.Handle("PATCH /api/v1/decisions/{id}", a.requireAuth(a.csrf(http.HandlerFunc(a.updateDecision))))
	a.mux.Handle("DELETE /api/v1/decisions/{id}", a.requireAuth(a.csrf(http.HandlerFunc(a.deleteDecision))))
	a.mux.Handle("POST /api/v1/work-items/{id}/split", a.requireAuth(a.csrf(http.HandlerFunc(a.splitWorkItem))))
	a.mux.Handle("PUT /api/v1/work-items/{id}/due-date", a.requireAuth(a.csrf(http.HandlerFunc(a.setWorkItemDueDate))))
	a.mux.Handle("GET /api/v1/rollups", a.requireAuth(http.HandlerFunc(a.periodRollup)))
	a.mux.Handle("GET /api/v1/rollups/export.csv", a.requireAuth(http.HandlerFunc(a.exportRollupCSV)))
	a.mux.Handle("GET /api/v1/rollups/export.pptx", a.requireAuth(http.HandlerFunc(a.exportRollupPPTX)))
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
	a.mux.Handle("GET /api/v1/work-items/search", a.requireAuth(http.HandlerFunc(a.searchWorkItems)))
	a.mux.Handle("GET /api/v1/meeting", a.requireAuth(http.HandlerFunc(a.meetingMode)))
	a.mux.Handle("GET /api/v1/changes", a.requireAuth(http.HandlerFunc(a.weeklyChanges)))
	a.mux.Handle("GET /api/v1/present/theme", a.requireAuth(http.HandlerFunc(a.presentThemeInfo)))
	a.mux.Handle("GET /api/v1/digest", a.requireRole("TEAM_LEADER", "ORG_MANAGER", "ADMIN")(http.HandlerFunc(a.executiveDigest)))
	a.mux.Handle("GET /api/v1/insights/work-graph", a.requireRole("TEAM_LEADER", "ORG_MANAGER", "ADMIN")(http.HandlerFunc(a.workGraph)))
	a.mux.Handle("GET /api/v1/handover", a.requireAuth(http.HandlerFunc(a.handover)))
	a.mux.Handle("GET /api/v1/admin/embeddings", a.requireRole("ADMIN")(http.HandlerFunc(a.embeddingStatus)))
	a.mux.Handle("POST /api/v1/admin/embeddings/rebuild", a.requireRole("ADMIN")(a.csrf(http.HandlerFunc(a.rebuildEmbeddings))))
	a.mux.Handle("GET /api/v1/admin/analytics/keywords", a.requireRole("ADMIN")(http.HandlerFunc(a.analyticsKeywords)))
	a.mux.Handle("GET /api/v1/admin/analytics/organizations", a.requireRole("ADMIN")(http.HandlerFunc(a.analyticsOrganizations)))
	a.mux.Handle("GET /api/v1/admin/analytics/participation", a.requireRole("ADMIN")(http.HandlerFunc(a.analyticsParticipation)))
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

// startupTasks runs the once-per-boot housekeeping after the server is already
// answering, in the order an operator reads the log: link what needs linking,
// then say whether the attachment files are all there.
func (a *App) startupTasks(ctx context.Context) {
	a.backfillWorkItems(ctx)
	a.checkAttachmentIntegrity(ctx)
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
			a.cleanupAttachmentFiles(ctx)
			a.pruneLoginAttempts(ctx)
			// Zero means an operator has chosen to keep everything, so the
			// trail is only trimmed when a policy asked for it.
			if days := a.settingInt(ctx, "audit.retention_days", 365); days > 0 {
				if _, err := a.db.Exec(ctx, `DELETE FROM audit_logs WHERE created_at < now() - make_interval(days => $1)`, days); err != nil {
					a.logger.Warn("prune audit logs", "error", err)
				}
			}
		}
	}
}

func asString(value any) string { return strings.TrimSpace(fmt.Sprint(value)) }

var errNotFound = errors.New("not found")
