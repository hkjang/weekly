package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

const sessionCookie = "weekly_session"

func (a *App) bootstrapAdmin(ctx context.Context, username, password string) error {
	var count int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='ADMIN'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(ctx, `INSERT INTO users(username, display_name, password_hash, role, organization_id)
		VALUES($1,$1,$2,'ADMIN',(SELECT id FROM organizations WHERE code='DEFAULT'))
		ON CONFLICT(username) DO UPDATE SET role='ADMIN', active=true,
		password_hash=CASE WHEN users.password_hash IS NULL THEN EXCLUDED.password_hash ELSE users.password_hash END`, username, hash)
	if err == nil {
		a.logger.Info("bootstrap administrator ensured", "username", username)
	}
	return err
}

func (a *App) authenticate(r *http.Request) (*principal, error) {
	ctx := r.Context()
	if authorization := r.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		if !strings.HasPrefix(token, "wky_") {
			return nil, errors.New("unsupported bearer token")
		}
		p := &principal{AuthType: "api_key"}
		err := a.db.QueryRow(ctx, `SELECT u.id,u.username,u.display_name,coalesce(u.email,''),u.role,u.organization_id,u.key_version,k.scopes
			FROM personal_api_keys k JOIN users u ON u.id=k.user_id
			WHERE k.token_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND u.active=true AND k.key_version=u.key_version`, tokenHash(token)).
			Scan(&p.ID, &p.Username, &p.DisplayName, &p.Email, &p.Role, &p.OrganizationID, &p.KeyVersion, &p.Scopes)
		if err != nil {
			return nil, err
		}
		_, _ = a.db.Exec(ctx, `UPDATE personal_api_keys SET last_used_at=now() WHERE token_hash=$1`, tokenHash(token))
		return p, nil
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, errors.New("session not found")
	}
	p := &principal{AuthType: "session"}
	err = a.db.QueryRow(ctx, `SELECT u.id,u.username,u.display_name,coalesce(u.email,''),u.role,u.organization_id,u.key_version
		FROM user_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.expires_at>now() AND u.active=true`, tokenHash(cookie.Value)).
		Scan(&p.ID, &p.Username, &p.DisplayName, &p.Email, &p.Role, &p.OrganizationID, &p.KeyVersion)
	if err != nil {
		return nil, err
	}
	_, _ = a.db.Exec(ctx, `UPDATE user_sessions SET last_seen_at=now() WHERE token_hash=$1 AND last_seen_at < now() - interval '5 minutes'`, tokenHash(cookie.Value))
	return p, nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.settingBool(r.Context(), "auth.local_enabled", true) {
		writeError(w, http.StatusForbidden, "LOCAL_LOGIN_DISABLED", "로컬 로그인이 비활성화되었습니다.")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	p := principal{AuthType: "session"}
	var passwordHash *string
	err := a.db.QueryRow(r.Context(), `SELECT id,username,display_name,coalesce(email,''),role,organization_id,key_version,password_hash
		FROM users WHERE lower(username)=lower($1) AND active=true`, strings.TrimSpace(input.Username)).
		Scan(&p.ID, &p.Username, &p.DisplayName, &p.Email, &p.Role, &p.OrganizationID, &p.KeyVersion, &passwordHash)
	if err != nil || passwordHash == nil || !verifyPassword(*passwordHash, input.Password) {
		time.Sleep(250 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "아이디 또는 비밀번호가 올바르지 않습니다.")
		return
	}
	if err := a.issueSession(w, r, p.ID); err != nil {
		a.logger.Error("issue session", "error", err)
		writeError(w, http.StatusInternalServerError, "SESSION_ERROR", "로그인 세션을 만들 수 없습니다.")
		return
	}
	_, _ = a.db.Exec(r.Context(), `UPDATE users SET last_login_at=now() WHERE id=$1`, p.ID)
	a.audit(r, &p, "auth.login", "user", strconv.FormatInt(p.ID, 10), map[string]any{"type": "local"})
	writeData(w, http.StatusOK, p)
}

