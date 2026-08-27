package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgconn"
)

type settingDefinition struct {
	Secret   bool
	Validate func(string) bool
}

var settingDefinitions = map[string]settingDefinition{
	"service.name":        {Validate: bounded(1, 80)},
	"service.notice":      {Validate: bounded(0, 500)},
	"service.timezone":    {Validate: validTimezone},
	"workflow.enabled":    {Validate: booleanValue},
	"workflow.week_start": {Validate: oneOf("SUNDAY", "MONDAY", "TUESDAY", "WEDNESDAY", "THURSDAY", "FRIDAY", "SATURDAY")},
	// Days after the week start, and the hour of that day. 24 means "by the end
	// of that day", which is how a deadline is usually stated out loud.
	"workflow.deadline_days": {Validate: integerRange(0, 13)},
	"workflow.deadline_hour": {Validate: integerRange(1, 24)},
	"auth.local_enabled":     {Validate: booleanValue},
	"auth.session_hours":     {Validate: integerRange(1, 720)},
	// Zero disables each limit; the account limit ships enabled and the address
	// limit ships disabled. See loginThrottleFor for why they differ.
	"auth.max_login_attempts":        {Validate: integerRange(0, 1000)},
	"auth.max_login_attempts_per_ip": {Validate: integerRange(0, 10000)},
	"auth.lockout_minutes":           {Validate: integerRange(1, 1440)},
	"oidc.enabled":                   {Validate: booleanValue},
	"oidc.issuer_url":                {Validate: validOptionalURL},
	"oidc.client_id":                 {Validate: bounded(0, 255)},
	"oidc.client_secret":             {Secret: true, Validate: bounded(0, 2048)},
	"oidc.redirect_url":              {Validate: validOptionalURL},
	"oidc.scopes":                    {Validate: bounded(1, 500)},
	"oidc.username_claim":            {Validate: bounded(1, 120)},
	"oidc.groups_claim":              {Validate: bounded(1, 120)},
	"oidc.admin_group":               {Validate: bounded(0, 255)},
	"oidc.auto_provision":            {Validate: booleanValue},
	"security.api_key_max_days":      {Validate: integerRange(1, 3650)},
	"analytics.retention_days":       {Validate: integerRange(1, 3650)},
	// Zero keeps everything. An operator whose policy demands indefinite
	// retention should be able to say so rather than discover the trail was
	// trimmed for them.
	"audit.retention_days":                {Validate: integerRange(0, 3650)},
	"rollup.merge_similarity":             {Validate: integerRange(0, 100)},
	"rollup.stall_weeks":                  {Validate: integerRange(2, 12)},
	"rollup.persistent_issue_weeks":       {Validate: integerRange(2, 12)},
	"rollup.max_weeks":                    {Validate: integerRange(1, 400)},
	"attachment.max_per_report":           {Validate: integerRange(1, 100)},
	"attachment.max_file_mb":              {Validate: integerRange(1, 50)},
	"search.similarity_threshold":         {Validate: integerRange(1, 99)},
	"search.semantic_threshold":           {Validate: integerRange(1, 99)},
	"ai.embedding_enabled":                {Validate: booleanValue},
	"ai.embedding_model":                  {Validate: bounded(0, 255)},
	"ai.embedding_endpoint":               {Validate: validOptionalURL},
	"ai.enabled":                          {Validate: booleanValue},
	"ai.endpoint":                         {Validate: validOptionalURL},
	"ai.api_key":                          {Secret: true, Validate: bounded(0, 4096)},
	"ai.model":                            {Validate: bounded(0, 255)},
	"ai.timeout_seconds":                  {Validate: integerRange(5, 300)},
	"ai.max_input_chars":                  {Validate: integerRange(1000, 200000)},
	"import.max_files":                    {Validate: integerRange(1, 100)},
	"import.max_file_mb":                  {Validate: integerRange(1, 100)},
	"import.retention_days":               {Validate: integerRange(1, 3650)},
	"confluence.enabled":                  {Validate: booleanValue},
	"confluence.base_url":                 {Validate: validConfluenceBaseURL},
	"confluence.auth_mode":                {Validate: oneOf("BASIC", "NONE")},
	"confluence.username":                 {Validate: bounded(0, 255)},
	"confluence.password":                 {Secret: true, Validate: bounded(0, 4096)},
	"confluence.include_spaces":           {Validate: bounded(0, 4000)},
	"confluence.exclude_spaces":           {Validate: bounded(0, 4000)},
	"confluence.sync_interval_minutes":    {Validate: integerRange(5, 1440)},
	"confluence.ai_enabled":               {Validate: booleanValue},
	"confluence.minimum_candidate_score":  {Validate: integerRange(-100, 200)},
	"confluence.ai_review_min_score":      {Validate: integerRange(-100, 200)},
	"confluence.analyze_body":             {Validate: booleanValue},
	"confluence.lookback_days":            {Validate: integerRange(1, 90)},
	"confluence.batch_size":               {Validate: integerRange(1, 200)},
	"confluence.include_blogs":            {Validate: booleanValue},
	"confluence.auto_map_email_localpart": {Validate: booleanValue},
	"confluence.auto_map_username":        {Validate: booleanValue},
	"confluence.work_keywords":            {Validate: bounded(0, 4000)},
	"confluence.personal_space_prefixes":  {Validate: bounded(0, 2000)},
	"confluence.score_project_space":      {Validate: integerRange(-100, 100)},
	"confluence.score_creator":            {Validate: integerRange(-100, 100)},
	"confluence.score_modifier":           {Validate: integerRange(-100, 100)},
	"confluence.score_work_keyword":       {Validate: integerRange(-100, 100)},
	"confluence.score_meeting":            {Validate: integerRange(-100, 100)},
	"confluence.score_notice":             {Validate: integerRange(-100, 100)},
	"confluence.score_leave":              {Validate: integerRange(-100, 100)},
	"confluence.score_personal_space":     {Validate: integerRange(-100, 100)},
	// Sending a finished report to an address the writer chose. The relay these
	// deployments have is usually an internal one on port 25 with nothing to
	// authenticate against, so NONE is a first-class choice rather than a
	// fallback — but a username without encryption is refused, because Go's
	// SMTP client will not put a password on a plaintext connection and saying
	// so here beats failing at send time.
	"mail.enabled":         {Validate: booleanValue},
	"mail.host":            {Validate: bounded(0, 255)},
	"mail.port":            {Validate: integerRange(1, 65535)},
	"mail.security":        {Validate: oneOf("NONE", "STARTTLS", "TLS")},
	"mail.username":        {Validate: bounded(0, 255)},
	"mail.password":        {Secret: true, Validate: bounded(0, 4096)},
	"mail.from":            {Validate: validOptionalMailAddress},
	"mail.from_name":       {Validate: bounded(0, 120)},
	"mail.timeout_seconds": {Validate: integerRange(5, 300)},
	// How many times one report is retried before it is given up on. The queue
	// is not a mailbox: a report that has not left after five tries is a relay
	// problem, and repeating it forever hides that.
	"mail.max_attempts": {Validate: integerRange(1, 20)},
}

