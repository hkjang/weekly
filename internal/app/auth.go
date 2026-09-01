package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	// Nobody can get in, so the pair is required here and only here.
	if missing := missingRequired(environment{PostgresDSN: "set", BootstrapAdmin: username, BootstrapPassword: password}); len(missing) > 0 {
		return fmt.Errorf(
			"관리자 계정이 없는 데이터베이스입니다. %s 환경변수가 없으면 아무도 로그인할 수 없습니다. "+
				"deploy/.env.example 을 복사해 채운 뒤 --env-file 로 넘기십시오",
			strings.Join(missing, ", "))
	}
	if len(password) < 12 {
		return errors.New(
			"WEEKLY_BOOTSTRAP_ADMIN_PASSWORD 는 12자 이상이어야 합니다. " +
				"첫 관리자 계정의 비밀번호이며, 기동한 뒤 화면에서 바꿀 수 있습니다")
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

// errNoSession means the caller genuinely is not signed in: no cookie, an
// unusable token, or credentials the database looked at and did not recognise.
//
// Anything else authenticate returns is a failure to find out — most often that
// the database is unreachable — and the two must not be answered the same way.
// They were. A database outage reached every user as "로그인이 필요합니다",
// which sends them to do the one thing that cannot help; and because the screen
// treats 401 as a lost session, it also offered them a fresh login tab. Anybody
// who took it lost the report they were part-way through writing.
var errNoSession = errors.New("not authenticated")

// refusalReason carries the sentence a refused caller should actually read.
//
// A browser with no cookie is told to sign in, and that is the right advice. A
// script holding an API key cannot take it — there is no login for it to do —
// and "로그인이 필요합니다" was the same answer for a mistyped key, a key that
// expired overnight, a key somebody revoked, and a key that a rotation
// invalidated. The product schedules the second of those itself: it offers
// expiresInDays and an administrator ceiling, creates the state, and then
// refuses to name it. Whoever wired an internal script to this API had no way
// to find out which of the four had happened.
type refusalReason struct {
	err     error
	message string
}

func (r refusalReason) Error() string { return r.err.Error() }
func (r refusalReason) Unwrap() error { return r.err }

// refusalMessage returns the sentence to show a refused caller, or "" to use
// the default.
func refusalMessage(err error) string {
	var reason refusalReason
	if errors.As(err, &reason) {
		return reason.message
	}
	return ""
}

// apiKeyRefusal looks up what became of a key that the authenticating query
// would not accept. It runs only after that query has already missed, so it
// costs nothing on the ordinary path.
//
// Naming the state tells nothing to somebody who does not already hold the key:
// the answer is reachable only by presenting the key itself, and the keys are
// 32 random bytes. Order is fixed and deliberate — a revocation is somebody's
// decision and outranks a date passing on its own.
func (a *App) apiKeyRefusal(ctx context.Context, token string) string {
	var revoked, expires *time.Time
	var keyVersion, userVersion int
	var active bool
	err := a.db.QueryRow(ctx, `SELECT k.revoked_at, k.expires_at, k.key_version, u.key_version, u.active
		FROM personal_api_keys k JOIN users u ON u.id=k.user_id WHERE k.token_hash=$1`, tokenHash(token)).
		Scan(&revoked, &expires, &keyVersion, &userVersion, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "이 API 키를 찾을 수 없습니다. 값이 잘못되었거나 이미 삭제된 키입니다."
	}
	if err != nil {
		// The lookup itself failed. Saying something specific now would be a
		// guess, and a guess is what this whole change is removing.
		return ""
	}
	switch {
	case revoked != nil:
		return "이 API 키는 회수되었습니다. 개인 설정에서 새 키를 발급하세요."
	case keyVersion != userVersion:
		return "키를 일괄 회수한 뒤 발급된 키가 아닙니다. 개인 설정에서 새 키를 발급하세요."
	case expires != nil && !expires.After(time.Now()):
		return "이 API 키는 " + expires.Format("2006-01-02") + " 에 만료되었습니다. 개인 설정에서 새 키를 발급하세요."
	case !active:
		return "이 키를 발급한 계정이 비활성 상태입니다. 관리자에게 문의하세요."
	}
	return ""
}

func (a *App) authenticate(r *http.Request) (*principal, error) {
	ctx := r.Context()
	if authorization := r.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		if !strings.HasPrefix(token, "wky_") {
			return nil, refusalReason{
				err:     fmt.Errorf("%w: unsupported bearer token", errNoSession),
				message: "Bearer 토큰이 Weekly API 키 형식이 아닙니다. 키는 wky_ 로 시작합니다.",
			}
		}
		p := &principal{AuthType: "api_key"}
		err := a.db.QueryRow(ctx, `SELECT u.id,u.username,u.display_name,coalesce(u.email,''),u.role,u.organization_id,u.key_version,k.scopes
			FROM personal_api_keys k JOIN users u ON u.id=k.user_id
			WHERE k.token_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND u.active=true AND k.key_version=u.key_version`, tokenHash(token)).
			Scan(&p.ID, &p.Username, &p.DisplayName, &p.Email, &p.Role, &p.OrganizationID, &p.KeyVersion, &p.Scopes)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, refusalReason{
				err:     fmt.Errorf("%w: no such api key", errNoSession),
				message: a.apiKeyRefusal(ctx, token),
			}
		}
		if err != nil {
			return nil, err
		}
		_, _ = a.db.Exec(ctx, `UPDATE personal_api_keys SET last_used_at=now() WHERE token_hash=$1`, tokenHash(token))
		return p, nil
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, fmt.Errorf("%w: no session cookie", errNoSession)
	}
	p := &principal{AuthType: "session"}
	err = a.db.QueryRow(ctx, `SELECT u.id,u.username,u.display_name,coalesce(u.email,''),u.role,u.organization_id,u.key_version
		FROM user_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.expires_at>now() AND u.active=true`, tokenHash(cookie.Value)).
		Scan(&p.ID, &p.Username, &p.DisplayName, &p.Email, &p.Role, &p.OrganizationID, &p.KeyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: session expired or revoked", errNoSession)
	}
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
	username := strings.TrimSpace(input.Username)
	address := remoteHost(r)
	// Checked before the password is verified, so a blocked account costs an
	// attacker one cheap query and gives away nothing about the password.
	if throttle := a.loginThrottleFor(r.Context(), username, address); throttle.Blocked {
		a.audit(r, nil, "auth.login_blocked", "user", username, map[string]any{"failures": throttle.Failures})
		writeLoginBlocked(w, throttle.RetryAfter)
		return
	}
	p := principal{AuthType: "session"}
	var passwordHash *string
	// Bounded, because a database that has gone away otherwise leaves the
	// person who pressed 로그인 watching a spinner. Five seconds is far beyond a
	// lookup on an indexed column and well short of giving up on the product.
	lookupCtx, cancelLookup := context.WithTimeout(r.Context(), 5*time.Second)
	err := a.db.QueryRow(lookupCtx, `SELECT id,username,display_name,coalesce(email,''),role,organization_id,key_version,password_hash
		FROM users WHERE lower(username)=lower($1) AND active=true`, username).
		Scan(&p.ID, &p.Username, &p.DisplayName, &p.Email, &p.Role, &p.OrganizationID, &p.KeyVersion, &passwordHash)
	cancelLookup()
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// Not knowing whether the password is right is not the same as knowing
		// it is wrong. This branch used to fall through to
		// "아이디 또는 비밀번호가 올바르지 않습니다", so a database outage told
		// everybody their password had stopped working — and each retry counted
		// against the throttle, so the people who tried hardest got locked out
		// of a service that was never rejecting them.
		//
		// Answering the same way for every caller costs nothing here: the store
		// being unreachable says nothing about whether an account exists, so
		// this reveals no more than the outage already does. Nothing is recorded
		// against the account either.
		a.logger.Error("login lookup", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusServiceUnavailable, "STORE_UNAVAILABLE",
			"지금 서버가 데이터베이스에 연결하지 못했습니다. 아이디와 비밀번호 문제가 아니니 잠시 후 다시 시도하세요.")
		return
	}
	if err != nil || passwordHash == nil || !verifyPassword(*passwordHash, input.Password) {
		a.recordLoginFailure(r.Context(), username, address)
		// Recounted after recording so this attempt is included: the delay has
		// to grow with the attempt that just failed, not with the one before it.
		after := a.loginThrottleFor(r.Context(), username, address)
		time.Sleep(loginFailureDelay(after.Failures))
		// The same answer whether the account exists or not. A different message
		// or a different delay for an unknown user turns the endpoint into a way
		// to enumerate accounts.
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "아이디 또는 비밀번호가 올바르지 않습니다.")
		return
	}
	a.clearLoginFailures(r.Context(), username)
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
	writeData(w, http.StatusOK, map[string]any{"user": p, "workflowEnabled": workflow, "aiEnabled": a.settingBool(r.Context(), "ai.enabled", false), "confluenceEnabled": a.settingBool(r.Context(), "confluence.enabled", false), "currentWeekStart": week.Format("2006-01-02"), "serviceName": a.setting(r.Context(), "service.name", "Weekly"), "notice": a.setting(r.Context(), "service.notice", ""), "accountNotice": accountNotice(p, workflow), "build": a.build})
}

