package app

import (
	"errors"
	"strings"
	"testing"
)

// Measured on a deployment: pointing Issuer URL at a closed port put "dial tcp
// 192.168.65.254:18899: connect: connection refused" on the administrator's
// screen, and pointing it at an address that answers 404 put the entire HTML
// error page into the notification. Three faults, one prefix, and the part that
// told them apart in English at the end.

// guards: oidcUserMessage
func TestOIDCDiscoverySaysWhichFaultItWasWithoutQuotingTheServer(t *testing.T) {
	page := "404 Not Found: <!DOCTYPE HTML>\n<html lang=\"en\">\n<head><title>Error response</title></head>\n<body><h1>Error response</h1></body></html>\n"
	cases := []struct{ name, raw, wants string }{
		{"연결 거부", `Get "http://sso.internal/realms/weekly/.well-known/openid-configuration": dial tcp 192.168.65.254:18899: connect: connection refused`, "연결하지 못했습니다"},
		{"이름 해석 실패", `Get "http://sso.internal/...": dial tcp: lookup sso.internal on 127.0.0.11:53: no such host`, "연결하지 못했습니다"},
		{"응답 없음", `Get "http://sso.internal/...": context deadline exceeded (Client.Timeout exceeded)`, "시간 안에 응답하지"},
		{"인증서", `Get "https://sso.internal/...": x509: certificate signed by unknown authority`, "인증서"},
		{"경로 없음", page, ".well-known/openid-configuration"},
		{"거부", "401 Unauthorized: no", "거부되었습니다"},
		{"제공자 오류", "503 Service Unavailable: down", "제공자 쪽 문제"},
		{"issuer 불일치", `oidc: issuer did not match the issuer returned by provider, expected "https://a" got "https://b"`, "issuer와 다릅니다"},
		{"JSON 아님", "oidc: failed to decode provider discovery object: invalid character '<' looking for beginning of value", "JSON이 아닌"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		got := oidcUserMessage(errors.New(tc.raw))
		if !strings.Contains(got, tc.wants) {
			t.Errorf("%s: %q 에 %q 가 없습니다", tc.name, got, tc.wants)
		}
		for _, leak := range []string{"dial tcp", "192.168.65.254", "127.0.0.11", "<html", "DOCTYPE", "x509:", "Client.Timeout"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: %q 가 %q 를 그대로 흘립니다", tc.name, got, leak)
			}
		}
		if len(got) > 200 {
			t.Errorf("%s: 알림에 넣기에 너무 긴 %d자입니다", tc.name, len([]rune(got)))
		}
		seen[got] = true
	}
	// Nine faults, and an administrator has to be able to tell them apart. The
	// old behaviour was one prefix for all of them.
	if len(seen) < 7 {
		t.Errorf("아홉 가지 고장이 %d가지 문장으로 뭉갰습니다", len(seen))
	}
	if got := oidcUserMessage(nil); got != "" {
		t.Errorf("오류가 없을 때 %q 를 말합니다", got)
	}
	if got := oidcUserMessage(errors.New("무언가 알 수 없는 실패")); !strings.Contains(got, "서버 로그") {
		t.Errorf("모르는 실패에 %q 라고 합니다", got)
	}
}