type settingView struct {
	Key        string    `json:"key"`
	Value      string    `json:"value,omitempty"`
	Secret     bool      `json:"secret"`
	Configured bool      `json:"configured"`
	Available  bool      `json:"available"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (a *App) adminSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT key,value,secret,updated_at FROM app_settings ORDER BY key`)
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "설정을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := []settingView{}
	for rows.Next() {
		var item settingView
		var stored string
		if err := rows.Scan(&item.Key, &stored, &item.Secret, &item.UpdatedAt); err != nil {
			writeError(w, 500, "QUERY_FAILED", "설정을 조회할 수 없습니다.")
			return
		}
		item.Configured = stored != ""
		item.Available = true
		if !item.Secret {
			item.Value = stored
		} else if item.Configured {
			_, decryptErr := a.box.Decrypt(stored)
			item.Available = decryptErr == nil
		}
		result = append(result, item)
	}
	writeData(w, 200, result)
}

func (a *App) updateSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Settings map[string]string `json:"settings"`
		// Confirmed lists settings whose consequences the administrator has been
		// shown and accepted. A change that quietly rewrites what past data
		// means should cost one deliberate answer.
		Confirmed []string `json:"confirmed"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Settings) == 0 || len(input.Settings) > len(settingDefinitions) {
		writeError(w, 400, "INVALID_SETTINGS", "변경할 설정을 입력하세요.")
		return
	}
	if enabled, ok := input.Settings["ai.enabled"]; ok && enabled == "true" {
		endpoint := a.setting(r.Context(), "ai.endpoint", "")
		model := a.setting(r.Context(), "ai.model", "")
		if value, exists := input.Settings["ai.endpoint"]; exists {
			endpoint = value
		}
		if value, exists := input.Settings["ai.model"]; exists {
			model = value
		}
		if !validOptionalURL(endpoint) || strings.TrimSpace(endpoint) == "" || strings.TrimSpace(model) == "" {
			writeError(w, 400, "AI_CONFIGURATION_REQUIRED", "AI Endpoint와 모델을 입력한 뒤 AI 기능을 활성화하세요.")
			return
		}
	}
	if enabled, ok := input.Settings["confluence.enabled"]; ok && enabled == "true" {
		baseURL := a.setting(r.Context(), "confluence.base_url", "")
		authMode := a.setting(r.Context(), "confluence.auth_mode", "BASIC")
		username := a.setting(r.Context(), "confluence.username", "")
		password, _ := a.secretSetting(r.Context(), "confluence.password")
		if value, exists := input.Settings["confluence.base_url"]; exists {
			baseURL = value
		}
		if value, exists := input.Settings["confluence.auth_mode"]; exists {
			authMode = value
		}
		if value, exists := input.Settings["confluence.username"]; exists {
			username = value
		}
		if value, exists := input.Settings["confluence.password"]; exists && value != "" {
			password = value
		}
		if !validConfluenceBaseURL(baseURL) || strings.TrimSpace(baseURL) == "" {
			writeError(w, 400, "CONFLUENCE_CONFIGURATION_REQUIRED", "Confluence Base URL을 입력한 뒤 연동을 활성화하세요.")
			return
		}
		if authMode == "BASIC" && (strings.TrimSpace(username) == "" || password == "") {
			writeError(w, 400, "CONFLUENCE_CREDENTIAL_REQUIRED", "Basic Auth 계정과 비밀번호를 입력한 뒤 연동을 활성화하세요.")
			return
		}
	}
	if local, ok := input.Settings["auth.local_enabled"]; ok && local == "false" {
		if _, err := a.oidcConfig(r.Context()); err != nil {
			writeError(w, 400, "LOGIN_REQUIRED", "검증된 OIDC 설정을 먼저 저장·활성화한 뒤 로컬 로그인을 끄세요.")
			return
		}
	}
	keys := make([]string, 0, len(input.Settings))
	for key, value := range input.Settings {
		definition, ok := settingDefinitions[key]
		if !ok || !definition.Validate(value) {
			writeError(w, 400, "INVALID_SETTING", key+" 설정값이 올바르지 않습니다.")
			return
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Checked after validation: a value that is not a weekday should be refused
	// on its own terms, not after a scan of every stored report.
	if weekday, ok := input.Settings["workflow.week_start"]; ok {
		if !a.weekStartChangeAllowed(w, r, weekday, input.Confirmed) {
			return
		}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "설정을 저장할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	p := currentPrincipal(r.Context())
	for _, key := range keys {
		value := input.Settings[key]
		definition := settingDefinitions[key]
		if definition.Secret {
			// Blank secret means keep the existing secret. This prevents accidental removal by masked forms.
			if value == "" {
				continue
			}
			value, err = a.box.Encrypt(value)
			if err != nil {
				writeError(w, 500, "ENCRYPTION_ERROR", "비밀 설정을 암호화할 수 없습니다.")
				return
			}
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO app_settings(key,value,secret,updated_by,updated_at) VALUES($1,$2,$3,$4,now())
			ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,secret=EXCLUDED.secret,updated_by=EXCLUDED.updated_by,updated_at=now()`, key, value, definition.Secret, p.ID)
		if err != nil {
			writeError(w, 500, "DATABASE_ERROR", "설정을 저장할 수 없습니다.")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "설정을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "settings.update", "settings", "global", map[string]any{"keys": keys})
	writeData(w, 200, map[string]any{"updated": keys})
}

