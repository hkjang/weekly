package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
)

// MCP 를 SSO 로 — 개인 키 없이, Keycloak 이 발급한 토큰으로.
//
// The MCP authorization specification (2025-06-18 and later) is OAuth 2.1: the
// MCP server is a *resource server* that publishes where its authorization
// server is (RFC 9728, /.well-known/oauth-protected-resource), and a client
// that is refused with 401 reads that document, sends the person through the
// authorization server with PKCE, and comes back with an access token whose
// audience (RFC 8707) is this server. Nothing about issuing tokens happens
// here — Keycloak does that — and this file only has to answer two questions:
// where is the authorization server, and is this token one it issued for us.
//
// The personal key stays. It is what an automation with no person behind it
// uses, and what a deployment without Keycloak uses. A token from SSO is a
// second door into the same room: it authenticates an *existing* Weekly
// account, carries the read-only scopes the administrator chose, and is held
// to the same read-only rule a key is. It never creates an account — signing
// in to the web once is what provisions one, and a machine presenting a token
// is not the moment to decide who somebody is.

type mcpOAuthSettings struct {
	Enabled bool
	// Issuer is the authorization server, shared with the web sign-in.
	Issuer string
	// ClientID is the web sign-in's client, accepted as an audience because
	// Keycloak puts it in tokens it issues to that client without any mapper.
	ClientID string
	// Resource is the identifier this server claims (RFC 8707). Empty means it
	// is derived from the request: scheme://host/mcp.
	Resource string
	// Audiences are additional accepted aud values an administrator names.
	Audiences []string
	// Scopes are what a valid token may do. A token does not carry Weekly's
	// scope vocabulary unless somebody teaches Keycloak that vocabulary, and
	// asking every deployment to do so before MCP works is the wrong trade;
	// the administrator states the ceiling once instead.
	Scopes        []string
	UsernameClaim string
}

func (a *App) mcpOAuthSettings(ctx context.Context) mcpOAuthSettings {
	return mcpOAuthSettings{
		Enabled:       a.settingBool(ctx, "mcp.oauth.enabled", false),
		Issuer:        strings.TrimRight(a.setting(ctx, "oidc.issuer_url", ""), "/"),
		ClientID:      a.setting(ctx, "oidc.client_id", ""),
		Resource:      strings.TrimSpace(a.setting(ctx, "mcp.oauth.resource", "")),
		Audiences:     strings.Fields(a.setting(ctx, "mcp.oauth.audience", "")),
		Scopes:        strings.Fields(a.setting(ctx, "mcp.oauth.scopes", "mcp:read reports:read analytics:read")),
		UsernameClaim: a.setting(ctx, "oidc.username_claim", "preferred_username"),
	}
}

// mcpResource is the identifier this deployment claims for its MCP endpoint:
// what the metadata document advertises and what a token's aud must name.
func (settings mcpOAuthSettings) mcpResource(r *http.Request) string {
	if settings.Resource != "" {
		return settings.Resource
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/mcp"
}

// metadataURL is where a refused client is sent to learn the above.
func (settings mcpOAuthSettings) metadataURL(r *http.Request) string {
	resource := settings.mcpResource(r)
	base := strings.TrimSuffix(resource, "/mcp")
	return base + "/.well-known/oauth-protected-resource/mcp"
}

// oauthProviders caches discovery per issuer. Discovery is a network round
// trip to Keycloak and the JWKS behind it is what verifies every token; doing
// that once per request would put Keycloak's latency in front of every MCP
// call. go-oidc refetches the key set on an unknown key id, so key rotation
// needs no cache invalidation here.
type oauthProviders struct {
	mu       sync.Mutex
	byIssuer map[string]*oidc.Provider
}

func (a *App) oauthProvider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	a.oauth.mu.Lock()
	defer a.oauth.mu.Unlock()
	if a.oauth.byIssuer == nil {
		a.oauth.byIssuer = map[string]*oidc.Provider{}
	}
	if provider := a.oauth.byIssuer[issuer]; provider != nil {
		return provider, nil
	}
	// Discovery must outlive this request: the provider it returns keeps the
	// context for later key fetches.
	provider, err := oidc.NewProvider(context.WithoutCancel(ctx), issuer)
	if err != nil {
		return nil, err
	}
	a.oauth.byIssuer[issuer] = provider
	return provider, nil
}

// looksLikeJWT is the cheap shape test that separates "not a key" from "not a
// token of any kind we accept", so the refusal can say which.
func looksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

