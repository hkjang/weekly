package app

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// 메일 수신 끄기 — 메일 안의 링크 한 번으로, 로그인 없이.
//
// Every mail this service sends on its own is one the reader turned on
// (mail-opt-in, TestANewAccountReceivesNoMailUntilItAsks). Turning it off has
// to be at least as easy: a link at the foot of the mail, and the
// List-Unsubscribe header (RFC 2369) with its one-click partner (RFC 8058), so
// a mail client can show its own 수신 거부 button. Signing in to find a switch
// on a settings screen is the kind of friction that turns a mail somebody no
// longer wants into a mail they mark as spam.
//
// The link is a capability: a token naming one person and one kind of mail,
// signed with a key derived from the instance key. It can only ever turn mail
// off — never on, never anything else — so a token that leaks costs at most a
// mail its owner turns back on. GET only shows a page; the change is a POST,
// because mail scanners open every link in a message and must not unsubscribe
// anybody by looking.

// The kinds of mail a person can turn off, with the column that holds the
// choice and the words the mail and the page use for it.
type mailKindInfo struct {
	column string
	label  string
	toggle string
}

const (
	mailKindReport   = "report"
	mailKindSchedule = "schedule"
	mailKindTeam     = "team"
)

var mailKinds = map[string]mailKindInfo{
	mailKindReport:   {column: "on_submit", label: "주간보고 제출 사본 메일", toggle: "'제출할 때 보내기'"},
	mailKindSchedule: {column: "schedule_reminder", label: "마감 임박 알림 메일", toggle: "'마감 임박 알림 받기'"},
	mailKindTeam:     {column: "team_reminder", label: "주간보고 작성 권고 메일", toggle: "'작성 권고 메일 받기'"},
}

// mailKindOrder is the order "모든 메일 끄기" walks and the page lists them.
var mailKindOrder = []string{mailKindReport, mailKindSchedule, mailKindTeam}

func mailUnsubscribeMessage(userID int64, kind string) string {
	return fmt.Sprintf("mail-unsubscribe:%d:%s", userID, kind)
}

// mailUnsubscribeToken is "<user>.<kind>.<signature>". The first two parts are
// readable on purpose: the token names whose mail and which, and the signature
// is what makes that claim true.
func (a *App) mailUnsubscribeToken(userID int64, kind string) string {
	signature := a.box.linkSignature(mailUnsubscribeMessage(userID, kind))[:16]
	return fmt.Sprintf("%d.%s.%s", userID, kind, base64.RawURLEncoding.EncodeToString(signature))
}

// parseMailUnsubscribeToken answers whose mail and which kind a token names, or
// false for anything not signed by this instance.
func (a *App) parseMailUnsubscribeToken(token string) (int64, string, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return 0, "", false
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || userID <= 0 {
		return 0, "", false
	}
	if _, known := mailKinds[parts[1]]; !known {
		return 0, "", false
	}
	given, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return 0, "", false
	}
	want := a.box.linkSignature(mailUnsubscribeMessage(userID, parts[1]))[:16]
	if subtle.ConstantTimeCompare(given, want) != 1 {
		return 0, "", false
	}
	return userID, parts[1], true
}

// publicBaseURL is the address people open this service at, for links in mail.
//
// service.public_url when an administrator has set it. Otherwise the origin of
// the Keycloak redirect URL, which is this service's own public address by
// construction — Keycloak sends browsers back to it. Neither: no links, and
// the mail says where the switch is in words instead.
func (a *App) publicBaseURL(ctx context.Context) string {
	if configured := strings.TrimRight(strings.TrimSpace(a.setting(ctx, "service.public_url", "")), "/"); configured != "" {
		return configured
	}
	if redirect := strings.TrimSpace(a.setting(ctx, "oidc.redirect_url", "")); redirect != "" {
		if parsed, err := url.Parse(redirect); err == nil && parsed.Host != "" &&
			(parsed.Scheme == "https" || parsed.Scheme == "http") {
			return parsed.Scheme + "://" + parsed.Host
		}
	}
	return ""
}

// mailLinks are the two addresses a mail's footer can offer.
type mailLinks struct {
	unsubscribe string
	settings    string
}