func (a *App) issueSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := randomToken(32)
	if err != nil {
		return err
	}
	hours := a.settingInt(r.Context(), "auth.session_hours", 12)
	if hours < 1 || hours > 720 {
		hours = 12
	}
	expires := time.Now().Add(time.Duration(hours) * time.Hour)
	var ip any
	if host := remoteHost(r); host != "" {
		ip = host
	}
	_, err = a.db.Exec(r.Context(), `INSERT INTO user_sessions(user_id,token_hash,expires_at,ip_address,user_agent) VALUES($1,$2,$3,$4,$5)`,
		userID, tokenHash(token), expires, ip, trimRunes(r.UserAgent(), 500))
	if err != nil {
		return err
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	return nil
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.db.Exec(r.Context(), `DELETE FROM user_sessions WHERE token_hash=$1`, tokenHash(cookie.Value))
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeData(w, http.StatusOK, map[string]bool{"loggedOut": true})
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	workflow := a.settingBool(r.Context(), "workflow.enabled", false)
	week := currentWeekStart(time.Now().In(a.serviceLocation(r.Context())), a.setting(r.Context(), "workflow.week_start", "MONDAY"))
	writeData(w, http.StatusOK, map[string]any{"user": p, "workflowEnabled": workflow, "aiEnabled": a.settingBool(r.Context(), "ai.enabled", false), "currentWeekStart": week.Format("2006-01-02"), "serviceName": a.setting(r.Context(), "service.name", "Weekly"), "notice": a.setting(r.Context(), "service.notice", ""), "build": a.build})
}

func (a *App) authProviders(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, map[string]any{
		"local":  a.settingBool(r.Context(), "auth.local_enabled", true),
		"oidc":   a.settingBool(r.Context(), "oidc.enabled", false),
		"name":   a.setting(r.Context(), "service.name", "Weekly"),
		"notice": a.setting(r.Context(), "service.notice", ""),
		"build":  a.build,
	})
}

type oidcConfiguration struct {
	Issuer, ClientID, ClientSecret, RedirectURL, UsernameClaim, GroupsClaim, AdminGroup string
	Scopes                                                                              []string
	AutoProvision                                                                       bool
}

func (a *App) oidcConfig(ctx context.Context) (oidcConfiguration, error) {
	if !a.settingBool(ctx, "oidc.enabled", false) {
		return oidcConfiguration{}, errors.New("OIDC disabled")
	}
	secret, err := a.secretSetting(ctx, "oidc.client_secret")
	if err != nil {
		return oidcConfiguration{}, err
	}
	cfg := oidcConfiguration{
		Issuer: a.setting(ctx, "oidc.issuer_url", ""), ClientID: a.setting(ctx, "oidc.client_id", ""), ClientSecret: secret,
		RedirectURL: a.setting(ctx, "oidc.redirect_url", ""), UsernameClaim: a.setting(ctx, "oidc.username_claim", "preferred_username"),
		GroupsClaim: a.setting(ctx, "oidc.groups_claim", "groups"), AdminGroup: a.setting(ctx, "oidc.admin_group", ""),
		Scopes: strings.Fields(a.setting(ctx, "oidc.scopes", "openid profile email")), AutoProvision: a.settingBool(ctx, "oidc.auto_provision", true),
	}
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return oidcConfiguration{}, errors.New("OIDC configuration incomplete")
	}
	if !slices.Contains(cfg.Scopes, oidc.ScopeOpenID) {
		cfg.Scopes = append([]string{oidc.ScopeOpenID}, cfg.Scopes...)
	}
	return cfg, nil
}