// oauthPrincipal turns a bearer access token into a Weekly principal, or says
// exactly why it will not.
func (a *App) oauthPrincipal(ctx context.Context, r *http.Request, token string) (*principal, error) {
	// The shape first, before anything that needs the database: a bearer that
	// is neither a key nor a token is a sign-out however the deployment is
	// configured, and authenticate promises that answer without a round trip.
	if !looksLikeJWT(token) {
		return nil, refusalReason{
			err:     fmt.Errorf("%w: bearer is neither key nor jwt", errNoSession),
			message: "Bearer 토큰이 Weekly API 키(wky_)도 SSO 액세스 토큰(JWT)도 아닙니다.",
		}
	}
	settings := a.mcpOAuthSettings(ctx)
	if !settings.Enabled {
		return nil, refusalReason{
			err:     fmt.Errorf("%w: sso tokens disabled", errNoSession),
			message: "이 서버는 SSO 액세스 토큰을 받지 않습니다. 개인 API 키(wky_)를 쓰거나, 관리자가 MCP SSO(OAuth) 인증을 켜야 합니다.",
		}
	}
	if settings.Issuer == "" {
		return nil, refusalReason{
			err:     fmt.Errorf("%w: sso issuer unset", errNoSession),
			message: "MCP SSO 인증이 켜져 있지만 Keycloak 발급자 주소(oidc.issuer_url)가 비어 있습니다. 관리자에게 알리세요.",
		}
	}
	provider, err := a.oauthProvider(ctx, settings.Issuer)
	if err != nil {
		a.logger.Warn("mcp oauth discovery", "issuer", settings.Issuer, "error", err)
		return nil, refusalReason{
			err:     fmt.Errorf("%w: discovery failed", errNoSession),
			message: "Keycloak 발급자 정보를 읽지 못해 SSO 토큰을 확인할 수 없습니다. 잠시 후 다시 시도하거나 관리자에게 알리세요.",
		}
	}
	// Signature, issuer and expiry. The audience is checked below by hand
	// because more than one value is acceptable and go-oidc compares one.
	verified, err := provider.Verifier(&oidc.Config{SkipClientIDCheck: true}).Verify(ctx, token)
	if err != nil {
		return nil, refusalReason{
			err:     fmt.Errorf("%w: token rejected: %v", errNoSession, err),
			message: "SSO 액세스 토큰이 유효하지 않습니다(서명·발급자·만료). 클라이언트에서 다시 로그인하세요.",
		}
	}
	var claims map[string]any
	if err := verified.Claims(&claims); err != nil {
		return nil, refusalReason{
			err:     fmt.Errorf("%w: claims unreadable", errNoSession),
			message: "SSO 토큰의 사용자 정보를 읽을 수 없습니다.",
		}
	}
	// Whom the token was minted for. Measured against a real Keycloak 26: an
	// access token issued to a client carries that client in `azp` and
	// `aud: ["account"]` — the client id is *not* in aud, whatever an ID token
	// does. So the binding this server checks is "aud names us, or the token
	// was issued to a client we trust (azp)". Either is the token being for
	// this deployment rather than passed through from some other application
	// in the realm, which is what RFC 8707 and the MCP specification are
	// guarding against. The administrator's list applies to both, so the plain
	// path — "put the MCP client's id in the list" — needs no mapper at all.
	resource := settings.mcpResource(r)
	accepted := append([]string{resource}, settings.Audiences...)
	if settings.ClientID != "" {
		accepted = append(accepted, settings.ClientID)
	}
	bound := append(slices.Clone(verified.Audience), claimString(claims, "azp"))
	if !slices.ContainsFunc(bound, func(value string) bool { return value != "" && slices.Contains(accepted, value) }) {
		return nil, refusalReason{
			err: fmt.Errorf("%w: audience %v / azp %q not accepted", errNoSession, verified.Audience, claimString(claims, "azp")),
			message: fmt.Sprintf("SSO 토큰이 이 서버를 위해 발급된 것이 아닙니다(aud %v, azp %q). 관리자가 허용 대상에 그 클라이언트 ID 를 적거나, Keycloak 클라이언트에 Audience 매퍼로 %q 를 더해야 합니다.",
				verified.Audience, claimString(claims, "azp"), resource),
		}
	}
	username := claimString(claims, settings.UsernameClaim)
	if username == "" {
		username = claimString(claims, "email")
	}
	subject := claimString(claims, "sub")
	if subject == "" || username == "" {
		return nil, refusalReason{
			err:     fmt.Errorf("%w: identity claims missing", errNoSession),
			message: "SSO 토큰에 사용자 식별 정보(sub·" + settings.UsernameClaim + ")가 없습니다.",
		}
	}
	// The same lookup the web sign-in uses, without the provisioning half.
	p := &principal{AuthType: "oauth", Scopes: settings.Scopes}
	err = a.db.QueryRow(ctx, `SELECT u.id,u.username,u.display_name,coalesce(u.email,''),u.role,u.organization_id,u.key_version
		FROM users u WHERE (u.oidc_subject=$1 OR lower(u.username)=lower($2)) AND u.active=true
		ORDER BY u.oidc_subject=$1 DESC LIMIT 1`, subject, username).
		Scan(&p.ID, &p.Username, &p.DisplayName, &p.Email, &p.Role, &p.OrganizationID, &p.KeyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, refusalReason{
			err:     fmt.Errorf("%w: no weekly account for sso subject", errNoSession),
			message: "이 SSO 계정은 Weekly 에 등록되지 않았거나 비활성입니다. 먼저 웹으로 한 번 로그인하세요.",
		}
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// protectedResourceMetadata is RFC 9728: the document a refused MCP client
// reads to find the authorization server. Public by design — it says where to
// sign in, not who is signed in.
func (a *App) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	settings := a.mcpOAuthSettings(r.Context())
	if !settings.Enabled || settings.Issuer == "" {
		writeError(w, http.StatusNotFound, "MCP_OAUTH_DISABLED", "이 서버의 MCP 는 SSO 토큰을 받지 않습니다. 개인 API 키(wky_)를 사용하세요.")
		return
	}
	// The bare document, not the product's envelope: the reader is an OAuth
	// client library following RFC 9728, which knows nothing about {data:…}.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":                 settings.mcpResource(r),
		"authorization_servers":    []string{settings.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         settings.Scopes,
		"resource_name":            "Weekly Analytics MCP",
		"resource_documentation":   strings.TrimSuffix(settings.mcpResource(r), "/mcp") + "/docs/MCP.html",
	})
}

// wwwAuthenticate is the header that turns a 401 into an invitation: the MCP
// client reads resource_metadata and starts the OAuth flow from there. Without
// it a refusal is a dead end.
func (a *App) wwwAuthenticate(w http.ResponseWriter, r *http.Request) {
	settings := a.mcpOAuthSettings(r.Context())
	if !settings.Enabled || settings.Issuer == "" {
		return
	}
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf(`Bearer realm="Weekly", resource_metadata=%q`, settings.metadataURL(r)))
}