// accountNotice describes a state of this account that makes the product answer
// correctly and uselessly.
//
// A leader with no organisation is the one that exists today. Every screen built
// on the organisation subtree — the team list, the dashboard, the handover
// roster — answers with nothing, and "nothing" is what a quiet week looks like
// too. Saying it once, where the reader always is, beats an explanation glued
// to each empty screen and forgotten on the next one.
//
// The service-wide notice is separate on purpose: that one is for everybody and
// this one is about you.
func accountNotice(p *principal, workflowEnabled bool) string {
	if p == nil || p.OrganizationID != nil {
		return ""
	}
	switch p.Role {
	case "TEAM_LEADER", "ORG_MANAGER":
		return "이 계정에 소속 조직이 지정되어 있지 않습니다. 팀 주간보고·대시보드·인수인계가 모두 비어 보이며, 관리자가 소속 조직을 지정하면 해결됩니다."
	case "USER":
		// Measured: this account's report is absent from every leader's team
		// list, so with the workflow on it is submitted into a queue no
		// reviewer can open. The writer does the work, submits it and waits.
		if workflowEnabled {
			return "이 계정에 소속 조직이 지정되어 있지 않습니다. 작성한 주간보고가 어느 팀장에게도 보이지 않아, 제출해도 검토 대기 상태로 남습니다. 관리자가 소속 조직을 지정하면 해결됩니다."
		}
		return "이 계정에 소속 조직이 지정되어 있지 않습니다. 작성한 주간보고가 어느 팀장의 팀 주간보고에도 나타나지 않으며, 관리자가 소속 조직을 지정하면 해결됩니다."
	}
	return ""
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
	silent := r.URL.Query().Get("silent") == "1"
	returnTo := safeOIDCReturnTo(r.URL.Query().Get("returnTo"))
	cfg, err := a.oidcConfig(r.Context())
	if err != nil {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "OIDC_UNAVAILABLE", "OIDC 로그인이 설정되지 않았습니다.")
		return
	}
	provider, err := oidc.NewProvider(r.Context(), cfg.Issuer)
	if err != nil {
		a.logger.Error("OIDC discovery", "error", err)
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusBadGateway, "OIDC_DISCOVERY_FAILED", "OIDC 제공자에 연결할 수 없습니다.")
		return
	}
	state, _ := randomToken(32)
	nonce, _ := randomToken(24)
	verifier, _ := randomToken(48)
	challengeRaw := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeRaw[:])
	_, err = a.db.Exec(r.Context(), `INSERT INTO oidc_login_states
		(state_hash,nonce,pkce_verifier,expires_at,silent,return_to)
		VALUES($1,$2,$3,now()+interval '10 minutes',$4,$5)`,
		tokenHash(state), nonce, verifier, silent, returnTo)
	if err != nil {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
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
	options := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.SetAuthURLParam("code_challenge", challenge), oauth2.SetAuthURLParam("code_challenge_method", "S256")}
	if silent {
		// Keycloak checks its own SSO cookie without drawing a login screen. No
		// cookie means an OIDC error callback, which is handled below as the
		// ordinary Weekly login page rather than as a failed login.
		options = append(options, oauth2.SetAuthURLParam("prompt", "none"))
	}
	http.Redirect(w, r, oauthCfg.AuthCodeURL(state, options...), http.StatusFound)
}