func (a *App) testOIDC(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.oidcConfig(r.Context())
	if err != nil {
		writeError(w, 400, "OIDC_CONFIGURATION_INVALID", "OIDC 설정이 완전하지 않습니다.")
		return
	}
	provider, err := oidc.NewProvider(r.Context(), cfg.Issuer)
	if err != nil {
		writeError(w, 502, "OIDC_DISCOVERY_FAILED", "Keycloak OIDC Discovery 연결에 실패했습니다: "+err.Error())
		return
	}
	var metadata struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil {
		writeError(w, 502, "OIDC_METADATA_FAILED", "OIDC 메타데이터를 읽을 수 없습니다.")
		return
	}
	writeData(w, 200, map[string]any{"ok": true, "issuer": metadata.Issuer, "authorizationEndpoint": metadata.AuthorizationEndpoint, "tokenEndpoint": metadata.TokenEndpoint})
}

type userView struct {
	ID             int64      `json:"id"`
	Username       string     `json:"username"`
	DisplayName    string     `json:"displayName"`
	Email          string     `json:"email"`
	Role           string     `json:"role"`
	OrganizationID *int64     `json:"organizationId"`
	ManagerID      *int64     `json:"managerId"`
	Active         bool       `json:"active"`
	KeyVersion     int        `json:"keyVersion"`
	LastLoginAt    *time.Time `json:"lastLoginAt"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// userListView carries a page of accounts with the size of the set it came
// from, so the screen can never present part of a directory as all of it.
type userListView struct {
	Items  []userView `json:"items"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
	Query  string     `json:"query,omitempty"`
	Roles  []string   `json:"roles,omitempty"`
	// Unassigned counts every account with no organisation, whatever the
	// current search is. Those accounts are invisible to every team leader, so
	// the number is a property of the deployment rather than of this page, and
	// an administrator has no other way to learn it: the directory has no
	// filter for a blank column, and reading one by eye means paging through
	// everybody.
	Unassigned int `json:"unassigned"`
	// Only string here so an empty filter stays out of the payload.
	Organization string `json:"organization,omitempty"`
}

