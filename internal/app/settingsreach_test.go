package app

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// A setting nobody can find is a setting nobody has.
//
// The settings screen draws one box per row the API returns, and the API used
// to return the rows of app_settings — so a setting that had never been written
// had no row, no box, and no way in. The ITSM link shipped that way: eleven
// settings defined in the code, described in the README and both guides, and no
// place on any screen to type them. The administrator was told to turn it on in
// 관리자 설정 and there was nothing there to turn on.
//
// Two halves have to hold for that not to happen again, and they fail in
// different places: the API has to offer every setting the code knows, and the
// screen has to put every one of them in a group.

// guards: adminSettings
func TestEverySettingCanBeReachedOnAFreshInstall(t *testing.T) {
	server := newTestServer(t)
	w := server.request(http.MethodGet, "/api/v1/admin/settings", nil, server.admin)
	if w.Code != http.StatusOK {
		t.Fatalf("read the settings: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data []struct {
			Key    string `json:"key"`
			Secret bool   `json:"secret"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the settings: %v", err)
	}
	offered := map[string]bool{}
	secret := map[string]bool{}
	for _, item := range envelope.Data {
		offered[item.Key] = true
		secret[item.Key] = item.Secret
	}
	var missing []string
	for key, definition := range settingDefinitions {
		if !offered[key] {
			missing = append(missing, key)
			continue
		}
		// A secret that arrives marked as ordinary would be drawn in a plain
		// text box and typed in front of whoever is standing there.
		if secret[key] != definition.Secret {
			t.Errorf("%s is secret=%v in the code and %v on the wire", key, definition.Secret, secret[key])
		}
	}
	if len(missing) > 0 {
		t.Errorf("설정 %d개를 화면이 받아 볼 수 없습니다 (한 번도 저장된 적 없는 키): %s",
			len(missing), strings.Join(missing, ", "))
	}
}

// guards: settingDefinitions
func TestEverySettingHasABoxOnTheAdministratorsScreen(t *testing.T) {
	// The screen is the other half. A key the API offers and no group lists is
	// still invisible — this reads the file rather than the browser, which is
	// enough to catch a setting that was added to the code and forgotten here.
	page, err := os.ReadFile("../../frontend/src/pages/AdminPage.tsx")
	if err != nil {
		t.Skipf("설정 화면을 읽을 수 없습니다: %v", err)
	}
	source := string(page)
	var missing []string
	for key := range settingDefinitions {
		if !strings.Contains(source, "'"+key+"'") {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("설정 %d개가 관리자 화면의 어떤 묶음에도 없습니다: %s",
			len(missing), strings.Join(missing, ", "))
	}
}