func (a *App) oidcStart(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.oidcConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC_UNAVAILABLE", "OIDC 로그인이 설정되지 않았습니다.")
		return
	}
	provider, err := oidc.NewProvider(r.Context(), cfg.Issuer)
	if err != nil {
		a.logger.Error("OIDC discovery", "error", err)
		writeError(w, http.StatusBadGateway, "OIDC_DISCOVERY_FAILED", "OIDC 제공자에 연결할 수 없습니다.")
		return
	}
	state, _ := randomToken(32)
	nonce, _ := randomToken(24)
	verifier, _ := randomToken(48)
	challengeRaw := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeRaw[:])
	_, err = a.db.Exec(r.Context(), `INSERT INTO oidc_login_states(state_hash,nonce,pkce_verifier,expires_at) VALUES($1,$2,$3,now()+interval '10 minutes')`, tokenHash(state), nonce, verifier)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OIDC_STATE_ERROR", "로그인 요청을 시작할 수 없습니다.")
		return
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: "weekly_oidc_state", Value: tokenHash(state), Path: "/api/v1/auth/oidc/callback", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	redirectURL := cfg.RedirectURL
	if redirectURL == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		redirectURL = scheme + "://" + r.Host + "/api/v1/auth/oidc/callback"
	}
	oauthCfg := oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: redirectURL, Scopes: cfg.Scopes}
	http.Redirect(w, r, oauthCfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.SetAuthURLParam("code_challenge", challenge), oauth2.SetAuthURLParam("code_challenge_method", "S256")), http.StatusFound)
}