func (a *App) mailLinksFor(ctx context.Context, userID int64, kind string) mailLinks {
	base := a.publicBaseURL(ctx)
	if base == "" {
		return mailLinks{}
	}
	return mailLinks{
		unsubscribe: base + "/mail/unsubscribe?t=" + url.QueryEscape(a.mailUnsubscribeToken(userID, kind)),
		settings:    base + "/#/profile",
	}
}

// mailFooter tells the reader why this mail came and how to stop it. Every
// mail the service sends on its own ends with one.
func mailFooter(kind string, links mailLinks) string {
	info := mailKinds[kind]
	var out strings.Builder
	out.WriteString("\n--\n")
	fmt.Fprintf(&out, "개인 설정에서 %s를 켜 두셔서 보낸 메일입니다.\n", info.toggle)
	if links.unsubscribe != "" {
		fmt.Fprintf(&out, "이 메일을 그만 받으려면(로그인 없이): %s\n", links.unsubscribe)
	} else {
		fmt.Fprintf(&out, "그만 받으려면 개인 설정 → 주간보고 메일 발송에서 %s를 끄십시오.\n", info.toggle)
	}
	if links.settings != "" {
		fmt.Fprintf(&out, "메일 설정: %s\n", links.settings)
	}
	return out.String()
}