func (a *App) oidcCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	providerError := strings.TrimSpace(r.URL.Query().Get("error"))
	cookie, cookieErr := r.Cookie("weekly_oidc_state")
	if state == "" || cookieErr != nil || cookie.Value != tokenHash(state) {
		writeError(w, http.StatusBadRequest, "OIDC_INVALID_CALLBACK", "OIDC 응답 검증에 실패했습니다.")
		return
	}
	// A provider callback has exactly one outcome to consume: an authorization
	// code or an OAuth error. A blank/malformed request may be retried with the
	// real provider response, so it must not spend the one-time state first.
	if code == "" && providerError == "" {
		writeError(w, http.StatusBadRequest, "OIDC_INVALID_CALLBACK", "OIDC 응답 검증에 실패했습니다.")
		return
	}
	var nonce, verifier, returnTo string
	var silent bool
	err := a.db.QueryRow(r.Context(), `DELETE FROM oidc_login_states
		WHERE state_hash=$1 AND expires_at>now()
		RETURNING nonce,pkce_verifier,silent,return_to`, tokenHash(state)).
		Scan(&nonce, &verifier, &silent, &returnTo)
	if err != nil {
		writeError(w, http.StatusBadRequest, "OIDC_STATE_EXPIRED", "OIDC 로그인 요청이 만료되었습니다.")
		return
	}
	returnTo = safeOIDCReturnTo(returnTo)
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: "weekly_oidc_state", Value: "", Path: "/api/v1/auth/oidc/callback", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if providerError != "" {
		if silent {
			reason := "unavailable"
			switch providerError {
			case "login_required", "interaction_required", "consent_required", "account_selection_required":
				reason = "miss"
			}
			redirectFromSilentOIDC(w, r, returnTo, reason)
			return
		}
		writeError(w, http.StatusUnauthorized, "OIDC_PROVIDER_ERROR", "OIDC 제공자가 로그인을 완료하지 않았습니다.")
		return
	}
	if code == "" {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusBadRequest, "OIDC_INVALID_CALLBACK", "OIDC 응답 검증에 실패했습니다.")
		return
	}
	cfg, err := a.oidcConfig(r.Context())
	if err != nil {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "OIDC_UNAVAILABLE", "OIDC 설정을 읽을 수 없습니다.")
		return
	}
	provider, err := oidc.NewProvider(r.Context(), cfg.Issuer)
	if err != nil {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
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
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusUnauthorized, "OIDC_EXCHANGE_FAILED", "OIDC 인증 코드를 확인할 수 없습니다.")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusUnauthorized, "OIDC_ID_TOKEN_MISSING", "OIDC ID 토큰이 없습니다.")
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(r.Context(), rawIDToken)
	if err != nil || idToken.Nonce != nonce {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusUnauthorized, "OIDC_TOKEN_INVALID", "OIDC ID 토큰 검증에 실패했습니다.")
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
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
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
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
			if silent {
				redirectFromSilentOIDC(w, r, returnTo, "unavailable")
				return
			}
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
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "OIDC_PROVISION_FAILED", "OIDC 사용자를 등록할 수 없습니다.")
		return
	}
	if err := a.issueSession(w, r, userID); err != nil {
		if silent {
			redirectFromSilentOIDC(w, r, returnTo, "unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "SESSION_ERROR", "로그인 세션을 만들 수 없습니다.")
		return
	}
	http.Redirect(w, r, "/"+returnTo, http.StatusFound)
}

// safeOIDCReturnTo accepts only this SPA's own hash route. A value supplied in
// the login-start URL must never turn the callback into an open redirect.
func safeOIDCReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 2048 || !strings.HasPrefix(value, "#/") || strings.ContainsAny(value, "\\\r\n\t") {
		return ""
	}
	return value
}

