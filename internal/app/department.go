package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Keycloak 부서(department)로 소속 조직 자동 지정.
//
// Every account SSO created landed in the default organisation, where no team
// leader looks, and stayed there until an administrator chose the right one by
// hand. On a deployment of a few hundred people that is a few hundred edits,
// repeated every time somebody changes team — and the person whose reports go
// nowhere in the meantime has no way to know.
//
// Keycloak already knows where everybody sits. A User Attribute mapper puts the
// person's department in the token, and this file turns that value into an
// organisation:
//
//  1. a mapping the administrator made by hand (organization_department_aliases)
//  2. an organisation whose code is the value, or the code this file generates
//     for the value when it creates one
//  3. an organisation whose name is the value — only if exactly one has it
//
// Two organisations with the same name is an answer this file refuses to guess
// at: putting somebody under the wrong 기획팀 sends their reports to a stranger.
// It records the value and leaves the account where it was, and the
// administrator's department screen lists the value with both candidates.

const (
	departmentSyncOff    = "OFF"
	departmentSyncFill   = "FILL"
	departmentSyncAlways = "ALWAYS"
)

type departmentSettings struct {
	// Claim is where the value is read from. Dotted paths reach into nested
	// claims, because a Keycloak mapper whose claim name has a dot produces one.
	Claim string
	// Mode is OFF (record only), FILL (only accounts with no organisation or the
	// default one) or ALWAYS (follow the directory at every sign-in).
	Mode string
	// Create makes an organisation for a value nothing matches.
	Create bool
}

func (a *App) departmentSettings(ctx context.Context) departmentSettings {
	mode := strings.ToUpper(strings.TrimSpace(a.setting(ctx, "oidc.department_sync", departmentSyncOff)))
	switch mode {
	case departmentSyncFill, departmentSyncAlways:
	default:
		mode = departmentSyncOff
	}
	return departmentSettings{
		Claim:  strings.TrimSpace(a.setting(ctx, "oidc.department_claim", "department")),
		Mode:   mode,
		Create: a.settingBool(ctx, "oidc.department_create", false),
	}
}

// departmentClaim reads the department from claims.
//
// Keycloak's User Attribute mapper sends a string, or an array when the
// attribute is multivalued, and a claim name containing dots becomes nested
// objects. All three shapes are accepted; an array answers with its first
// non-empty value, which is the department Keycloak lists first.
func departmentClaim(claims map[string]any, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || claims == nil {
		return ""
	}
	// The literal key first: a claim really named "org.department" is not a path.
	if value, ok := claims[path]; ok {
		return departmentValue(value)
	}
	var current any = claims
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = object[part]
		if !ok {
			return ""
		}
	}
	return departmentValue(current)
}

func departmentValue(value any) string {
	switch typed := value.(type) {
	case string:
		return normaliseDepartment(typed)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if normalised := normaliseDepartment(text); normalised != "" {
					return normalised
				}
			}
		}
	case []string:
		for _, text := range typed {
			if normalised := normaliseDepartment(text); normalised != "" {
				return normalised
			}
		}
	}
	return ""
}

// normaliseDepartment trims and collapses inner whitespace, and caps the value
// at the column width. "개발 1팀 " and "개발 1팀" are one department.
func normaliseDepartment(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if runes := []rune(value); len(runes) > 255 {
		value = string(runes[:255])
	}
	return value
}

// departmentKey is how two spellings of a value are compared: case does not
// distinguish departments, and neither does spacing.
func departmentKey(value string) string {
	return strings.ToLower(normaliseDepartment(value))
}

// generatedDepartmentCode is the code an organisation created for a value
// gets, and the code a later sign-in looks it up by. Deterministic, so the
// organisation is found again after an administrator renames it, and so two
// sign-ins racing to create it agree on what they are creating.
//
// A value that is already a valid code (ASCII, as Keycloak deployments often
// use: "DEV01") is used as is; anything else — every Korean department name —
// gets a short digest of its key.
func generatedDepartmentCode(value string) string {
	upper := strings.ToUpper(normaliseDepartment(value))
	if organizationCodePattern.MatchString(upper) {
		return upper
	}
	sum := sha256.Sum256([]byte(departmentKey(value)))
	return "DEPT-" + strings.ToUpper(hex.EncodeToString(sum[:])[:10])
}

