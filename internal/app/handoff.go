package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// 서비스 간 문서 넘기기 — 받는 쪽.
//
// Another service issues a short, single-use claim for one document and sends
// the browser here with the claim and its own origin. This service fetches the
// document from that origin with the claim as the only credential and stages
// it as an import job, the same job an upload would have made. No service
// holds another's credentials; the claim is five minutes long and spent on the
// first fetch.
//
// The dangerous parameter is source. It comes from outside, and a service that
// fetches whatever address it is handed is a tool for reading any address on
// the intranet from inside. So the origin is checked against the
// administrator's list before anything is requested, redirects are not
// followed, and the body is bounded in bytes and seconds.

const (
	handoffClaimPath = "/api/v1/handoff/claims/"
	handoffTimeout   = 30 * time.Second
	handoffClaimMax  = 512
	pptxContentType  = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

// handoffEntry is the browser entry point the standard names. It changes no
// state: the claim goes into the fragment, where the SPA reads it after the
// login screen (the hash survives a login), and the request line of every
// later request does not carry it.
func (a *App) handoffEntry(w http.ResponseWriter, r *http.Request) {
	query := url.Values{}
	if source := strings.TrimSpace(r.URL.Query().Get("source")); source != "" {
		query.Set("source", source)
	}
	if claim := strings.TrimSpace(r.URL.Query().Get("claim")); claim != "" {
		query.Set("claim", claim)
	}
	target := "/#/import"
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// normalizeOrigin reduces an address to scheme://host[:port], lower-cased, and
// reports whether it was an origin at all. A path, query, fragment or user
// part means the value is not an origin and is refused rather than trimmed —
// an allow-list entry that says more than an origin is a misunderstanding
// worth surfacing when it is typed, not when a claim fails.
func normalizeOrigin(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return "", false
	}
	return scheme + "://" + strings.ToLower(parsed.Host), true
}

// splitOriginList reads the setting as typed: one origin per line or comma.
func splitOriginList(value string) []string {
	var items []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ';' }) {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// validOriginList accepts an empty list (the default: nobody may hand
// anything over) or a list in which every entry is a bare origin.
func validOriginList(value string) bool {
	if runeLength(value) > 4000 {
		return false
	}
	for _, item := range splitOriginList(value) {
		if _, ok := normalizeOrigin(item); !ok {
			return false
		}
	}
	return true
}

// handoffAllowedOrigins is the administrator's list, normalised.
func (a *App) handoffAllowedOrigins(ctx context.Context) map[string]bool {
	allowed := map[string]bool{}
	for _, item := range splitOriginList(a.setting(ctx, "handoff.allowed_origins", "")) {
		if origin, ok := normalizeOrigin(item); ok {
			allowed[origin] = true
		}
	}
	return allowed
}

// handoffFilename takes the name the sending service attached, or falls back
// to a fixed one, and makes sure the result is a .pptx name with no directory.
func handoffFilename(contentDisposition string) string {
	name := ""
	if _, params, err := mime.ParseMediaType(contentDisposition); err == nil {
		name = params["filename"]
	}
	name = trimRunes(filepath.Base(strings.TrimSpace(name)), 250)
	if name == "" || name == "." || name == "/" {
		name = "handoff"
	}
	if !strings.EqualFold(filepath.Ext(name), ".pptx") {
		name += ".pptx"
	}
	return name
}

// receiveHandoff fetches one claim from an allowed origin and stages it as an
// import job for the signed-in person. Refusals that need no request are made
// before the client is built, in the order the standard lists them.
func (a *App) receiveHandoff(w http.ResponseWriter, r *http.Request) {
	if _, err := a.aiConfig(r.Context(), true); err != nil {
		writeError(w, http.StatusServiceUnavailable, "AI_UNAVAILABLE", "관리자가 AI Gateway를 설정하고 활성화해야 넘겨받은 PPTX를 분석할 수 있습니다.")
		return
	}
	p := currentPrincipal(r.Context())
	var input struct {
		Source string `json:"source"`
		Claim  string `json:"claim"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	claim := strings.TrimSpace(input.Claim)
	if claim == "" || len(claim) > handoffClaimMax || strings.ContainsAny(claim, "/?#%\\ \t") {
		writeError(w, http.StatusBadRequest, "HANDOFF_CLAIM_INVALID", "표(claim)가 비어 있거나 모양이 올바르지 않습니다.")
		return
	}
	origin, ok := normalizeOrigin(input.Source)
	if !ok {
		writeError(w, http.StatusBadRequest, "HANDOFF_SOURCE_INVALID", "source 는 https://ptium.intra 같은 오리진이어야 합니다.")
		return
	}
	// The allow-list gate. Nothing below this line runs for an origin the
	// administrator did not name, and the refusal is made without a request.
	if !a.handoffAllowedOrigins(r.Context())[origin] {
		writeError(w, http.StatusForbidden, "HANDOFF_SOURCE_NOT_ALLOWED", "관리자가 허용한 서비스에서만 문서를 넘겨받을 수 있습니다.")
		return
	}
	limit := int64(a.settingInt(r.Context(), "import.max_file_mb", 25)) << 20

	ctx, cancel := context.WithTimeout(r.Context(), handoffTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+handoffClaimPath+url.PathEscape(claim), nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "HANDOFF_CLAIM_INVALID", "표(claim)가 비어 있거나 모양이 올바르지 않습니다.")
		return
	}
	request.Header.Set("Accept", pptxContentType)
	client := &http.Client{
		Timeout: handoffTimeout,
		// A redirect would be a second address, one the allow-list never saw.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		a.logger.Warn("handoff fetch failed", "source", origin, "error", err)
		writeError(w, http.StatusBadGateway, "HANDOFF_SOURCE_FAILED", "보내는 서비스에서 문서를 받아 오지 못했습니다. 잠시 뒤 다시 보내 달라고 하십시오.")
		return
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 300 && response.StatusCode < 400:
		writeError(w, http.StatusBadGateway, "HANDOFF_REDIRECTED", "보내는 서비스가 다른 주소로 안내했습니다. 넘겨받기는 리다이렉트를 따라가지 않습니다.")
		return
	case response.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "HANDOFF_CLAIM_REJECTED", "표가 만료됐거나 이미 쓰였습니다. 보내는 쪽에서 다시 보내 주십시오.")
		return
	case response.StatusCode != http.StatusOK:
		writeError(w, http.StatusBadGateway, "HANDOFF_SOURCE_FAILED", "보내는 서비스에서 문서를 받아 오지 못했습니다. 잠시 뒤 다시 보내 달라고 하십시오.")
		return
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != pptxContentType {
		writeError(w, http.StatusUnsupportedMediaType, "HANDOFF_UNSUPPORTED_FORMAT", "이 서비스는 PPTX 만 넘겨받을 수 있습니다.")
		return
	}
	tooLarge := fmt.Sprintf("넘겨받을 파일이 관리자가 정한 한 개당 크기 제한(%dMB)을 넘습니다.", limit>>20)
	if response.ContentLength > limit {
		writeError(w, http.StatusRequestEntityTooLarge, "HANDOFF_TOO_LARGE", tooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			writeError(w, http.StatusBadGateway, "HANDOFF_SOURCE_FAILED", "보내는 서비스가 30초 안에 문서를 다 보내지 못했습니다.")
			return
		}
		writeError(w, http.StatusBadGateway, "HANDOFF_SOURCE_FAILED", "문서를 받는 도중 연결이 끊겼습니다.")
		return
	}
	if int64(len(body)) > limit {
		writeError(w, http.StatusRequestEntityTooLarge, "HANDOFF_TOO_LARGE", tooLarge)
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadGateway, "HANDOFF_SOURCE_FAILED", "보내는 서비스가 빈 파일을 보냈습니다.")
		return
	}
	name := handoffFilename(response.Header.Get("Content-Disposition"))

	jobID, jobDirectory, ok := a.openImportJob(w, r, p, 1)
	if !ok {
		return
	}
	queued, failed := 0, 0
	switch a.stageImportBody(r.Context(), p, jobID, jobDirectory, name, body, origin) {
	case importStaged:
		queued++
	case importStageFailed:
		failed++
	}
	status := a.finishImportIntake(r, p, jobID, 1, queued, failed, "import.upload")
	// The claim itself is written nowhere: not here, not in the request log
	// (which records the path only) and not in the answer.
	a.audit(r, p, "handoff.receive", "import_job", fmt.Sprint(jobID), map[string]any{"source": origin, "filename": name, "bytes": len(body)})
	writeData(w, http.StatusAccepted, map[string]any{"id": jobID, "status": status, "filename": name, "source": origin})
}
