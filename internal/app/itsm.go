package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ITSM 연동: turning an SR number into a line on the board.
//
// The department that plans a month in this product does not invent that work.
// It arrives as a service request in whatever ITSM the company runs, and the
// person building the board has the number in front of them — SR2609-00001 —
// and retypes the title by hand, usually wrongly and usually shortened.
//
// So: type the number, get the title. Two things make that awkward in these
// networks, and both are settings rather than code:
//
//   - Every ITSM answers at a different address in a different shape. The
//     lookup is therefore a URL template and a path into the response, not a
//     driver for one vendor.
//   - The address that answers a machine is rarely the address a person opens.
//     One is an API behind a gateway, the other is a portal page. They are
//     configured separately (itsm.lookup_url and itsm.link_url) because
//     assuming they were the same would give every board a link into an API
//     that renders as raw JSON in a browser.
//
// The link is stored as the number, never as a URL. An ITSM that moves to a new
// portal is then one setting away from every existing row pointing at the right
// place, instead of a data migration.

const (
	// itsmBodyLimit caps what is read from a service nobody here operates.
	itsmBodyLimit = 1 << 20
	// itsmIDLimit is the longest SR number accepted. Long enough for any
	// numbering scheme, short enough that a URL cannot be smuggled in.
	itsmIDLimit = 64
	// itsmDefaultIDPattern is what an SR number looks like when an operator has
	// not said. Letters, digits, dash, underscore, dot — everything that could
	// change the shape of the URL it is substituted into is excluded.
	itsmDefaultIDPattern = `^[A-Za-z0-9._-]{1,64}$`
	itsmDefaultParam     = "sr_id"
	itsmDefaultLabel     = "SR"
)

type itsmSettings struct {
	Enabled   bool
	LookupURL string
	LinkURL   string
	// QueryParam is the name appended when a template carries no {id}, which is
	// the "서비스 주소 + sr_id=SR2609-00001" shape most of these systems use.
	QueryParam string
	TitlePath  string
	TitleRegex string
	IDPattern  string
	AuthHeader string
	AuthToken  string
	Label      string
	Timeout    time.Duration
	// TokenUnreadable says a token is stored that this key cannot decrypt. Like
	// the mail password, that is a configuration state and not a lookup
	// failure, and it is named as one.
	TokenUnreadable bool
}

func (a *App) loadITSMSettings(ctx context.Context) itsmSettings {
	token, err := a.secretSetting(ctx, "itsm.auth_token")
	unreadable := false
	if err != nil {
		if !errors.Is(err, errSecretUnreadable) {
			a.logger.Error("read itsm token", "error", err)
		}
		unreadable, token = true, ""
	}
	settings := itsmSettings{
		TokenUnreadable: unreadable,
		Enabled:         a.settingBool(ctx, "itsm.enabled", false),
		LookupURL:       strings.TrimSpace(a.setting(ctx, "itsm.lookup_url", "")),
		LinkURL:         strings.TrimSpace(a.setting(ctx, "itsm.link_url", "")),
		QueryParam:      strings.TrimSpace(a.setting(ctx, "itsm.query_param", itsmDefaultParam)),
		TitlePath:       strings.TrimSpace(a.setting(ctx, "itsm.title_path", "")),
		TitleRegex:      strings.TrimSpace(a.setting(ctx, "itsm.title_regex", "")),
		IDPattern:       strings.TrimSpace(a.setting(ctx, "itsm.id_pattern", itsmDefaultIDPattern)),
		AuthHeader:      strings.TrimSpace(a.setting(ctx, "itsm.auth_header", "")),
		AuthToken:       token,
		Label:           strings.TrimSpace(a.setting(ctx, "itsm.label", itsmDefaultLabel)),
		Timeout:         time.Duration(a.settingInt(ctx, "itsm.timeout_seconds", 10)) * time.Second,
	}
	if settings.QueryParam == "" {
		settings.QueryParam = itsmDefaultParam
	}
	if settings.IDPattern == "" {
		settings.IDPattern = itsmDefaultIDPattern
	}
	if settings.Label == "" {
		settings.Label = itsmDefaultLabel
	}
	return settings
}

// unusable names what an operator has to fix, or "" when a lookup can be tried.
func (settings itsmSettings) unusable() string {
	if !settings.Enabled {
		return "ITSM 연동이 꺼져 있습니다. 관리자 설정에서 켜십시오."
	}
	if settings.TokenUnreadable {
		return "저장된 ITSM 인증 토큰을 현재 암호화 키로 읽을 수 없습니다. 관리자 설정에서 다시 입력하십시오."
	}
	if settings.LookupURL == "" {
		return "ITSM 조회 주소가 비어 있습니다."
	}
	return ""
}

