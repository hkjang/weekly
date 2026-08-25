package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The README tells operators that in the compatibility mode the volume's
// instance.key is the only copy of the key, so losing the volume loses every
// secret setting. The product knew which mode it was in and said so once, in a
// boot log line — gone by the time anyone inherits the deployment and asks.

func decodeEncryption(t *testing.T, w *httptest.ResponseRecorder) encryptionView {
	t.Helper()
	var envelope struct {
		Data encryptionView `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return envelope.Data
}

// guards: adminEncryption
func TestAnAdministratorCanSeeWhetherALostVolumeCostsTheSecrets(t *testing.T) {
	server := newTestServer(t)

	// The harness runs without WEEKLY_ENCRYPTION_KEY, which is the mode the
	// README warns about.
	before := decodeEncryption(t, server.request(http.MethodGet, "/api/v1/admin/encryption", nil, server.admin))
	if before.KeySource != "state_volume" {
		t.Fatalf("this deployment stores the key in the volume, the API says %q", before.KeySource)
	}
	if before.StateDirectory == "" {
		t.Fatal("the answer does not name the directory an operator has to protect")
	}
	// Nothing is encrypted yet, so nothing is at risk yet.
	if before.StoredSecrets != 0 || !before.Recoverable {
		t.Fatalf("with no secrets stored nothing is at risk: %+v", before)
	}

	saved := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled":  "true",
			"ai.endpoint": "http://ai.invalid/v1/chat/completions",
			"ai.model":    "stub",
			"ai.api_key":  "a-secret-worth-losing",
		},
	}, server.admin)
	if saved.Code != http.StatusOK {
		t.Fatalf("store a secret setting: %d %s", saved.Code, saved.Body.String())
	}

	after := decodeEncryption(t, server.request(http.MethodGet, "/api/v1/admin/encryption", nil, server.admin))
	if after.StoredSecrets < 1 {
		t.Fatalf("a secret was stored and the count says %d", after.StoredSecrets)
	}
	// Counted now, not at boot: the key stored this afternoon is the one at risk.
	if after.Recoverable {
		t.Fatalf("the only copy of the key is in the volume and a secret depends on it, yet the answer says it is recoverable: %+v", after)
	}
}

// guards: adminEncryption
func TestTheEncryptionModeIsNotReadableWithoutBeingAnAdministrator(t *testing.T) {
	server := newTestServer(t)
	writer := server.createUser("keyreader", "USER", nil)
	w := server.request(http.MethodGet, "/api/v1/admin/encryption", nil, writer)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a writer read the deployment's key mode: %d %s", w.Code, w.Body.String())
	}
}
