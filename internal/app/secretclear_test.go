package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A stored secret could be set and never removed.
//
// Saving a blank value means "keep what is there" so a masked form cannot wipe
// a password by being submitted — deliberate, and it left no deliberate way out.
// Measured before this test existed: PUT with "" answered
// {"updated":["confluence.password"]} and changed nothing, so the answer named
// a key it had skipped; and PUT with " " encrypted and stored the space, after
// which the setting read back as configured and readable and the relay would
// have been handed a single space as a password.
//
// guards: updateSettings, clearSecretSetting
func TestAStoredSecretCanBeRemovedAndABlankOneIsNotAnUpdate(t *testing.T) {
	server := newTestServer(t)

	set := func(value string) map[string]any {
		w := server.request(http.MethodPut, "/api/v1/admin/settings",
			map[string]any{"settings": map[string]string{"confluence.password": value}}, server.admin)
		if w.Code != http.StatusOK {
			t.Fatalf("설정 저장이 %d: %s", w.Code, w.Body.String())
		}
		return decodeData(t, w)
	}
	stored := func() (configured, available bool) {
		w := server.request(http.MethodGet, "/api/v1/admin/settings", nil, server.admin)
		if w.Code != http.StatusOK {
			t.Fatalf("설정 조회가 %d: %s", w.Code, w.Body.String())
		}
		var envelope struct {
			Data []settingView `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		for _, item := range envelope.Data {
			if item.Key == "confluence.password" {
				return item.Configured, item.Available
			}
		}
		t.Fatal("confluence.password 설정이 목록에 없습니다")
		return false, false
	}

	set("relaysecret1234")
	if configured, available := stored(); !configured || !available {
		t.Fatalf("비밀을 저장했는데 설정됨=%v 읽힘=%v", configured, available)
	}

	// Blank keeps it — and must not claim otherwise.
	if updated := set(""); len(updated["updated"].([]any)) != 0 {
		t.Errorf("빈 값을 건너뛰고도 바꿨다고 답합니다: %v", updated["updated"])
	}
	if configured, _ := stored(); !configured {
		t.Error("빈 값이 저장된 비밀을 지웠습니다")
	}

	// Whitespace is somebody emptying the field, not a password.
	if updated := set("   "); len(updated["updated"].([]any)) != 0 {
		t.Errorf("공백만 있는 값을 저장했다고 답합니다: %v", updated["updated"])
	}
	if configured, available := stored(); !configured || !available {
		t.Errorf("공백을 저장해 비밀을 덮어썼습니다: 설정됨=%v 읽힘=%v", configured, available)
	}

	// The deliberate path.
	w := server.request(http.MethodDelete, "/api/v1/admin/settings/confluence.password", nil, server.admin)
	if w.Code != http.StatusOK {
		t.Fatalf("비밀 지우기가 %d: %s", w.Code, w.Body.String())
	}
	if configured, _ := stored(); configured {
		t.Error("지운 뒤에도 비밀이 설정된 것으로 남아 있습니다")
	}

	// Only secrets, and only administrators.
	if w := server.request(http.MethodDelete, "/api/v1/admin/settings/workflow.week_start", nil, server.admin); w.Code != http.StatusBadRequest {
		t.Errorf("비밀이 아닌 설정을 지우려 할 때 %d, 400을 기대합니다: %s", w.Code, w.Body.String())
	}
	if w := server.request(http.MethodDelete, "/api/v1/admin/settings/nope.nothing", nil, server.admin); w.Code != http.StatusNotFound {
		t.Errorf("없는 설정을 지우려 할 때 %d, 404를 기대합니다", w.Code)
	}
	writer := server.createUser("secretclearer", "USER", nil)
	if w := server.request(http.MethodDelete, "/api/v1/admin/settings/confluence.password", nil, writer); w.Code != http.StatusForbidden {
		t.Errorf("작성자가 비밀을 지울 수 있습니다: %d", w.Code)
	}
}
