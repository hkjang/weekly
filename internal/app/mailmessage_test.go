package app

import (
	"errors"
	"net/mail"
	"strings"
	"testing"
	"time"
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

// RFC 5322 §3.6 names Date and From as the two required header fields, and
// leaves Message-ID as SHOULD. A submission server fills in a missing one;
// this product relays to an internal MTA on port 25, which does not.
//
// Captured off the wire from a running deployment before this test existed, the
// message carried From, To, Subject, MIME-Version, Content-Type,
// Content-Transfer-Encoding and Auto-Submitted — and neither of these. A
// reader's client had no send time to show or sort by, and no key to thread or
// de-duplicate on.
//
// guards: buildMailMessage, mailMessageID
func TestTheMessageCarriesTheHeadersNothingDownstreamWillAdd(t *testing.T) {
	raw := string(buildMailMessage("weekly@internal.test", "주간보고",
		"reader@internal.test", "2026-08-24 주간보고", "본문입니다.\n"))
	parsed, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("만든 메시지를 파서가 읽지 못합니다: %v\n%s", err, raw)
	}
	sent, err := parsed.Header.Date()
	if err != nil {
		t.Fatalf("Date 헤더를 날짜로 읽을 수 없습니다: %v (%q)", err, parsed.Header.Get("Date"))
	}
	if elapsed := time.Since(sent); elapsed < -time.Minute || elapsed > time.Hour {
		t.Errorf("Date 가 지금과 %v 떨어져 있습니다: %q", elapsed, parsed.Header.Get("Date"))
	}
	id := parsed.Header.Get("Message-ID")
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") || !strings.Contains(id, "@internal.test") {
		t.Errorf("Message-ID 가 <무엇@보내는도메인> 모양이 아닙니다: %q", id)
	}
	// Two messages are two messages. A receiver that de-duplicates on this
	// header would drop the retry of a report that failed the first time.
	other := string(buildMailMessage("weekly@internal.test", "주간보고",
		"reader@internal.test", "2026-08-24 주간보고", "본문입니다.\n"))
	secondParsed, err := mail.ReadMessage(strings.NewReader(other))
	if err != nil {
		t.Fatalf("두 번째 메시지를 파서가 읽지 못합니다: %v", err)
	}
	if second := secondParsed.Header.Get("Message-ID"); second == id {
		t.Errorf("두 메시지가 같은 Message-ID 를 씁니다: %q", id)
	}
	// A From with no domain still has to produce a legal header rather than
	// "<...@>", which is what a naive split gives.
	odd := string(buildMailMessage("weekly", "", "reader@internal.test", "제목", "본문"))
	oddParsed, err := mail.ReadMessage(strings.NewReader(odd))
	if err != nil {
		t.Fatalf("도메인 없는 보내는 주소로 만든 메시지를 파서가 읽지 못합니다: %v", err)
	}
	if id := oddParsed.Header.Get("Message-ID"); !strings.Contains(id, "@") || strings.HasSuffix(id, "@>") {
		t.Errorf("도메인 없는 보내는 주소에서 Message-ID 가 %q 입니다", id)
	}
}
