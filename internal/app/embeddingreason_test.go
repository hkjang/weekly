package app

import (
	"strings"
	"testing"
)

// embeddingStatus hands this error's text to the screen as `reason`, and the
// 검색 설정 line prints it beside the counts. On a live deployment that read
// "pgvector 사용 가능 · 임베딩 0/110534 · embedding is disabled", and pressing
// 임베딩 다시 생성 answered "의미 검색이 활성화돼 있지 않습니다: embedding
// endpoint or model is not configured" — an internal English string in an
// otherwise Korean product, saying neither which setting was missing nor what
// to do about it.

// guards: embeddingConfig
func TestTheSearchSettingSaysWhichPieceIsMissingInTheReadersLanguage(t *testing.T) {
	server := newTestServer(t)

	reason := func() string {
		t.Helper()
		cfg, err := server.app.embeddingConfig(server.ctx())
		if err == nil {
			t.Fatalf("설정이 갖춰지지 않았는데 통과했습니다: %#v", cfg)
		}
		text := err.Error()
		for _, latin := range []string{"embedding is", "endpoint or model", "pgvector is", "not configured", "is disabled"} {
			if strings.Contains(text, latin) {
				t.Errorf("%q 가 화면에 나갈 문장에 그대로 있습니다: %q", latin, text)
			}
		}
		if !strings.ContainsAny(text, "가나다라마바사아자차카타파하") {
			t.Errorf("읽는 사람의 말이 아닙니다: %q", text)
		}
		return text
	}

	set := func(values map[string]string) {
		t.Helper()
		if w := server.request("PUT", "/api/v1/admin/settings", map[string]any{"settings": values}, server.admin); w.Code != 200 {
			t.Fatalf("설정: %d %s", w.Code, w.Body.String())
		}
	}

	off := reason()

	set(map[string]string{"ai.embedding_enabled": "true", "ai.embedding_endpoint": "", "ai.embedding_model": ""})
	both := reason()
	set(map[string]string{"ai.embedding_endpoint": "http://gateway.internal/v1/embeddings", "ai.embedding_model": ""})
	noModel := reason()
	set(map[string]string{"ai.embedding_endpoint": "", "ai.embedding_model": "embed-1"})
	noEndpoint := reason()

	// Four states, four sentences. One sentence for all of them is what sent an
	// administrator to check the setting that was already right.
	seen := map[string]bool{off: true, both: true, noModel: true, noEndpoint: true}
	if len(seen) != 4 {
		t.Errorf("네 가지 상태가 %d가지 문장으로 뭉갰습니다: %v", len(seen), seen)
	}
	if !strings.Contains(noEndpoint, "Endpoint") {
		t.Errorf("Endpoint 가 빈 경우인데 %q 라고 합니다", noEndpoint)
	}
	if !strings.Contains(noModel, "모델") {
		t.Errorf("모델이 빈 경우인데 %q 라고 합니다", noModel)
	}
}