const (
	userPageDefault = 100
	userPageMaximum = 500
)

// adminUsers lists accounts, searchable and paged.
//
// It used to return every row. On a 300 person deployment that was 63 KB and
// a table nobody could navigate; at ten thousand it is megabytes, and the only
// way to reach one account was the browser's own find-in-page over whatever
// had rendered. So the cap arrives together with a search — a capped list with
// no way to look something up is worse than an unbounded one, because the
// account you need is simply gone.
//
// The search matches login, display name and email, because an administrator
// arrives with whichever of the three they were given.
func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := clampQueryInt(r, "limit", userPageDefault, 1, userPageMaximum)
	offset := clampQueryInt(r, "offset", 0, 0, 1_000_000)

	// A role filter, because one caller is not browsing the directory at all:
	// the 검토 책임자 picker needs the handful of people who can review, and
	// building it from whichever page of the table happened to load would
	// silently drop reviewers as the organisation grew.
	roles := []string{}
	for _, role := range strings.Split(r.URL.Query().Get("role"), ",") {
		role = strings.ToUpper(strings.TrimSpace(role))
		if validRole(role) {
			roles = append(roles, role)
		}
	}

	where, args := "", []any{}
	conditions := []string{}
	if query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		conditions = append(conditions, fmt.Sprintf(
			`(lower(username) LIKE $%d OR lower(display_name) LIKE $%d OR lower(coalesce(email,'')) LIKE $%d)`,
			len(args), len(args), len(args)))
	}
	if len(roles) > 0 {
		args = append(args, roles)
		conditions = append(conditions, fmt.Sprintf(`role = ANY($%d)`, len(args)))
	}
	organization := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("organization")))
	if organization != "none" {
		organization = ""
	}
	if organization == "none" {
		conditions = append(conditions, `organization_id IS NULL`)
	}
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM users`+where, args...).Scan(&total); err != nil {
		writeError(w, 500, "QUERY_FAILED", "사용자를 조회할 수 없습니다.")
		return
	}
	args = append(args, limit, offset)
	statement := `SELECT id,username,display_name,coalesce(email,''),role,organization_id,manager_id,active,key_version,last_login_at,created_at
		FROM users` + where + fmt.Sprintf(` ORDER BY display_name, id LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	rows, err := a.db.Query(r.Context(), statement, args...)
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "사용자를 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := []userView{}
	for rows.Next() {
		var user userView
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Email, &user.Role, &user.OrganizationID, &user.ManagerID, &user.Active, &user.KeyVersion, &user.LastLoginAt, &user.CreatedAt); err != nil {
			writeError(w, 500, "QUERY_FAILED", "사용자를 조회할 수 없습니다.")
			return
		}
		result = append(result, user)
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "QUERY_FAILED", "사용자를 조회할 수 없습니다.")
		return
	}
	var unassigned int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE organization_id IS NULL`).Scan(&unassigned); err != nil {
		a.logger.Warn("count accounts without an organisation", "error", err)
	}
	writeData(w, 200, userListView{Items: result, Total: total, Limit: limit, Offset: offset, Query: query, Roles: roles,
		Unassigned: unassigned, Organization: organization})
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{2,120}$`)