// mailUnsubscribeHeaders lets a mail client offer its own 수신 거부 button.
// One-click (RFC 8058) is declared only over HTTPS, which that RFC requires.
func mailUnsubscribeHeaders(links mailLinks) []string {
	if links.unsubscribe == "" {
		return nil
	}
	headers := []string{"List-Unsubscribe: <" + links.unsubscribe + ">"}
	if strings.HasPrefix(links.unsubscribe, "https://") {
		headers = append(headers, "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	}
	return headers
}

// ---------------------------------------------------------------------------
// The page and the switch
// ---------------------------------------------------------------------------

var unsubscribePage = template.Must(template.New("unsubscribe").Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>{{.Title}} · {{.Service}}</title>
<style>
body{margin:0;font-family:system-ui,-apple-system,"Malgun Gothic","Apple SD Gothic Neo",sans-serif;background:#f8fafc;color:#1e293b}
main{max-width:520px;margin:12vh auto;padding:28px;background:#fff;border:1px solid #e2e8f0;border-radius:14px}
h1{font-size:20px;margin:0 0 12px}p{line-height:1.6;margin:8px 0;font-size:14px}.muted{color:#64748b;font-size:13px}
form{display:flex;gap:8px;flex-wrap:wrap;margin:18px 0 8px}
button{border:1px solid #cbd5e1;background:#fff;border-radius:9px;padding:10px 16px;font-weight:700;font-size:14px;cursor:pointer}
button.primary{background:#2563eb;border-color:#2563eb;color:#fff}a{color:#2563eb}
</style></head><body><main>
<h1>{{.Title}}</h1>
{{range .Lines}}<p>{{.}}</p>{{end}}
{{if .Token}}<form method="post" action="?t={{.Token}}">
<button class="primary" type="submit" name="scope" value="kind">{{.KindLabel}}만 끄기</button>
<button type="submit" name="scope" value="all">모든 메일 끄기</button>
</form>{{end}}
<p class="muted">{{.Note}}{{if .Settings}} <a href="{{.Settings}}">개인 설정 열기</a>{{end}}</p>
</main></body></html>`))

type unsubscribeView struct {
	Title, Service, Token, KindLabel, Note, Settings string
	Lines                                            []string
}

func (a *App) writeUnsubscribePage(w http.ResponseWriter, r *http.Request, status int, view unsubscribeView) {
	view.Service = a.setting(r.Context(), "service.name", "Weekly")
	if base := a.publicBaseURL(r.Context()); base != "" {
		view.Settings = base + "/#/profile"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The token is in this page's address. Nothing on the page loads from
	// elsewhere, and this keeps it out of any Referer all the same.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	if err := unsubscribePage.Execute(w, view); err != nil {
		a.logger.Warn("render unsubscribe page", "error", err)
	}
}

func (a *App) invalidUnsubscribeLink(w http.ResponseWriter, r *http.Request) {
	a.writeUnsubscribePage(w, r, http.StatusBadRequest, unsubscribeView{
		Title: "링크가 올바르지 않습니다",
		Lines: []string{"이 수신 끄기 링크는 이 서비스가 만든 것이 아니거나, 서비스의 암호화 키가 바뀌어 더 이상 쓸 수 없습니다."},
		Note:  "로그인한 뒤 개인 설정 → 주간보고 메일 발송에서 끌 수 있습니다.",
	})
}

// mailUnsubscribePage shows what the link would turn off and asks. It changes
// nothing: mail scanners and link previews open every address in a message.
func (a *App) mailUnsubscribePage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	_, kind, ok := a.parseMailUnsubscribeToken(token)
	if !ok {
		a.invalidUnsubscribeLink(w, r)
		return
	}
	info := mailKinds[kind]
	a.writeUnsubscribePage(w, r, http.StatusOK, unsubscribeView{
		Title:     "메일 수신 끄기",
		Token:     token,
		KindLabel: info.label,
		Lines: []string{
			"이 링크를 받은 메일 주소로 보내는 " + info.label + "을 끕니다.",
			"다른 메일까지 모두 끄려면 '모든 메일 끄기'를 누르십시오.",
		},
		Note: "끈 뒤 다시 받으려면 로그인해서 개인 설정에서 켜십시오.",
	})
}

// mailUnsubscribe turns the mail off. The page's form posts here, and so does
// a mail client's one-click button with the body List-Unsubscribe=One-Click.
func (a *App) mailUnsubscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		a.invalidUnsubscribeLink(w, r)
		return
	}
	token := r.URL.Query().Get("t")
	if token == "" {
		token = r.PostForm.Get("t")
	}
	userID, kind, ok := a.parseMailUnsubscribeToken(token)
	if !ok {
		a.invalidUnsubscribeLink(w, r)
		return
	}
	oneClick := r.PostForm.Get("List-Unsubscribe") == "One-Click"
	kinds := []string{kind}
	if !oneClick && r.PostForm.Get("scope") == "all" {
		kinds = mailKindOrder
	}
	if err := a.turnMailOff(r.Context(), userID, kinds); err != nil {
		a.logger.Error("mail unsubscribe", "error", err, "trace", traceIDFromContext(r.Context()))
		a.writeUnsubscribePage(w, r, http.StatusInternalServerError, unsubscribeView{
			Title: "지금은 처리할 수 없습니다",
			Lines: []string{"잠시 뒤 같은 링크로 다시 시도하십시오."},
			Note:  "로그인한 뒤 개인 설정에서 직접 끌 수도 있습니다.",
		})
		return
	}
	via := "page"
	if oneClick {
		via = "one-click"
	}
	a.auditSystem(r.Context(), "mail.unsubscribe", "user", strconv.FormatInt(userID, 10),
		map[string]any{"kinds": kinds, "via": via})
	labels := make([]string, 0, len(kinds))
	for _, item := range kinds {
		labels = append(labels, mailKinds[item].label)
	}
	a.writeUnsubscribePage(w, r, http.StatusOK, unsubscribeView{
		Title: "수신을 껐습니다",
		Lines: []string{strings.Join(labels, ", ") + "을 더 이상 보내지 않습니다. 이미 발송 대기 중이던 메일도 취소했습니다."},
		Note:  "다시 받으려면 로그인해서 개인 설정에서 켜십시오.",
	})
}

// turnMailOff clears the given kinds for one person and drops what is waiting
// to go to them under those kinds. Columns come from mailKinds, never from the
// request.
func (a *App) turnMailOff(ctx context.Context, userID int64, kinds []string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, kind := range kinds {
		info, known := mailKinds[kind]
		if !known {
			return fmt.Errorf("unknown mail kind %q", kind)
		}
		if _, err := tx.Exec(ctx, `UPDATE user_mail_settings SET `+info.column+`=false, updated_at=now()
			WHERE user_id=$1`, userID); err != nil {
			return err
		}
		var drop string
		switch kind {
		case mailKindReport:
			drop = `DELETE FROM report_mail_deliveries WHERE user_id=$1 AND status='QUEUED'`
		case mailKindSchedule:
			drop = `DELETE FROM schedule_reminder_deliveries WHERE user_id=$1 AND status='QUEUED'`
		case mailKindTeam:
			drop = `DELETE FROM team_reminder_deliveries WHERE recipient_user_id=$1 AND status='QUEUED'`
		}
		if _, err := tx.Exec(ctx, drop, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