// departmentDirectory is the organisation chart as the resolver needs it,
// read once per request so a page of a hundred accounts is two queries and
// not two hundred.
type departmentDirectory struct {
	aliases   map[string]int64
	byCode    map[string]int64
	byName    map[string][]int64
	names     map[int64]string
	defaultID int64
}

type departmentMatch struct {
	OrganizationID *int64 `json:"organizationId"`
	// Via is alias, code, name, ambiguous or unmatched.
	Via string `json:"via"`
	// Candidates are the organisations an ambiguous name could mean.
	Candidates []int64 `json:"candidates,omitempty"`
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadDepartmentDirectory(ctx context.Context, db queryer) (*departmentDirectory, error) {
	directory := &departmentDirectory{
		aliases: map[string]int64{}, byCode: map[string]int64{},
		byName: map[string][]int64{}, names: map[int64]string{},
	}
	rows, err := db.Query(ctx, `SELECT id, code, name FROM organizations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var code, name string
		if err := rows.Scan(&id, &code, &name); err != nil {
			rows.Close()
			return nil, err
		}
		directory.byCode[strings.ToUpper(code)] = id
		key := departmentKey(name)
		directory.byName[key] = append(directory.byName[key], id)
		directory.names[id] = name
		if code == "DEFAULT" {
			directory.defaultID = id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = db.Query(ctx, `SELECT department, organization_id FROM organization_department_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var department string
		var id int64
		if err := rows.Scan(&department, &id); err != nil {
			return nil, err
		}
		directory.aliases[department] = id
	}
	return directory, rows.Err()
}

func (d *departmentDirectory) resolve(value string) departmentMatch {
	key := departmentKey(value)
	if key == "" {
		return departmentMatch{Via: "unmatched"}
	}
	if id, ok := d.aliases[key]; ok {
		return departmentMatch{OrganizationID: &id, Via: "alias"}
	}
	for _, code := range []string{strings.ToUpper(normaliseDepartment(value)), generatedDepartmentCode(value)} {
		if id, ok := d.byCode[code]; ok {
			return departmentMatch{OrganizationID: &id, Via: "code"}
		}
	}
	switch candidates := d.byName[key]; len(candidates) {
	case 0:
		return departmentMatch{Via: "unmatched"}
	case 1:
		id := candidates[0]
		return departmentMatch{OrganizationID: &id, Via: "name"}
	default:
		return departmentMatch{Via: "ambiguous", Candidates: append([]int64(nil), candidates...)}
	}
}

// departmentSyncTimeout bounds what a sign-in spends placing somebody. The
// person is waiting on a redirect, and a sync that cannot finish is skipped,
// never allowed to fail the sign-in.
const departmentSyncTimeout = 5 * time.Second

// syncUserDepartment records what the provider said about where somebody sits
// and, if the administrator has asked for it, moves them there.
//
// It never fails a sign-in. Every error is logged and the account is left as it
// was: a person who cannot be placed is still a person who can sign in, and the
// administrator's screen shows the value that could not be used.
func (a *App) syncUserDepartment(r *http.Request, userID int64, department string, settings departmentSettings, via string) {
	department = normaliseDepartment(department)
	if department == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), departmentSyncTimeout)
	defer cancel()

	var current *int64
	var locked bool
	err := a.db.QueryRow(ctx, `UPDATE users SET sso_department=$2, sso_department_at=now()
		WHERE id=$1 RETURNING organization_id, organization_locked`, userID, department).
		Scan(&current, &locked)
	if err != nil {
		a.logger.Warn("record sso department", "user_id", userID, "error", err)
		return
	}
	if settings.Mode == departmentSyncOff || locked {
		return
	}
	directory, err := loadDepartmentDirectory(ctx, a.db)
	if err != nil {
		a.logger.Warn("read organisations for department sync", "user_id", userID, "error", err)
		return
	}
	if settings.Mode == departmentSyncFill && current != nil && *current != directory.defaultID {
		return
	}
	match := directory.resolve(department)
	created := false
	if match.OrganizationID == nil && match.Via == "unmatched" && settings.Create {
		id, made, err := a.createDepartmentOrganization(ctx, department)
		if err != nil {
			a.logger.Warn("create organisation for department", "department", department, "error", err)
			return
		}
		match, created = departmentMatch{OrganizationID: &id, Via: "code"}, made
	}
	if match.OrganizationID == nil {
		// Unmatched or ambiguous: the value is recorded above, and the
		// administrator's department screen lists it with what it could mean.
		return
	}
	if current != nil && *current == *match.OrganizationID {
		return
	}
	// organization_locked is asked again in the write: an administrator who
	// pins somebody between the read above and this line has the last word.
	command, err := a.db.Exec(ctx, `UPDATE users SET organization_id=$2, updated_at=now()
		WHERE id=$1 AND NOT organization_locked`, userID, *match.OrganizationID)
	if err != nil {
		a.logger.Warn("apply sso department", "user_id", userID, "error", err)
		return
	}
	if command.RowsAffected() == 0 {
		return
	}
	actor := &principal{ID: userID}
	if created {
		a.audit(r, actor, "organization.department_create", "organization", strconv.FormatInt(*match.OrganizationID, 10),
			map[string]any{"department": department, "via": via})
	}
	a.audit(r, actor, "user.department_sync", "user", strconv.FormatInt(userID, 10), map[string]any{
		"department": department, "from": current, "to": *match.OrganizationID, "match": match.Via, "via": via,
	})
}