// organizations.code is varchar(60), narrower than the username column, so it
// needs its own bound rather than reusing the username pattern.
var organizationCodePattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{2,60}$`)

// validateProfileLengths keeps display name and email inside their column
// widths, counted in characters like PostgreSQL counts them.
func validateProfileLengths(displayName, email string) error {
	if runeLength(displayName) > 120 {
		return errors.New("표시 이름은 120자 이하로 입력하세요.")
	}
	if runeLength(email) > 255 {
		return errors.New("이메일은 255자 이하로 입력하세요.")
	}
	return nil
}

func (a *App) createUser(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username, DisplayName, Email, Password, Role string
		OrganizationID, ManagerID                    *int64
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)
	input.Role = strings.ToUpper(input.Role)
	if !usernamePattern.MatchString(input.Username) || input.DisplayName == "" || !validRole(input.Role) {
		writeError(w, 400, "INVALID_USER", "사용자 이름, 표시 이름 또는 역할이 올바르지 않습니다.")
		return
	}
	if err := validateProfileLengths(input.DisplayName, input.Email); err != nil {
		writeError(w, 400, "INVALID_USER", err.Error())
		return
	}
	var passwordHash any
	if input.Password != "" {
		if len(input.Password) < 12 {
			writeError(w, 400, "WEAK_PASSWORD", "비밀번호는 12자 이상이어야 합니다.")
			return
		}
		hash, err := hashPassword(input.Password)
		if err != nil {
			writeError(w, 500, "PASSWORD_ERROR", "비밀번호를 처리할 수 없습니다.")
			return
		}
		passwordHash = hash
	}
	var id int64
	err := a.db.QueryRow(r.Context(), `INSERT INTO users(username,display_name,email,password_hash,role,organization_id,manager_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, input.Username, input.DisplayName, strings.TrimSpace(input.Email), passwordHash, input.Role, input.OrganizationID, input.ManagerID).Scan(&id)
	if err != nil {
		// The constraint by name, from the structured error rather than the
		// message text. Two unique constraints live on this table — username
		// and oidc_subject — so the code alone would conflate an administrator
		// reusing an id with an SSO subject already claimed, and those are
		// different mistakes with different fixes. Reading the name out of
		// err.Error() worked, but only for as long as the driver keeps
		// formatting the sentence that way.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_username_key" {
			writeError(w, 409, "USERNAME_EXISTS", "이미 사용 중인 아이디입니다.")
		} else {
			a.logger.Error("create user", "error", err, "trace", traceIDFromContext(r.Context()))
			writeError(w, 500, "DATABASE_ERROR", "사용자를 만들 수 없습니다.")
		}
		return
	}
	a.audit(r, currentPrincipal(r.Context()), "user.create", "user", strconv.FormatInt(id, 10), map[string]any{"username": input.Username, "role": input.Role})
	writeData(w, 201, map[string]int64{"id": id})
}

