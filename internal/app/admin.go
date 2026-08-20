package app

import (
	"errors"
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
	"auth.max_login_attempts":             {Validate: integerRange(0, 1000)},
	"auth.max_login_attempts_per_ip":      {Validate: integerRange(0, 10000)},
	"auth.lockout_minutes":                {Validate: integerRange(1, 1440)},
	"oidc.enabled":                        {Validate: booleanValue},
	"oidc.issuer_url":                     {Validate: validOptionalURL},
	"oidc.client_id":                      {Validate: bounded(0, 255)},
	"oidc.client_secret":                  {Secret: true, Validate: bounded(0, 2048)},
	"oidc.redirect_url":                   {Validate: validOptionalURL},
	"oidc.scopes":                         {Validate: bounded(1, 500)},
	"oidc.username_claim":                 {Validate: bounded(1, 120)},
	"oidc.groups_claim":                   {Validate: bounded(1, 120)},
	"oidc.admin_group":                    {Validate: bounded(0, 255)},
	"oidc.auto_provision":                 {Validate: booleanValue},
	"security.api_key_max_days":           {Validate: integerRange(1, 3650)},
	"analytics.retention_days":            {Validate: integerRange(1, 3650)},
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

func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT id,username,display_name,coalesce(email,''),role,organization_id,manager_id,active,key_version,last_login_at,created_at FROM users ORDER BY display_name`)
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
	writeData(w, 200, result)
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
		if strings.Contains(err.Error(), "users_username_key") {
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
	if !decodeJSON(w, r, &input) {
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
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	if id == currentPrincipal(r.Context()).ID && (!active || input.Role != "ADMIN") {
		writeError(w, 400, "SELF_LOCKOUT", "현재 관리자 계정을 비활성화하거나 관리자 권한을 제거할 수 없습니다.")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "사용자를 저장할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	command, err := tx.Exec(r.Context(), `UPDATE users SET display_name=$1,email=$2,role=$3,organization_id=$4,manager_id=$5,active=$6,updated_at=now() WHERE id=$7`, input.DisplayName, strings.TrimSpace(input.Email), input.Role, input.OrganizationID, input.ManagerID, active, id)
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

func (a *App) auditLogs(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT a.id,a.actor_id,coalesce(u.display_name,'시스템'),a.action,a.resource_type,coalesce(a.resource_id,''),a.detail,a.ip_address::text,a.created_at FROM audit_logs a LEFT JOIN users u ON u.id=a.actor_id ORDER BY a.created_at DESC LIMIT 500`)
	if err != nil {
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
	writeData(w, 200, result)
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