// createDepartmentOrganization makes the organisation a department value
// names, at the top of the chart where an administrator will see it and can
// move it. Two sign-ins from the same new department race here; the generated
// code is the same for both, so one inserts and the other finds its row.
func (a *App) createDepartmentOrganization(ctx context.Context, department string) (int64, bool, error) {
	name := normaliseDepartment(department)
	if runeLength(name) > 120 {
		name = string([]rune(name)[:120])
	}
	code := generatedDepartmentCode(department)
	var id int64
	err := a.db.QueryRow(ctx, `INSERT INTO organizations(name, code, mail_blocked) VALUES($1, $2, $3)
		ON CONFLICT (code) DO NOTHING RETURNING id`, name, code, defaultMailBlocked(name)).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	err = a.db.QueryRow(ctx, `SELECT id FROM organizations WHERE code=$1`, code).Scan(&id)
	return id, false, err
}

// --- 관리자: 부서 값 현황, 매핑, 일괄 적용 ---------------------------------

type departmentRow struct {
	Department       string          `json:"department"`
	Users            int             `json:"users"`
	InOrganization   int             `json:"inOrganization"`
	Locked           int             `json:"locked"`
	Pending          int             `json:"pending"`
	OrganizationID   *int64          `json:"organizationId"`
	OrganizationName string          `json:"organizationName,omitempty"`
	Via              string          `json:"via"`
	Candidates       []departmentOrg `json:"candidates,omitempty"`
	SuggestedCode    string          `json:"suggestedCode"`
	LastSeenAt       *time.Time      `json:"lastSeenAt"`
}

type departmentOrg struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type departmentOverview struct {
	Mode   string          `json:"mode"`
	Claim  string          `json:"claim"`
	Create bool            `json:"create"`
	Items  []departmentRow `json:"items"`
	// WithoutDepartment counts active SSO accounts the provider has never sent
	// a department for — the size of what a missing mapper is costing.
	WithoutDepartment int `json:"withoutDepartment"`
	Pending           int `json:"pending"`
}