func (a *App) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		DisplayName, Email, Password, Role string
		OrganizationID, ManagerID          *int64
		Active                             *bool
	}
	// Every column below is rewritten, so a caller who mentions only the field
	// it means to change used to blank the rest. Resetting a password wiped the
	// address, dropped the account out of its organisation — leaving it in the
	// queue no team leader can open — and, because the flag defaulted to true,
	// handed a disabled account its sign-in back. An omission must never move
	// access in that direction.
	sent, ok := decodeJSONFields(w, r, &input)
	if !ok {
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)
	input.Role = strings.ToUpper(input.Role)
	if input.DisplayName == "" || !validRole(input.Role) {
		writeError(w, 400, "INVALID_USER", "표시 이름 또는 역할이 올바르지 않습니다.")
		return
	}
	if err := validateProfileLengths(input.DisplayName, input.Email); err != nil {
		writeError(w, 400, "INVALID_USER", err.Error())
		return
	}
	var passwordHash string
	if input.Password != "" {
		if len(input.Password) < 12 {
			writeError(w, 400, "WEAK_PASSWORD", "비밀번호는 12자 이상이어야 합니다.")
			return
		}
		passwordHash, _ = hashPassword(input.Password)
	}
	// A body that says nothing about activity leaves it alone, so an edit made
	// for another reason cannot re-enable an account by silence.
	setActive := sent["active"] && input.Active != nil
	active := setActive && *input.Active
	if id == currentPrincipal(r.Context()).ID && ((setActive && !active) || input.Role != "ADMIN") {
		writeError(w, 400, "SELF_LOCKOUT", "현재 관리자 계정을 비활성화하거나 관리자 권한을 제거할 수 없습니다.")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "사용자를 저장할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	command, err := tx.Exec(r.Context(), `UPDATE users SET display_name=$1,
			email=CASE WHEN $2 THEN $3 ELSE email END,
			role=$4,
			organization_id=CASE WHEN $5 THEN $6 ELSE organization_id END,
			manager_id=CASE WHEN $7 THEN $8 ELSE manager_id END,
			active=CASE WHEN $9 THEN $10 ELSE active END,
			updated_at=now() WHERE id=$11`,
		input.DisplayName,
		sent["email"], strings.TrimSpace(input.Email),
		input.Role,
		sent["organizationid"], input.OrganizationID,
		sent["managerid"], input.ManagerID,
		setActive, active, id)
	if err != nil || command.RowsAffected() == 0 {
		writeError(w, 404, "USER_NOT_FOUND", "사용자를 찾을 수 없습니다.")
		return
	}
	if input.Password != "" {
		_, err = tx.Exec(r.Context(), `UPDATE users SET password_hash=$1 WHERE id=$2`, passwordHash, id)
		if err != nil {
			writeError(w, 500, "DATABASE_ERROR", "비밀번호를 저장할 수 없습니다.")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "사용자를 저장할 수 없습니다.")
		return
	}
	a.audit(r, currentPrincipal(r.Context()), "user.update", "user", strconv.FormatInt(id, 10), map[string]any{"role": input.Role, "active": active})
	writeData(w, 200, map[string]int64{"id": id})
}

type organizationView struct {
	ID        int64  `json:"id"`
	ParentID  *int64 `json:"parentId"`
	Name      string `json:"name"`
	Code      string `json:"code"`
	UserCount int    `json:"userCount"`
}

func (a *App) adminOrganizations(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT o.id,o.parent_id,o.name,o.code,count(u.id) FROM organizations o LEFT JOIN users u ON u.organization_id=o.id GROUP BY o.id ORDER BY o.name`)
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "조직을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := []organizationView{}
	for rows.Next() {
		var item organizationView
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Name, &item.Code, &item.UserCount); err != nil {
			writeError(w, 500, "QUERY_FAILED", "조직을 조회할 수 없습니다.")
			return
		}
		result = append(result, item)
	}
	writeData(w, 200, result)
}

