package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Every way an API key can stop working answered with one sentence: "로그인이
// 필요합니다". A script cannot sign in. It was the same answer for a mistyped
// key, a key that expired overnight, a key somebody revoked, and a key a
// rotation invalidated — and the product schedules the second of those itself.
//
// guards: authenticate, requireAuth
func TestARefusedKeyIsToldWhatBecameOfIt(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("key_refusal", "USER", nil)

	issue := func() (string, int64) {
		t.Helper()
		created := server.request(http.MethodPost, "/api/v1/keys",
			map[string]any{"name": "검사용", "expiresInDays": 30}, author)
		if created.Code != http.StatusCreated && created.Code != http.StatusOK {
			t.Fatalf("issue a key: %d %s", created.Code, created.Body.String())
		}
		var body struct {
			Data struct {
				ID    int64  `json:"id"`
				Token string `json:"token"`
			} `json:"data"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode the new key: %v", err)
		}
		return body.Data.Token, body.Data.ID
	}

	refusalFor := func(token string) string {
		t.Helper()
		response := server.bearer(http.MethodGet, "/api/v1/reports", token)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("a refused key answered %d: %s", response.Code, response.Body.String())
		}
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode the refusal: %v", err)
		}
		if body.Error.Message == "로그인이 필요합니다." {
			t.Errorf("a key holder was told to sign in, which it cannot do")
		}
		return body.Error.Message
	}

	// A key the server has never seen.
	unknown := refusalFor("wky_" + "0123456789abcdef0123456789abcdef0123456789")
	if !strings.Contains(unknown, "찾을 수 없습니다") {
		t.Errorf("an unknown key: %q", unknown)
	}

	// Something that is not one of our keys at all.
	shape := refusalFor("some-other-systems-token")
	if !strings.Contains(shape, "wky_") {
		t.Errorf("a token of the wrong shape: %q", shape)
	}

	// Expired. The product offers expiresInDays and then has to say so.
	token, id := issue()
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE personal_api_keys SET expires_at = now() - interval '1 day' WHERE id=$1`, id); err != nil {
		t.Fatalf("age the key: %v", err)
	}
	expired := refusalFor(token)
	if !strings.Contains(expired, "만료") {
		t.Errorf("an expired key: %q", expired)
	}

	// Revoked, which is somebody's decision and outranks a date passing.
	token, id = issue()
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE personal_api_keys SET revoked_at = now() WHERE id=$1`, id); err != nil {
		t.Fatalf("revoke the key: %v", err)
	}
	if revoked := refusalFor(token); !strings.Contains(revoked, "회수") {
		t.Errorf("a revoked key: %q", revoked)
	}

	// Rotating is a bulk revocation — it stamps revoked_at on every live key as
	// well as moving the version — so a key caught by one is told it was
	// revoked, which is what happened to it.
	token, _ = issue()
	rotated := server.request(http.MethodPost, "/api/v1/keys/rotate", map[string]any{}, author)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate the keys: %d %s", rotated.Code, rotated.Body.String())
	}
	if message := refusalFor(token); !strings.Contains(message, "회수") {
		t.Errorf("a key invalidated by a rotation: %q", message)
	}

	// The version is a second layer under that stamp, and it catches the one
	// key the stamp cannot: one issued in a request that overlapped the
	// rotation transaction, which reads the old version and is inserted after
	// the sweep has already passed. Its owner did nothing wrong and needs to be
	// told something other than "your key does not exist".
	token, id = issue()
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE users SET key_version = key_version + 1 WHERE id = (SELECT user_id FROM personal_api_keys WHERE id=$1)`,
		id); err != nil {
		t.Fatalf("move the key version: %v", err)
	}
	if message := refusalFor(token); !strings.Contains(message, "일괄 회수") {
		t.Errorf("a key left behind by a rotation: %q", message)
	}
}
