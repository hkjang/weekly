package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The settings carry the company mail standard's names and its `auto`
// security, so an operator who set up one service can set up this one without
// learning a second vocabulary. `auto` is the mode a new install starts in, and
// it has to do the right thing against both relays these networks have: the
// plain one on port 25, and the one that offers STARTTLS.

// guards: sendMail, mailSettings.unusable, mailSettings.tlsConfig
func TestAutoSecurityTakesTheEncryptedRoadOnlyWhenTheRelayOffersIt(t *testing.T) {
	app := &App{}
	base := mailSettings{Enabled: true, Security: "AUTO", From: "weekly@internal.test", FromName: "주간보고",
		Timeout: 10 * time.Second, MaxAttempts: 3}

	// A plain relay: auto sends, and sends plain, exactly as NONE would have.
	plain := startFakeRelay(t)
	plain.mu.Lock()
	plain.tlsConfig = nil
	plain.mu.Unlock()
	settings := base
	settings.Host, settings.Port = plain.hostPort()
	if reason := settings.unusable(); reason != "" {
		t.Fatalf("auto on a plain relay was refused before sending: %s", reason)
	}
	if err := app.sendMail(context.Background(), settings, "reader@internal.test", "제목", "본문"); err != nil {
		t.Fatalf("auto against a plain relay: %v", err)
	}
	plain.mu.Lock()
	secured := plain.secured
	plain.mu.Unlock()
	if secured {
		t.Error("a relay that offered no STARTTLS was upgraded anyway")
	}

	// The same plain relay with an account: the password must not travel, and
	// the refusal names what to change rather than repeating Go's.
	withAccount := settings
	withAccount.Username, withAccount.Password = "weekly", "s3cret"
	if reason := withAccount.unusable(); reason != "" {
		t.Fatalf("auto with an account was refused before the relay could answer: %s", reason)
	}
	err := app.sendMail(context.Background(), withAccount, "reader@internal.test", "제목", "본문")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS를 제공하지 않아") {
		t.Fatalf("a password over auto on a plain relay: err=%v, want the STARTTLS refusal", err)
	}
	if messages := plain.received(); len(messages) != 1 {
		t.Fatalf("the plain relay holds %d messages, want only the anonymous one", len(messages))
	}

	// A relay that offers STARTTLS: auto upgrades and the account goes through.
	upgraded := startTLSRelay(t, "weekly", "s3cret")
	restore := mailTLSForTest(upgraded)
	defer restore()
	settings = withAccount
	settings.Host, settings.Port = upgraded.hostPort()
	if err := app.sendMail(context.Background(), settings, "reader@internal.test", "제목", "본문"); err != nil {
		t.Fatalf("auto against a STARTTLS relay: %v", err)
	}
	upgraded.mu.Lock()
	secured, authed := upgraded.secured, upgraded.authed
	upgraded.mu.Unlock()
	if !secured || !authed {
		t.Errorf("auto on a STARTTLS relay: secured=%v authed=%v, want both", secured, authed)
	}

	// Verification is on unless the operator turned it off, and the switch
	// reaches the configuration the dial uses.
	if settings.tlsConfig().InsecureSkipVerify {
		t.Error("certificate verification was off without being asked")
	}
	settings.SkipTLSVerify = true
	if !settings.tlsConfig().InsecureSkipVerify {
		t.Error("mail.skip_tls_verify did not reach the TLS configuration")
	}
}

// The standard's names are what the settings API knows, the relay works through
// them end to end, and the password is not among what comes back.

// guards: loadMailSettings
func TestTheMailSettingsSpeakTheStandardsNames(t *testing.T) {
	s := newTestServer(t)
	relay := startFakeRelay(t)
	host, port := relay.hostPort()
	w := s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{
		"mail.enabled": "true", "mail.smtp_host": host, "mail.smtp_port": fmt.Sprint(port),
		"mail.security": "auto", "mail.skip_tls_verify": "false", "mail.from_address": "weekly@internal.test",
		"mail.username": "", "mail.password": "hunter2",
	}}, s.admin)
	if w.Code != http.StatusOK {
		t.Fatalf("save the standard's keys: %d %s", w.Code, w.Body.String())
	}
	// The old names are gone from the API, not merely unused: a screen that
	// still sent them would be told so instead of quietly saving nothing.
	for _, key := range []string{"mail.host", "mail.port", "mail.from"} {
		w := s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{key: "x"}}, s.admin)
		if w.Code == http.StatusOK {
			t.Errorf("the old key %s was still accepted", key)
		}
	}
	// Upper-case values were the old spelling; the standard's are lower-case.
	w = s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{"settings": map[string]string{"mail.security": "NONE"}}, s.admin)
	if w.Code == http.StatusOK {
		t.Error("the old upper-case security value was still accepted")
	}

	settings, err := s.app.loadMailSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Host != host || settings.Port != port || settings.Security != "AUTO" || settings.From != "weekly@internal.test" || settings.Password != "hunter2" {
		t.Errorf("loaded %+v, want the values saved under the standard's names", settings)
	}
	if reason := settings.unusable(); reason != "" {
		t.Errorf("a relay configured through the standard's names was unusable: %s", reason)
	}

	// What the settings API returns never includes the password.
	w = s.request(http.MethodGet, "/api/v1/admin/settings", nil, s.admin)
	if w.Code != http.StatusOK {
		t.Fatalf("read settings: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Error("the SMTP password came back from the settings API")
	}
	var envelope struct {
		Data []struct {
			Key        string `json:"key"`
			Value      string `json:"value"`
			Secret     bool   `json:"secret"`
			Configured bool   `json:"configured"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range envelope.Data {
		seen[item.Key] = true
		if item.Key == "mail.password" && (!item.Secret || !item.Configured || item.Value != "") {
			t.Errorf("mail.password came back as %+v, want secret · configured · empty", item)
		}
	}
	for _, key := range []string{"mail.enabled", "mail.smtp_host", "mail.smtp_port", "mail.security", "mail.skip_tls_verify",
		"mail.username", "mail.password", "mail.from_address", "mail.from_name", "mail.timeout_seconds"} {
		if !seen[key] {
			t.Errorf("the settings API does not list %s", key)
		}
	}
}