func (a *App) createOrganization(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name, Code string
		ParentID   *int64
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Code = strings.ToUpper(strings.TrimSpace(input.Code))
	if input.Name == "" || !organizationCodePattern.MatchString(input.Code) {
		writeError(w, 400, "INVALID_ORGANIZATION", "조직 코드는 영문, 숫자와 ._@- 기호로 2~60자여야 합니다.")
		return
	}
	if runeLength(input.Name) > 120 {
		writeError(w, 400, "INVALID_ORGANIZATION", "조직 이름은 120자 이하로 입력하세요.")
		return
	}
	var id int64
	err := a.db.QueryRow(r.Context(), `INSERT INTO organizations(name,code,parent_id) VALUES($1,$2,$3) RETURNING id`, input.Name, input.Code, input.ParentID).Scan(&id)
	if err != nil {
		// Only a duplicate code is the caller's mistake. Anything else is a
		// server fault and must not be reported as a conflict.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, 409, "ORGANIZATION_EXISTS", "이미 사용 중인 조직 코드입니다.")
			return
		}
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			writeError(w, 400, "INVALID_PARENT", "상위 조직을 찾을 수 없습니다.")
			return
		}
		a.logger.Error("create organization", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, 500, "DATABASE_ERROR", "조직을 만들 수 없습니다.")
		return
	}
	a.audit(r, currentPrincipal(r.Context()), "organization.create", "organization", strconv.FormatInt(id, 10), map[string]any{"code": input.Code})
	writeData(w, 201, map[string]int64{"id": id})
}

// auditLogs answers questions of the trail rather than printing the end of it.
//
// Filters and paging exist because the previous behaviour — the last 500 rows,
// nothing else — made the stored answer unreachable within days of use. The
// total is returned alongside the page so a screen can say how much matched
// instead of implying that what it shows is all there is.
const (
	auditPageDefault = 50
	auditPageMaximum = 200
)

