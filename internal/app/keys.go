package app

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type keyView struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	KeyVersion int        `json:"keyVersion"`
	Scopes     []string   `json:"scopes"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

func (a *App) listKeys(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	rows, err := a.db.Query(r.Context(), `SELECT id,name,prefix,key_version,scopes,last_used_at,expires_at,created_at FROM personal_api_keys WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, p.ID)
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "API 키를 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	result := []keyView{}
	for rows.Next() {
		var item keyView
		if err := rows.Scan(&item.ID, &item.Name, &item.Prefix, &item.KeyVersion, &item.Scopes, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt); err != nil {
			writeError(w, 500, "QUERY_FAILED", "API 키를 조회할 수 없습니다.")
			return
		}
		result = append(result, item)
	}
	writeData(w, 200, map[string]any{"keyVersion": p.KeyVersion, "keys": result})
}

func (a *App) createKey(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	var input struct {
		Name          string   `json:"name"`
		ExpiresInDays int      `json:"expiresInDays"`
		Scopes        []string `json:"scopes"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		writeError(w, 400, "INVALID_KEY_NAME", "키 이름은 1~120자로 입력하세요.")
		return
	}
	maximum := a.settingInt(r.Context(), "security.api_key_max_days", 365)
	if input.ExpiresInDays <= 0 {
		input.ExpiresInDays = maximum
	}
	if input.ExpiresInDays > maximum {
		writeError(w, 400, "INVALID_EXPIRY", "API 키 유효기간이 관리자 제한을 초과합니다.")
		return
	}
	allowed := map[string]bool{"reports:read": true, "analytics:read": true, "mcp:read": true}
	if len(input.Scopes) == 0 {
		input.Scopes = []string{"reports:read", "analytics:read", "mcp:read"}
	}
	for _, scope := range input.Scopes {
		if !allowed[scope] {
			writeError(w, 400, "INVALID_SCOPE", "허용되지 않은 API 키 범위입니다.")
			return
		}
	}
	random, err := randomToken(32)
	if err != nil {
		writeError(w, 500, "KEY_ERROR", "API 키를 생성할 수 없습니다.")
		return
	}
	token := "wky_" + random
	prefix := token
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	expires := time.Now().AddDate(0, 0, input.ExpiresInDays)
	var id int64
	err = a.db.QueryRow(r.Context(), `INSERT INTO personal_api_keys(user_id,name,prefix,token_hash,key_version,scopes,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, p.ID, input.Name, prefix, tokenHash(token), p.KeyVersion, input.Scopes, expires).Scan(&id)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "API 키를 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "key.create", "api_key", strconv.FormatInt(id, 10), map[string]any{"prefix": prefix, "scopes": input.Scopes})
	writeData(w, 201, map[string]any{"id": id, "token": token, "prefix": prefix, "expiresAt": expires, "warning": "이 키는 지금 한 번만 표시됩니다."})
}

func (a *App) rotateKeys(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "키를 회전할 수 없습니다.")
		return
	}
	defer tx.Rollback(r.Context())
	var version int
	err = tx.QueryRow(r.Context(), `UPDATE users SET key_version=key_version+1,updated_at=now() WHERE id=$1 RETURNING key_version`, p.ID).Scan(&version)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE personal_api_keys SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, p.ID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 500, "DATABASE_ERROR", "키를 회전할 수 없습니다.")
		return
	}
	a.audit(r, p, "key.rotate", "user", strconv.FormatInt(p.ID, 10), map[string]any{"keyVersion": version})
	writeData(w, 200, map[string]any{"keyVersion": version, "revokedAll": true})
}

func (a *App) revokeKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	command, err := a.db.Exec(r.Context(), `UPDATE personal_api_keys SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, id, p.ID)
	if err != nil || command.RowsAffected() == 0 {
		writeError(w, 404, "KEY_NOT_FOUND", "API 키를 찾을 수 없습니다.")
		return
	}
	a.audit(r, p, "key.revoke", "api_key", strconv.FormatInt(id, 10), nil)
	writeData(w, 200, map[string]bool{"revoked": true})
}
