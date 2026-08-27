package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Measured on a deployment: with ai.timeout_seconds at its default 90 and an
// embedding gateway that accepted the connection and then said nothing, a
// search answered in 90.7 seconds. The meaning pass is an extra — it runs only
// when the cheaper passes came back thin — so the searcher waited a minute and
// a half for hits they were never going to get.

// guards: searchSemantic
func TestASlowEmbeddingGatewayDoesNotHoldTheSearchBox(t *testing.T) {
	server := newTestServer(t)
	if !server.app.capabilities.Vector {
		t.Skip("pgvector 가 없는 데이터베이스입니다")
	}
	author := server.createUser("search_budget", "USER", nil)

	// Accepts the connection and never answers, which is what a wedged relay
	// or a proxy holding the socket open looks like.
	held := make(chan struct{})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-held:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	// Order matters: the handler has to be released before the server is
	// closed, or Close waits on the connection this test deliberately wedged.
	defer gateway.Close()
	defer close(held)

	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.embedding_enabled": "true", "ai.embedding_endpoint": gateway.URL,
			"ai.embedding_model": "budget-1", "ai.timeout_seconds": "90",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("임베딩을 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	started := time.Now()
	// A query nothing matches, so the cheap passes come back thin and the
	// meaning pass is the one that decides how long this takes.
	answer := server.request(http.MethodGet, "/api/v1/search?q=%EC%9E%88%EC%9D%84%20%EB%A6%AC%20%EC%97%86%EB%8A%94%20%EB%A7%90", nil, author)
	took := time.Since(started)

	if answer.Code != http.StatusOK {
		t.Fatalf("검색이 %d 로 끝났습니다: %s", answer.Code, answer.Body.String())
	}
	// The budget plus room for the rest of the search on a loaded machine. The
	// number that matters is that this is nowhere near ai.timeout_seconds.
	t.Logf("검색 응답 %s (의미 단계 예산 %s, AI 제한시간 90s)", took.Round(time.Millisecond), searchSemanticBudget)
	if limit := searchSemanticBudget + 20*time.Second; took > limit {
		t.Errorf("검색이 %s 걸렸습니다. 의미 단계 예산은 %s 인데 AI 제한시간을 그대로 쓴 것입니다", took, searchSemanticBudget)
	}
}
