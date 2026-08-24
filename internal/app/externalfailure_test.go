package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Different failures must read differently, or the message is not information.
//
// One sentence used to cover all of them — "유효한 구조화 결과를 반환하지
// 못했습니다. 관리자 설정과 모델 지원 여부를 확인하세요" — which is right for a
// model that cannot produce structured output and wrong for every other case.
// A rejected API key, a gateway that is down, and a proxy answering with its own
// sign-in page all sent the administrator to check the model.
func TestAIMessageNamesWhichFailureItWas(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		says    string
		notSays string
	}{
		{"인증 거부", &aiStatusError{Status: http.StatusUnauthorized}, "API Key", "Structured Output"},
		{"권한 없음", &aiStatusError{Status: http.StatusForbidden}, "API Key", "Structured Output"},
		{"경로 없음", &aiStatusError{Status: http.StatusNotFound}, "chat/completions", "API Key"},
		{"한도 초과", &aiStatusError{Status: http.StatusTooManyRequests}, "429", "API Key"},
		{"게이트웨이 오류", &aiStatusError{Status: http.StatusBadGateway}, "게이트웨이 쪽 문제", "API Key"},
		{"JSON 아님", fmt.Errorf("wrapped: %w", errAINotJSON), "프록시", "Structured Output"},
		{"주소 없음", errors.New(`Post "http://x/v1": dial tcp: no such host`), "연결하지 못했습니다", "API Key"},
		{"시간 초과", context.DeadlineExceeded, "시간이 초과", "API Key"},
		{"구조화 실패", errors.New("AI structured output is invalid"), "Structured Output", "API Key"},
	}
	seen := map[string]string{}
	for _, item := range cases {
		message := aiUserMessage(item.err)
		if !strings.Contains(message, item.says) {
			t.Errorf("%s: %q does not mention %q", item.name, message, item.says)
		}
		if item.notSays != "" && strings.Contains(message, item.notSays) {
			t.Errorf("%s: %q sends the reader after %q instead", item.name, message, item.notSays)
		}
		if previous, repeated := seen[message]; repeated {
			t.Errorf("%s and %s share one message, so it tells the reader nothing: %q", item.name, previous, message)
		}
		seen[message] = item.name
	}
}

// The settings screen must not print internal URLs or Go decoder errors. A
// wrong Base URL used to answer with the whole REST query string, and a proxy
// in front of Confluence with `invalid character '<' looking for beginning of
// value`.
func TestConfluenceMessageIsActionableAndLeaksNothing(t *testing.T) {
	cases := []struct {
		name string
		err  error
		says string
	}{
		{"인증 거부", &ConfluenceHTTPError{StatusCode: http.StatusUnauthorized}, "아이디와 비밀번호"},
		{"권한 없음", &ConfluenceHTTPError{StatusCode: http.StatusForbidden}, "Space"},
		{"경로 없음", &ConfluenceHTTPError{StatusCode: http.StatusNotFound}, "Base URL"},
		{"서버 오류", &ConfluenceHTTPError{StatusCode: http.StatusInternalServerError}, "Confluence 쪽 문제"},
		{"JSON 아님", errors.New("decode Confluence response: invalid character '<' looking for beginning of value"), "프록시"},
		{"주소 없음", errors.New(`Get "http://host/confluence/rest/api/content/search?cql=type+%3D+page": dial tcp: no such host`), "연결하지 못했습니다"},
		{"인증서", errors.New("x509: certificate signed by unknown authority"), "인증서"},
	}
	for _, item := range cases {
		message := safeConfluenceError(item.err)
		if !strings.Contains(message, item.says) {
			t.Errorf("%s: %q does not mention %q", item.name, message, item.says)
		}
		for _, leaked := range []string{"rest/api", "cql=", "invalid character", "x509:", "dial tcp"} {
			if strings.Contains(message, leaked) {
				t.Errorf("%s: the settings screen would show %q — %q", item.name, leaked, message)
			}
		}
	}
	if safeConfluenceError(nil) != "" {
		t.Error("no error should produce no message")
	}

	// The branches above name the failures worth naming. Everything else has to
	// fall back to something safe rather than to the raw error — an unfamiliar
	// failure is exactly the one whose text nobody has read before, and the
	// settings screen is not where an internal address should first appear.
	unfamiliar := errors.New(`unexpected EOF from http://confluence.internal:8090/rest/api/content?cql=secret`)
	message := safeConfluenceError(unfamiliar)
	if strings.Contains(message, "confluence.internal") || strings.Contains(message, "cql=") {
		t.Errorf("an unrecognised error was printed verbatim: %q", message)
	}
	if message == "" {
		t.Error("an unrecognised failure still has to say something")
	}
}