// validITSMID keeps an operator's pattern from being the only thing between a
// typed string and a URL this server will fetch.
//
// The pattern is configurable because numbering schemes are; the length cap and
// the refusal of anything that is not printable are not, because they are about
// what a URL can be made to mean rather than what an SR is called.
func validITSMID(settings itsmSettings, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("SR 번호를 입력하세요.")
	}
	if len([]rune(id)) > itsmIDLimit {
		return fmt.Errorf("SR 번호는 %d자 이하여야 합니다.", itsmIDLimit)
	}
	pattern, err := regexp.Compile(settings.IDPattern)
	if err != nil {
		// A pattern that does not compile is an operator's mistake, and letting
		// it through would be worse than refusing: it is the only check on what
		// gets substituted into the URL.
		return errors.New("SR 번호 형식 설정이 올바르지 않습니다. 관리자에게 알려 주세요.")
	}
	if !pattern.MatchString(id) {
		return errors.New("SR 번호 형식이 올바르지 않습니다.")
	}
	return nil
}

// itsmURL fills one template with one SR number.
//
// Two shapes, because both are in the wild: a template with {id} in it (a path
// like /portal/sr/{id} needs this), and a plain address the number is appended
// to as a query parameter. The number is escaped either way — it has already
// been matched against the pattern, and escaping it as well costs nothing and
// removes the class of mistake entirely.
func itsmURL(template, param, id string) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", errors.New("주소가 설정되지 않았습니다.")
	}
	if strings.Contains(template, "{id}") || strings.Contains(template, "{sr_id}") {
		filled := strings.NewReplacer(
			"{id}", url.QueryEscape(id),
			"{sr_id}", url.QueryEscape(id),
		).Replace(template)
		return checkedHTTPURL(filled)
	}
	parsed, err := url.Parse(template)
	if err != nil {
		return "", errors.New("주소 형식이 올바르지 않습니다.")
	}
	query := parsed.Query()
	query.Set(param, id)
	parsed.RawQuery = query.Encode()
	return checkedHTTPURL(parsed.String())
}

// checkedHTTPURL refuses everything that is not an ordinary web address, so a
// mistyped setting cannot turn into a request to a file or a local socket.
func checkedHTTPURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("주소 형식이 올바르지 않습니다.")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("주소는 http 또는 https여야 합니다.")
	}
	if parsed.Host == "" {
		return "", errors.New("주소에 호스트가 없습니다.")
	}
	return parsed.String(), nil
}

// itsmLinkURL is where a person goes when they click the number on the board.
//
// Falls back to the lookup address when no separate link is configured, because
// a deployment whose ITSM serves people and machines at the same address should
// not have to say so twice. When they differ — the usual case — the link one
// wins, and nothing on screen ever points at the API.
func (settings itsmSettings) linkFor(id string) string {
	template := settings.LinkURL
	if template == "" {
		template = settings.LookupURL
	}
	link, err := itsmURL(template, settings.QueryParam, id)
	if err != nil {
		return ""
	}
	return link
}

type itsmLookupResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// URL is where a person should be sent, not where the title came from.
	URL string `json:"url,omitempty"`
	// Status and Snippet are only filled for the administrator's test, where
	// the question is why a lookup failed rather than what the title is.
	Status  int    `json:"status,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// itsmHTTPClient is a seam. Production never replaces it; a test needs a server
// it can answer from without one running.
var itsmHTTPClient = func(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// lookupITSM asks the configured service what an SR number is called.
func (a *App) lookupITSM(ctx context.Context, settings itsmSettings, id string) (itsmLookupResult, error) {
	result := itsmLookupResult{ID: id}
	if reason := settings.unusable(); reason != "" {
		return result, errors.New(reason)
	}
	if err := validITSMID(settings, id); err != nil {
		return result, err
	}
	endpoint, err := itsmURL(settings.LookupURL, settings.QueryParam, id)
	if err != nil {
		return result, fmt.Errorf("ITSM 조회 주소가 올바르지 않습니다: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result, errors.New("ITSM 요청을 만들 수 없습니다.")
	}
	request.Header.Set("Accept", "application/json, text/html;q=0.8, */*;q=0.5")
	if settings.AuthHeader != "" && settings.AuthToken != "" {
		request.Header.Set(settings.AuthHeader, settings.AuthToken)
	}
	response, err := itsmHTTPClient(settings.Timeout).Do(request)
	if err != nil {
		// The transport's message names this deployment's network, which is not
		// something the person who typed an SR number can act on. The whole
		// error still reaches the log.
		a.logger.Error("itsm lookup", "error", err, "url", endpoint)
		return result, errors.New("ITSM 서비스에 연결하지 못했습니다. 관리자에게 알려 주세요.")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, itsmBodyLimit))
	result.Status = response.StatusCode
	result.Snippet = trimRunes(strings.TrimSpace(string(body)), 400)
	if response.StatusCode == http.StatusNotFound {
		return result, fmt.Errorf("%s %s 를 ITSM에서 찾지 못했습니다.", settings.Label, id)
	}
	if response.StatusCode >= 400 {
		return result, fmt.Errorf("ITSM이 요청을 거부했습니다 (HTTP %d).", response.StatusCode)
	}
	title, err := itsmTitle(settings, body)
	if err != nil {
		return result, err
	}
	result.Title = title
	result.URL = settings.linkFor(id)
	return result, nil
}

// itsmTitle pulls the title out of whatever came back.
//
// A JSON path first, because that is what an API returns and what an operator
// can point at exactly. A regular expression second, because some of these
// systems have no API at all and the only thing available is the portal page,
// where <title> is the title. Neither is guessed: a deployment that configures
// neither is told so, rather than shown the first string in the document.
func itsmTitle(settings itsmSettings, body []byte) (string, error) {
	if settings.TitlePath != "" {
		var decoded any
		if err := json.Unmarshal(body, &decoded); err == nil {
			if value, ok := jsonPathString(decoded, settings.TitlePath); ok {
				if title := strings.TrimSpace(value); title != "" {
					return trimRunes(title, scheduleTitleLimit), nil
				}
			}
		}
	}
	if settings.TitleRegex != "" {
		pattern, err := regexp.Compile(settings.TitleRegex)
		if err != nil {
			return "", errors.New("제목 추출 정규식 설정이 올바르지 않습니다. 관리자에게 알려 주세요.")
		}
		if match := pattern.FindSubmatch(body); len(match) > 1 {
			if title := strings.TrimSpace(string(match[1])); title != "" {
				return trimRunes(title, scheduleTitleLimit), nil
			}
		}
	}
	if settings.TitlePath == "" && settings.TitleRegex == "" {
		return "", errors.New("제목을 읽을 위치가 설정되지 않았습니다. 관리자 설정에서 제목 경로 또는 정규식을 지정하십시오.")
	}
	return "", errors.New("응답에서 제목을 찾지 못했습니다. 관리자 설정의 제목 경로를 확인하십시오.")
}

// jsonPathString walks a dotted path — data.request.title, results.0.subject —
// through a decoded document. Numeric segments index arrays, because a service
// that returns a one-element list is common enough that requiring an operator
// to write code for it would be the same as not supporting them.
func jsonPathString(document any, path string) (string, bool) {
	current := document
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return "", false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return "", false
			}
			current = node[index]
		default:
			return "", false
		}
	}
	switch value := current.(type) {
	case string:
		return value, true
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(value), true
	}
	return "", false
}

// itsmLookup answers the SR field on the board.
func (a *App) itsmLookup(w http.ResponseWriter, r *http.Request) {
	settings := a.loadITSMSettings(r.Context())
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("srId"))
	}
	result, err := a.lookupITSM(r.Context(), settings, id)
	if err != nil {
		// The lookup is a helper on a form, so its failures are shown next to
		// the field. Only the reason travels; the snippet is an operator's
		// diagnostic and stays in the administrator's test.
		writeError(w, http.StatusBadGateway, "ITSM_LOOKUP_FAILED", err.Error())
		return
	}
	writeData(w, http.StatusOK, itsmLookupResult{ID: result.ID, Title: result.Title, URL: result.URL})
}

// adminITSMTest is the same lookup with the diagnostics left in.
//
// An operator setting this up is debugging a URL template, a header and a path
// into somebody else's JSON. Telling them only "제목을 찾지 못했습니다" makes
// that a guessing game; the status code and the first few hundred characters of
// the answer make it a five second fix.
func (a *App) adminITSMTest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	settings := a.loadITSMSettings(r.Context())
	result, err := a.lookupITSM(r.Context(), settings, strings.TrimSpace(input.ID))
	lookupURL, _ := itsmURL(settings.LookupURL, settings.QueryParam, strings.TrimSpace(input.ID))
	payload := map[string]any{
		"ok": err == nil, "id": result.ID, "title": result.Title, "url": result.URL,
		"lookupUrl": lookupURL, "status": result.Status, "snippet": result.Snippet,
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	a.audit(r, currentPrincipal(r.Context()), "itsm.test", "setting", "itsm", map[string]any{"ok": err == nil})
	writeData(w, http.StatusOK, payload)
}

// itsmLabelOrDefault is what the screens call a service request: "SR" unless an
// operator's ITSM calls it something else.
func itsmLabelOrDefault(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimRunes(trimmed, 40)
	}
	return itsmDefaultLabel
}