// adminDepartments lists every department value the provider has sent, what
// each one resolves to, and how many accounts are not where it says.
//
// Problems first: a value nothing matches, then a name two organisations
// share, then values with people waiting to move. A screen sorted by name puts
// the one row that needs a decision somewhere on the third page.
func (a *App) adminDepartments(w http.ResponseWriter, r *http.Request) {
	settings := a.departmentSettings(r.Context())
	directory, err := loadDepartmentDirectory(r.Context(), a.db)
	if err != nil {
		a.logger.Error("read organisations for departments", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, 500, "QUERY_FAILED", "부서 현황을 조회할 수 없습니다.")
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT min(sso_department), organization_id, organization_locked,
			count(*), max(sso_department_at)
		FROM users
		WHERE sso_department IS NOT NULL AND btrim(sso_department) <> '' AND active
		GROUP BY lower(btrim(sso_department)), organization_id, organization_locked`)
	if err != nil {
		a.logger.Error("read departments", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, 500, "QUERY_FAILED", "부서 현황을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	byKey := map[string]*departmentRow{}
	for rows.Next() {
		var department string
		var organization *int64
		var locked bool
		var count int
		var seen *time.Time
		if err := rows.Scan(&department, &organization, &locked, &count, &seen); err != nil {
			writeError(w, 500, "QUERY_FAILED", "부서 현황을 조회할 수 없습니다.")
			return
		}
		key := departmentKey(department)
		row := byKey[key]
		if row == nil {
			match := directory.resolve(department)
			row = &departmentRow{Department: normaliseDepartment(department), OrganizationID: match.OrganizationID,
				Via: match.Via, SuggestedCode: generatedDepartmentCode(department)}
			if match.OrganizationID != nil {
				row.OrganizationName = directory.names[*match.OrganizationID]
			}
			for _, id := range match.Candidates {
				row.Candidates = append(row.Candidates, departmentOrg{ID: id, Name: directory.names[id]})
			}
			byKey[key] = row
		}
		row.Users += count
		if seen != nil && (row.LastSeenAt == nil || seen.After(*row.LastSeenAt)) {
			row.LastSeenAt = seen
		}
		switch {
		case row.OrganizationID != nil && organization != nil && *organization == *row.OrganizationID:
			row.InOrganization += count
		case locked:
			row.Locked += count
		case row.OrganizationID != nil:
			row.Pending += count
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "QUERY_FAILED", "부서 현황을 조회할 수 없습니다.")
		return
	}
	overview := departmentOverview{Mode: settings.Mode, Claim: settings.Claim, Create: settings.Create, Items: []departmentRow{}}
	for _, row := range byKey {
		overview.Items = append(overview.Items, *row)
		overview.Pending += row.Pending
	}
	rank := func(row departmentRow) int {
		switch {
		case row.Via == "unmatched":
			return 0
		case row.Via == "ambiguous":
			return 1
		case row.Pending > 0:
			return 2
		}
		return 3
	}
	sort.Slice(overview.Items, func(i, j int) bool {
		left, right := overview.Items[i], overview.Items[j]
		if rank(left) != rank(right) {
			return rank(left) < rank(right)
		}
		if left.Users != right.Users {
			return left.Users > right.Users
		}
		return left.Department < right.Department
	})
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM users
		WHERE active AND oidc_subject IS NOT NULL AND (sso_department IS NULL OR btrim(sso_department) = '')`).
		Scan(&overview.WithoutDepartment); err != nil {
		a.logger.Warn("count accounts without a department", "error", err)
	}
	writeData(w, 200, overview)
}

// updateDepartmentMapping ties a department value to an organisation, or
// removes the tie when organizationId is null.
func (a *App) updateDepartmentMapping(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Department     string `json:"department"`
		OrganizationID *int64 `json:"organizationId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	key := departmentKey(input.Department)
	if key == "" {
		writeError(w, 400, "INVALID_DEPARTMENT", "부서 값을 입력하세요.")
		return
	}
	p := currentPrincipal(r.Context())
	if input.OrganizationID == nil {
		if _, err := a.db.Exec(r.Context(), `DELETE FROM organization_department_aliases WHERE department=$1`, key); err != nil {
			a.logger.Error("remove department mapping", "error", err, "trace", traceIDFromContext(r.Context()))
			writeError(w, 500, "DATABASE_ERROR", "부서 매핑을 지울 수 없습니다.")
			return
		}
	} else if _, err := a.db.Exec(r.Context(), `INSERT INTO organization_department_aliases(department, organization_id, updated_by, updated_at)
		VALUES($1, $2, $3, now())
		ON CONFLICT (department) DO UPDATE SET organization_id=EXCLUDED.organization_id, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		key, *input.OrganizationID, p.ID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			writeError(w, 400, "ORGANIZATION_NOT_FOUND", "그런 조직이 없습니다.")
			return
		}
		a.logger.Error("save department mapping", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, 500, "DATABASE_ERROR", "부서 매핑을 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "department.map", "department", key, map[string]any{
		"department": normaliseDepartment(input.Department), "organizationId": input.OrganizationID,
	})
	writeData(w, 200, map[string]any{"department": normaliseDepartment(input.Department), "organizationId": input.OrganizationID})
}

