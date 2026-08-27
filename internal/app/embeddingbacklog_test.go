package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Measured on a seeded deployment: 110,534 items, one batch of 32 embedded
// every two minutes, and the gateway round trip for a batch was 9 ms. The tick
// was idle for the other 119.99 seconds, so a first pass would have taken 3,455
// ticks — four days and nineteen hours — during which semantic search answers
// from the fraction that happens to be embedded and says nothing about the rest.

// guards: drainEmbeddings, embedPending
func TestTheEmbeddingWorkerFinishesTheBacklogRatherThanOneBatchOfIt(t *testing.T) {
	server := newTestServer(t)
	if !server.app.capabilities.Vector {
		t.Skip("pgvector 가 없는 데이터베이스입니다")
	}
	author := server.createUser("embed_backlog", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "임베딩 적재 확인")

	// More than two batches, so finishing cannot be mistaken for one round.
	const items = embeddingBatchSize*2 + 5
	for index := 0; index < items; index++ {
		if _, err := server.app.db.Exec(server.ctx(),
			`INSERT INTO report_items(report_id, category, title, current_result, next_plan, issue, progress, sort_order)
				VALUES($1, '인프라', $2, $3, '잔여 지사 마무리', '', 50, $4)`,
			reportID, fmt.Sprintf("회선 이설 %d차", index), fmt.Sprintf("%d개 지사 완료", index), index); err != nil {
			t.Fatal(err)
		}
	}

	var calls, embedded atomic.Int64
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		calls.Add(1)
		embedded.Add(int64(len(request.Input)))
		vectors := make([]map[string]any, 0, len(request.Input))
		for index := range request.Input {
			// Distinct per input so nothing can pass by returning one vector.
			values := make([]float32, 8)
			values[index%8] = 1
			vectors = append(vectors, map[string]any{"index": index, "embedding": values})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": vectors})
	}))
	defer gateway.Close()

	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.embedding_enabled": "true", "ai.embedding_endpoint": gateway.URL,
			"ai.embedding_model": "backlog-1",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("임베딩을 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	server.app.drainEmbeddings(server.ctx())

	if calls.Load() < 2 {
		t.Errorf("게이트웨이를 %d번만 불렀습니다. 한 묶음으로 끝냈다는 뜻입니다", calls.Load())
	}
	var pending int
	if err := server.app.db.QueryRow(server.ctx(), `SELECT count(*) FROM report_items i
		LEFT JOIN report_item_embeddings e ON e.report_item_id = i.id AND e.model = 'backlog-1'
		WHERE i.report_id = $1 AND e.report_item_id IS NULL`, reportID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Errorf("한 번 돌고 나서도 %d건이 임베딩되지 않은 채 남았습니다 (호출 %d회, 보낸 항목 %d건)",
			pending, calls.Load(), embedded.Load())
	}
}

// The rebuild button embedded 6,400 items on a corpus of 110,534, logged
// "embedding rebuild complete" and told the operator it had generated 6,400 —
// with 104,134 missing and nothing on screen saying so. A request has to end,
// so the cap stays; what it leaves behind has to be in the answer.

// guards: rebuildEmbeddings, pendingEmbeddingCount
func TestTheRebuildSaysHowMuchIsStillMissing(t *testing.T) {
	server := newTestServer(t)
	if !server.app.capabilities.Vector {
		t.Skip("pgvector 가 없는 데이터베이스입니다")
	}
	author := server.createUser("embed_rebuild", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "다시 만들기 확인")
	const items = embeddingBatchSize + 8
	for index := 0; index < items; index++ {
		if _, err := server.app.db.Exec(server.ctx(),
			`INSERT INTO report_items(report_id, category, title, current_result, next_plan, issue, progress, sort_order)
				VALUES($1, '인프라', $2, '진행 중', '', '', 10, $3)`,
			reportID, fmt.Sprintf("다시 만들기 대상 %d", index), index); err != nil {
			t.Fatal(err)
		}
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		vectors := make([]map[string]any, 0, len(request.Input))
		for index := range request.Input {
			values := make([]float32, 8)
			values[index%8] = 1
			vectors = append(vectors, map[string]any{"index": index, "embedding": values})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": vectors})
	}))
	defer gateway.Close()
	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.embedding_enabled": "true", "ai.embedding_endpoint": gateway.URL,
			"ai.embedding_model": "rebuild-1",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("임베딩을 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	answer := server.request(http.MethodPost, "/api/v1/admin/embeddings/rebuild", map[string]any{}, server.admin)
	if answer.Code != http.StatusOK {
		t.Fatalf("다시 만들기: %d %s", answer.Code, answer.Body.String())
	}
	var envelope struct {
		Data struct {
			Embedded  int `json:"embedded"`
			Remaining int `json:"remaining"`
		} `json:"data"`
	}
	if err := json.Unmarshal(answer.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Embedded < items {
		t.Errorf("%d건을 만들었다는데 대상은 %d건입니다", envelope.Data.Embedded, items)
	}
	if envelope.Data.Remaining != 0 {
		t.Errorf("전부 처리했는데 %d건이 남았다고 합니다", envelope.Data.Remaining)
	}

	// Editing an item makes its stored vector answer for wording that is gone,
	// which is the other half of what the count has to see.
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE report_items SET current_result = '내용이 바뀌었습니다' WHERE report_id = $1 AND sort_order = 0`, reportID); err != nil {
		t.Fatal(err)
	}
	if remaining := server.app.pendingEmbeddingCount(server.ctx(), "rebuild-1"); remaining != 1 {
		t.Errorf("본문이 바뀐 항목 하나가 남아야 하는데 %d건이라고 합니다", remaining)
	}

	second := server.request(http.MethodPost, "/api/v1/admin/embeddings/rebuild", map[string]any{}, server.admin)
	if second.Code != http.StatusOK {
		t.Fatalf("두 번째 다시 만들기: %d %s", second.Code, second.Body.String())
	}
	if err := json.Unmarshal(second.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Embedded != 1 || envelope.Data.Remaining != 0 {
		t.Errorf("바뀐 한 건만 다시 만들어야 하는데 %d건 생성·%d건 잔여입니다", envelope.Data.Embedded, envelope.Data.Remaining)
	}
}