func (a *App) oidcCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	cookie, cookieErr := r.Cookie("weekly_oidc_state")
	if state == "" || code == "" || cookieErr != nil || cookie.Value != tokenHash(state) {
		writeError(w, http.StatusBadRequest, "OIDC_INVALID_CALLBACK", "OIDC 응답 검증에 실패했습니다.")
		return
	}
	var nonce, verifier string
	err := a.db.QueryRow(r.Context(), `DELETE FROM oidc_login_states WHERE state_hash=$1 AND expires_at>now() RETURNING nonce,pkce_verifier`, tokenHash(state)).Scan(&nonce, &verifier)
	if err != nil {
		writeError(w, http.StatusBadRequest, "OIDC_STATE_EXPIRED", "OIDC 로그인 요청이 만료되었습니다.")
		return
	}
	cfg, err := a.oidcConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC_UNAVAILABLE", "OIDC 설정을 읽을 수 없습니다.")
		return
	}
	provider, err := oidc.NewProvider(r.Context(), cfg.Issuer)
	if err != nil {
		writeError(w, http.StatusBadGateway, "OIDC_DISCOVERY_FAILED", "OIDC 제공자에 연결할 수 없습니다.")
		return
	}
	redirectURL := cfg.RedirectURL
	if redirectURL == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		redirectURL = scheme + "://" + r.Host + "/api/v1/auth/oidc/callback"
	}
	oauthCfg := oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: redirectURL, Scopes: cfg.Scopes}
	token, err := oauthCfg.Exchange(r.Context(), code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "OIDC_EXCHANGE_FAILED", "OIDC 인증 코드를 확인할 수 없습니다.")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		writeError(w, http.StatusUnauthorized, "OIDC_ID_TOKEN_MISSING", "OIDC ID 토큰이 없습니다.")
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(r.Context(), rawIDToken)
	if err != nil || idToken.Nonce != nonce {
		writeError(w, http.StatusUnauthorized, "OIDC_TOKEN_INVALID", "OIDC ID 토큰 검증에 실패했습니다.")
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		writeError(w, http.StatusUnauthorized, "OIDC_CLAIMS_INVALID", "OIDC 사용자 정보를 읽을 수 없습니다.")
		return
	}
	username := claimString(claims, cfg.UsernameClaim)
	if username == "" {
		username = claimString(claims, "email")
	}
	displayName := claimString(claims, "name")
	if displayName == "" {
		displayName = username
	}
	email := claimString(claims, "email")
	subject := claimString(claims, "sub")
	if subject == "" || username == "" {
		writeError(w, http.StatusUnauthorized, "OIDC_CLAIMS_MISSING", "OIDC 사용자 식별 정보가 없습니다.")
		return
	}
	role := "USER"
	if cfg.AdminGroup != "" && slices.Contains(claimStrings(claims, cfg.GroupsClaim), cfg.AdminGroup) {
		role = "ADMIN"
	}
	var userID int64
	err = a.db.QueryRow(r.Context(), `SELECT id FROM users WHERE oidc_subject=$1 OR lower(username)=lower($2) ORDER BY oidc_subject=$1 DESC LIMIT 1`, subject, username).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		if !cfg.AutoProvision {
			writeError(w, http.StatusForbidden, "OIDC_USER_NOT_PROVISIONED", "Weekly에 등록되지 않은 사용자입니다.")
			return
		}
		err = a.db.QueryRow(r.Context(), `INSERT INTO users(username,display_name,email,role,organization_id,oidc_subject)
			VALUES($1,$2,$3,$4,(SELECT id FROM organizations WHERE code='DEFAULT'),$5) RETURNING id`, username, displayName, email, role, subject).Scan(&userID)
	} else if err == nil {
		_, err = a.db.Exec(r.Context(), `UPDATE users SET display_name=$2,email=$3,oidc_subject=$4,
			role=CASE WHEN $5='ADMIN' THEN 'ADMIN' ELSE role END,last_login_at=now(),updated_at=now() WHERE id=$1`, userID, displayName, email, subject, role)
	}
	if err != nil {
		a.logger.Error("provision OIDC user", "error", err)
		writeError(w, http.StatusInternalServerError, "OIDC_PROVISION_FAILED", "OIDC 사용자를 등록할 수 없습니다.")
		return
	}
	if err := a.issueSession(w, r, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_ERROR", "로그인 세션을 만들 수 없습니다.")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func claimString(claims map[string]any, key string) string {
	if value, ok := claims[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func claimStrings(claims map[string]any, key string) []string {
	value := claims[key]
	if items, ok := value.([]any); ok {
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	}
	if text, ok := value.(string); ok {
		return strings.Fields(text)
	}
	return nil
}

func (a *App) setting(ctx context.Context, key, fallback string) string {
	var value string
	if err := a.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1 AND secret=false`, key).Scan(&value); err != nil {
		return fallback
	}
	return value
}

func (a *App) secretSetting(ctx context.Context, key string) (string, error) {
	var value string
	if err := a.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1 AND secret=true`, key).Scan(&value); err != nil {
		return "", err
	}
	if value == "" {
		return "", nil
	}
	return a.box.Decrypt(value)
}

func (a *App) settingBool(ctx context.Context, key string, fallback bool) bool {
	value := a.setting(ctx, key, strconv.FormatBool(fallback))
	result, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return result
}

func (a *App) settingInt(ctx context.Context, key string, fallback int) int {
	value, err := strconv.Atoi(a.setting(ctx, key, strconv.Itoa(fallback)))
	if err != nil {
		return fallback
	}
	return value
}

func (a *App) audit(r *http.Request, p *principal, action, resourceType, resourceID string, detail any) {
	encoded, _ := json.Marshal(detail)
	var actor any
	if p != nil {
		actor = p.ID
	}
	var ip any
	if host := remoteHost(r); host != "" {
		ip = host
	}
	_, _ = a.db.Exec(r.Context(), `INSERT INTO audit_logs(actor_id,action,resource_type,resource_id,detail,ip_address) VALUES($1,$2,$3,$4,$5,$6)`, actor, action, resourceType, resourceID, encoded, ip)
}

func remoteHost(r *http.Request) string {
	host := strings.TrimSpace(strings.Split(r.RemoteAddr, ":")[0])
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		host = forwarded
	}
	return host
}

