package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
)

// The embedding card is how an operator decides whether semantic search can
// answer at all. A failed count and an empty corpus both leave the three
// figures at zero, and zero reads as "nothing to embed" — work already done.
// So a database that refuses the count sends the operator away satisfied, or
// sends them to press 다시 만들기 against a corpus they cannot see.
//
// guards: embeddingStatus
func TestAnUnreadEmbeddingCountIsNotAnEmptyCorpus(t *testing.T) {
	server := newTestServer(t)
	if !server.app.capabilities.Vector {
		t.Skip("pgvector 가 없는 데이터베이스입니다 — 이 시험은 벡터 지원 DB 에서만 의미가 있습니다")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the status card asked the gateway for an embedding")
	}))
	defer gateway.Close()
	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.embedding_enabled": "true", "ai.embedding_endpoint": gateway.URL,
			"ai.embedding_model": "status-1",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("임베딩을 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	// What the card looks like when the count does run. The failing answer has
	// to be a superset of this: adding a flag, never renaming or dropping a
	// figure an existing client already reads.
	healthy := readEmbeddingCard(t, server)
	if _, ok := healthy["countsUnread"]; ok {
		t.Error("a count that was read reports itself as unread")
	}
	if names := keysOf(healthy); fmt.Sprint(names) != "[embedded enabled items model stale vectorAvailable]" {
		t.Errorf("the healthy card carries %v; an existing client reads these names", names)
	}

	// The failure that reaches this path in the field is a count the database
	// refuses. Renaming the table it reads reproduces that exactly.
	if _, err := server.app.db.Exec(server.ctx(),
		`ALTER TABLE report_item_embeddings RENAME TO report_item_embeddings_hidden`); err != nil {
		t.Fatalf("hide the table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = server.app.db.Exec(context.Background(),
			`ALTER TABLE report_item_embeddings_hidden RENAME TO report_item_embeddings`)
	})

	card := readEmbeddingCard(t, server)
	if unread, _ := card["countsUnread"].(bool); !unread {
		t.Errorf("the counts could not be read and the card does not say so: %v", card)
	}
	// Losing the whole card would make an unreadable count look like a
	// deployment with no pgvector, which is a different thing to go and fix.
	if available, _ := card["vectorAvailable"].(bool); !available {
		t.Error("an unreadable count took pgvector availability with it")
	}
	if enabled, _ := card["enabled"].(bool); !enabled {
		t.Error("an unreadable count took the enabled flag with it")
	}
	if model, _ := card["model"].(string); model != "status-1" {
		t.Errorf("an unreadable count took the model name with it: %q", model)
	}
}

// readEmbeddingCard reads the admin card through the production handler and
// answers with the fields as they arrive on the wire, so a missing name and a
// zero are not the same thing here either.
func readEmbeddingCard(t *testing.T, server *testServer) map[string]any {
	t.Helper()
	response := server.request(http.MethodGet, "/api/v1/admin/embeddings", nil, server.admin)
	if response.Code != http.StatusOK {
		t.Fatalf("read the embedding card: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the embedding card: %v", err)
	}
	return body.Data
}

func keysOf(fields map[string]any) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// 다시 만들기 reports what it left behind, because a request has to end and the
// pass is capped. That number comes from a count of its own, run after the
// batches: if the count fails the handler read zero and said "남은 항목이
// 없습니다" over a backlog it never looked at — the one sentence that stops an
// operator from pressing the button again.
//
// guards: rebuildEmbeddings, pendingEmbeddingCount
func TestTheRebuildDoesNotCallAnUnreadBacklogEmpty(t *testing.T) {
	server := newTestServer(t)
	if !server.app.capabilities.Vector {
		t.Skip("pgvector 가 없는 데이터베이스입니다 — 이 시험은 벡터 지원 DB 에서만 의미가 있습니다")
	}
	author := server.createUser("embed_unread", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "남은 건수 확인")
	// One batch is enough: the pass embeds them all and then runs the count,
	// which is the step under test.
	const items = 4
	for index := 0; index < items; index++ {
		if _, err := server.app.db.Exec(server.ctx(),
			`INSERT INTO report_items(report_id, category, title, current_result, next_plan, issue, progress, sort_order)
				VALUES($1, '인프라', $2, '진행 중', '', '', 10, $3)`,
			reportID, fmt.Sprintf("남은 건수 대상 %d", index), index); err != nil {
			t.Fatal(err)
		}
	}

	// The batches have to succeed and the count after them has to fail, which
	// is the shape of the real failure: a transient refusal on the one query
	// run last. Renaming report_items while a batch is at the gateway gets
	// there exactly — the rows are already selected, the embedding INSERTs
	// name only report_item_embeddings, and the count is the next read.
	var once sync.Once
	var hideErr error
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		once.Do(func() {
			if _, err := server.app.db.Exec(r.Context(),
				`ALTER TABLE report_items RENAME TO report_items_hidden`); err != nil {
				hideErr = err
				return
			}
			// A real backlog behind the unreadable count, so "0건 남음" is a
			// statement about rows nobody looked at rather than a coincidence.
			if _, err := server.app.db.Exec(r.Context(),
				`INSERT INTO report_items_hidden(report_id, category, title, current_result, next_plan, issue, progress, sort_order)
					VALUES($1, '인프라', '집계 뒤에 남은 항목', '진행 중', '', '', 10, 99)`, reportID); err != nil {
				hideErr = err
			}
		})
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
	t.Cleanup(func() {
		_, _ = server.app.db.Exec(context.Background(),
			`ALTER TABLE report_items_hidden RENAME TO report_items`)
	})

	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.embedding_enabled": "true", "ai.embedding_endpoint": gateway.URL,
			"ai.embedding_model": "unread-1",
		},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("임베딩을 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	answer := server.request(http.MethodPost, "/api/v1/admin/embeddings/rebuild", map[string]any{}, server.admin)
	if hideErr != nil {
		t.Fatalf("hide the table mid-pass: %v", hideErr)
	}
	if answer.Code != http.StatusOK {
		t.Fatalf("다시 만들기: %d %s", answer.Code, answer.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(answer.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if embedded, _ := body.Data["embedded"].(float64); int(embedded) != items {
		t.Fatalf("the pass embedded %v of %d items, so the count is not the step that failed: %v",
			body.Data["embedded"], items, body.Data)
	}
	if remaining, ok := body.Data["remaining"]; ok {
		t.Errorf("the backlog count failed and the answer reports remaining=%v as a fact: %v", remaining, body.Data)
	}
	if unread, _ := body.Data["remainingUnread"].(bool); !unread {
		t.Errorf("the backlog could not be counted and the answer does not say so: %v", body.Data)
	}

	// pendingEmbeddingCount is the figure's only source, and the rebuild is not
	// the only caller it will ever have: it has to hand the failure up rather
	// than answer zero.
	if _, err := server.app.pendingEmbeddingCount(server.ctx(), "unread-1"); err == nil {
		t.Error("the count could not run and reported no error")
	}
}
