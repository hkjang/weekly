package app

import (
	"errors"
	"strings"
	"testing"
)

// Measured on a deployment with the relay unreachable: the reason on 개인 설정
// read "연결할 수 없습니다: dial tcp 10.20.0.25:25: connect: connection refused"
// and "…lookup smtp.internal.example on 127.0.0.11:53: no such host" — the
// relay address, its port and the container's resolver, on the screen of 61
// ordinary writers. The seed carried the same shape, which is how it survived.

// guards: mailUserMessage
func TestTheWriterIsToldWhatToDoAndNotWhereTheRelayLives(t *testing.T) {
	// The strings sendMail actually produces, relay addresses and all.
	// The relay's own words stay: a writer whose address is wrong learns it
	// from them. Only the failures that never reached a reply are replaced.
	kept := []string{
		"받는 주소를 릴레이가 거부했습니다: 550 5.1.1 mailbox unavailable",
		"보내는 주소를 릴레이가 거부했습니다: 553 5.7.1 sender denied",
		"릴레이가 본문을 받지 않았습니다: 451 4.3.0 temporary local problem",
		"받는 주소 형식이 올바르지 않습니다.",
	}
	for _, raw := range kept {
		if got := mailUserMessage(errors.New(raw)); got != raw {
			t.Errorf("릴레이가 한 말이 지워졌습니다: %q → %q", raw, got)
		}
	}

	// These never got a reply. What they carry is our own network.
	replaced := []struct{ name, raw, wants string }{
		{"주소 없음", "연결할 수 없습니다: dial tcp: lookup smtp.internal.example on 127.0.0.11:53: no such host", "잠시 뒤 다시 시도합니다"},
		{"연결 거부", "연결할 수 없습니다: dial tcp 10.20.0.25:25: connect: connection refused", "잠시 뒤 다시 시도합니다"},
		{"응답 없음", "연결할 수 없습니다: dial tcp 10.20.0.25:25: i/o timeout", "잠시 뒤 다시 시도합니다"},
		{"인증서", "STARTTLS에 실패했습니다: x509: certificate signed by unknown authority", "관리자에게 알려 주세요"},
		{"인증 거부", "계정 인증에 실패했습니다: 535 5.7.8 authentication failed", "관리자에게 알려 주세요"},
	}
	seen := map[string]bool{}
	for _, tc := range replaced {
		got := mailUserMessage(errors.New(tc.raw))
		if !strings.Contains(got, tc.wants) {
			t.Errorf("%s: %q 에 %q 가 없습니다", tc.name, got, tc.wants)
		}
		for _, leak := range []string{"10.20.0.25", "smtp.internal.example", "127.0.0.11", "dial tcp", "x509"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: %q 가 %q 를 그대로 흘립니다", tc.name, got, leak)
			}
		}
		seen[got] = true
	}
	if len(seen) < 3 {
		t.Errorf("다섯 가지 고장이 %d가지 문장으로 뭉갰습니다: %v", len(seen), seen)
	}
	if got := mailUserMessage(nil); got != "" {
		t.Errorf("오류가 없을 때 %q 를 말합니다", got)
	}
	// Anything else is left alone. sendMail's other returns are already
	// sentences written for a person — "받는 주소 형식이 올바르지 않습니다." and
	// what settings.unusable() says — and rewriting those would lose meaning
	// to gain nothing.
	if got := mailUserMessage(errors.New("메일 설정이 완료되지 않았습니다.")); got != "메일 설정이 완료되지 않았습니다." {
		t.Errorf("이미 사람이 읽을 문장인데 %q 로 바뀌었습니다", got)
	}
}