// applyDepartments moves every account whose department resolves to an
// organisation it is not in — except the ones an administrator has pinned.
//
// The sign-in does this one person at a time, so a mapping made today would
// otherwise reach somebody only when they next sign in, which for a person on
// leave is weeks. An empty department applies every value.
//
// This is the administrator's explicit instruction and does not consult the
// sync mode: OFF means "do not do it at sign-in", not "never".
func (a *App) applyDepartments(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Department string `json:"department"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	key := departmentKey(input.Department)
	directory, err := loadDepartmentDirectory(r.Context(), a.db)
	if err != nil {
		a.logger.Error("read organisations for department apply", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, 500, "QUERY_FAILED", "조직을 조회할 수 없습니다.")
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT id, sso_department, organization_id FROM users
		WHERE sso_department IS NOT NULL AND btrim(sso_department) <> '' AND active AND NOT organization_locked
		  AND ($1::boolean OR lower(btrim(sso_department)) = $2)`, key == "", key)
	if err != nil {
		a.logger.Error("read accounts for department apply", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, 500, "QUERY_FAILED", "사용자를 조회할 수 없습니다.")
		return
	}
	type move struct {
		user     int64
		from, to *int64
	}
	moves := []move{}
	for rows.Next() {
		var id int64
		var department string
		var current *int64
		if err := rows.Scan(&id, &department, &current); err != nil {
			rows.Close()
			writeError(w, 500, "QUERY_FAILED", "사용자를 조회할 수 없습니다.")
			return
		}
		match := directory.resolve(department)
		if match.OrganizationID == nil || (current != nil && *current == *match.OrganizationID) {
			continue
		}
		moves = append(moves, move{user: id, from: current, to: match.OrganizationID})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeError(w, 500, "QUERY_FAILED", "사용자를 조회할 수 없습니다.")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "조직을 적용할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	moved := []int64{}
	for _, item := range moves {
		command, err := tx.Exec(r.Context(), `UPDATE users SET organization_id=$2, updated_at=now()
			WHERE id=$1 AND NOT organization_locked`, item.user, *item.to)
		if err != nil {
			a.logger.Error("apply department", "error", err, "trace", traceIDFromContext(r.Context()))
			writeError(w, 500, "DATABASE_ERROR", "조직을 적용할 수 없습니다.")
			return
		}
		if command.RowsAffected() > 0 {
			moved = append(moved, item.user)
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "조직을 적용할 수 없습니다.")
		return
	}
	detail := map[string]any{"department": normaliseDepartment(input.Department), "moved": len(moved)}
	if len(moved) <= 200 {
		detail["users"] = moved
	}
	resource := key
	if resource == "" {
		resource = "*"
	}
	a.audit(r, currentPrincipal(r.Context()), "department.apply", "department", resource, detail)
	writeData(w, 200, map[string]any{"moved": len(moved)})
}

// departmentOrganizations resolves each listed account's department in one pass,
// for the directory screen's "부서 기준 조직" column.
func departmentOrganizations(directory *departmentDirectory, users []userView) {
	if directory == nil {
		return
	}
	for index := range users {
		if users[index].SSODepartment == nil {
			continue
		}
		match := directory.resolve(*users[index].SSODepartment)
		users[index].DepartmentOrganizationID = match.OrganizationID
		users[index].DepartmentMatch = match.Via
	}
}