func redirectFromSilentOIDC(w http.ResponseWriter, r *http.Request, returnTo, reason string) {
	reason = strings.TrimSpace(reason)
	target := "/"
	if reason != "" {
		target += "?oidc_auto=" + reason
	}
	target += safeOIDCReturnTo(returnTo)
	http.Redirect(w, r, target, http.StatusFound)
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

// settingWait bounds one settings read.
//
// A setting already has an answer for when the read fails — the caller passes
// the fallback. What it did not have was a bound on how long to wait before
// using it, so a database that had gone away made each read sit out the full
// dial timeout. Handlers read several settings apiece: the login screen reads
// four, which turned a five second outage into twenty seconds of a spinning
// button with nothing said.
//
// One second, because this is a lookup that normally takes under a millisecond
// and whose failure mode is already handled. The readiness probe in app.go
// bounds its own database call the same way and for the same reason.
const settingWait = time.Second

func (a *App) setting(ctx context.Context, key, fallback string) string {
	ctx, cancel := context.WithTimeout(ctx, settingWait)
	defer cancel()
	var value string
	if err := a.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1 AND secret=false`, key).Scan(&value); err != nil {
		return fallback
	}
	return value
}

func (a *App) secretSetting(ctx context.Context, key string) (string, error) {
	var value string
	if err := a.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1 AND secret=true`, key).Scan(&value); err != nil {
		// No row is no secret. A settings key that has never been written is
		// how a deployment that does not use a feature looks, and reading that
		// back as a failure makes every caller answer 500 for "not configured".
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
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

// audit records what happened. It is called before the response is written, so
// the action it describes has already committed by the time this runs: if the
// insert is dropped, the deployment keeps the change and loses the only record
// of who made it, and the administrator reading 감사 로그 has no way to tell
// that a row is missing. Two things follow.
//
// The write does not ride on the request context. A client that walks away
// mid-request would cancel it, and the audit trail would develop holes exactly
// when someone closes the tab after acting.
//
// And a failure is logged. It stays best-effort — refusing a completed action
// because its bookkeeping failed would be worse — but a gap nobody can find is
// not a gap anybody will fix.
func (a *App) audit(r *http.Request, p *principal, action, resourceType, resourceID string, detail any) {
	encoded, err := json.Marshal(detail)
	if err != nil {
		a.logger.Warn("audit detail could not be encoded", "error", err, "action", action, "resource", resourceType)
		encoded = []byte("{}")
	}
	var actor any
	if p != nil {
		actor = p.ID
	}
	var ip any
	if host := remoteHost(r); host != "" {
		ip = host
	}
	ctx := context.WithoutCancel(r.Context())
	if _, err := a.db.Exec(ctx, `INSERT INTO audit_logs(actor_id,action,resource_type,resource_id,detail,ip_address) VALUES($1,$2,$3,$4,$5,$6)`, actor, action, resourceType, resourceID, encoded, ip); err != nil {
		a.logger.Error("audit record was not written", "error", err, "action", action,
			"resource_type", resourceType, "resource_id", resourceID, "actor_id", actor,
			"trace", traceIDFromContext(r.Context()))
	}
}

func remoteHost(r *http.Request) string {
	host := strings.TrimSpace(strings.Split(r.RemoteAddr, ":")[0])
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		host = forwarded
	}
	return host
}