func (a *App) auditLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	// clampQueryInt, like every other paged list, rather than a second version
	// of the same rule. Written out by hand this endpoint answered limit=0 with
	// the default while /reports answered it with 1 — one sentence in the
	// contract, two behaviours, and a client that learned the rule from one list
	// got a different answer from this one.
	limit := clampQueryInt(r, "limit", auditPageDefault, 1, auditPageMaximum)
	offset := clampQueryInt(r, "offset", 0, 0, 1_000_000)

	where := " WHERE 1=1"
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(clause, len(args))
	}
	if value := strings.TrimSpace(query.Get("action")); value != "" {
		// Prefix match, so "report" finds every report action without the caller
		// having to know the full list.
		add(" AND a.action LIKE $%d", value+"%")
	}
	if value := strings.TrimSpace(query.Get("resourceType")); value != "" {
		add(" AND a.resource_type = $%d", value)
	}
	if value := strings.TrimSpace(query.Get("resourceId")); value != "" {
		add(" AND a.resource_id = $%d", value)
	}
	if value := strings.TrimSpace(query.Get("actor")); value != "" {
		// One value compared against two columns, so the placeholder is
		// referenced twice rather than bound twice.
		args = append(args, "%"+value+"%")
		where += fmt.Sprintf(" AND (u.username ILIKE $%d OR u.display_name ILIKE $%d)", len(args), len(args))
	}
	// A day means a day in the service timezone. Resolving the boundary in the
	// database session timezone instead would drop the first nine hours of the
	// requested day in a KST deployment and include them from the next one —
	// the same mistake the submission deadline carried until v0.18.0.
	zone := a.setting(r.Context(), "service.timezone", "Asia/Seoul")
	for _, bound := range []struct {
		name   string
		clause string
		label  string
	}{
		{"from", " AND a.created_at >= ($%d::date)::timestamp AT TIME ZONE $%d", "조회 시작일"},
		{"to", " AND a.created_at < ($%d::date + 1)::timestamp AT TIME ZONE $%d", "조회 종료일"},
	} {
		value := strings.TrimSpace(query.Get(bound.name))
		if value == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", value); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_RANGE", bound.label+"은 YYYY-MM-DD 형식이어야 합니다.")
			return
		}
		args = append(args, value, zone)
		where += fmt.Sprintf(bound.clause, len(args)-1, len(args))
	}

	var total int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM audit_logs a LEFT JOIN users u ON u.id=a.actor_id`+where, args...).Scan(&total); err != nil {
		a.logger.Error("count audit logs", "error", err)
		writeError(w, 500, "QUERY_FAILED", "감사 로그를 조회할 수 없습니다.")
		return
	}
	args = append(args, limit, offset)
	statement := `SELECT a.id,a.actor_id,coalesce(u.display_name,'시스템'),a.action,a.resource_type,coalesce(a.resource_id,''),a.detail,a.ip_address::text,a.created_at
		FROM audit_logs a LEFT JOIN users u ON u.id=a.actor_id` + where +
		fmt.Sprintf(" ORDER BY a.created_at DESC, a.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := a.db.Query(r.Context(), statement, args...)
	if err != nil {
		a.logger.Error("list audit logs", "error", err)
		writeError(w, 500, "QUERY_FAILED", "감사 로그를 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id int64
		var actorID *int64
		var actor, action, resourceType, resourceID string
		var detail any
		var ip *string
		var created time.Time
		if err := rows.Scan(&id, &actorID, &actor, &action, &resourceType, &resourceID, &detail, &ip, &created); err != nil {
			writeError(w, 500, "QUERY_FAILED", "감사 로그를 조회할 수 없습니다.")
			return
		}
		result = append(result, map[string]any{"id": id, "actorId": actorID, "actor": actor, "action": action, "resourceType": resourceType, "resourceId": resourceID, "detail": detail, "ipAddress": ip, "createdAt": created})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "QUERY_FAILED", "감사 로그를 조회할 수 없습니다.")
		return
	}
	writeData(w, 200, map[string]any{
		"items": result, "total": total, "limit": limit, "offset": offset,
		"retentionDays": a.settingInt(r.Context(), "audit.retention_days", 365),
	})
}

// bounded counts characters, not bytes, so a Korean value is measured the same
// way an operator counts it while typing.
func bounded(minimum, maximum int) func(string) bool {
	return func(v string) bool { return runeLength(v) >= minimum && runeLength(v) <= maximum }
}
func booleanValue(v string) bool { return v == "true" || v == "false" }
func oneOf(values ...string) func(string) bool {
	return func(v string) bool {
		for _, item := range values {
			if v == item {
				return true
			}
		}
		return false
	}
}
func integerRange(minimum, maximum int) func(string) bool {
	return func(v string) bool { n, err := strconv.Atoi(v); return err == nil && n >= minimum && n <= maximum }
}
func validOptionalURL(v string) bool {
	if v == "" {
		return true
	}
	parsed, err := url.Parse(v)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
func validConfluenceBaseURL(v string) bool {
	if v == "" {
		return true
	}
	parsed, err := url.Parse(v)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}
func validRole(role string) bool {
	return role == "USER" || role == "TEAM_LEADER" || role == "ORG_MANAGER" || role == "ADMIN"
}

func validTimezone(value string) bool {
	if len(value) < 1 || len(value) > 120 {
		return false
	}
	_, err := time.LoadLocation(value)
	return err == nil
}

// encryptionView answers a question the README poses and the product could not:
// if this deployment loses its state volume, do the secret settings survive?
//
// The answer used to live in one line of the boot log. Months later, on a
// deployment someone else installed, that line is gone and the administrator has
// no way to tell which mode they are in — while the backup instructions depend
// on knowing.
type encryptionView struct {
	// KeySource is "environment" or "state_volume".
	KeySource string `json:"keySource"`
	// StoredSecrets is counted now rather than at boot: a key added this
	// afternoon is exactly the one at risk.
	StoredSecrets int `json:"storedSecrets"`
	// Recoverable is false when the only copy of the key is inside the volume
	// and there is something encrypted with it.
	Recoverable    bool   `json:"recoverable"`
	StateDirectory string `json:"stateDirectory"`
}

func (a *App) adminEncryption(w http.ResponseWriter, r *http.Request) {
	var stored int
	if err := a.db.QueryRow(r.Context(),
		`SELECT count(*) FROM app_settings WHERE secret AND value <> ''`).Scan(&stored); err != nil {
		writeError(w, 500, "QUERY_FAILED", "비밀 설정 상태를 조회할 수 없습니다.")
		return
	}
	writeData(w, 200, encryptionView{
		KeySource:      a.keySource,
		StoredSecrets:  stored,
		Recoverable:    a.keySource != "state_volume" || stored == 0,
		StateDirectory: stateDirectory,
	})
}
