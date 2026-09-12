package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 업무 검색이 무엇을 먼저 보여 주는가.
//
// The list is capped at 25, so the order is not presentation — it decides which
// tasks the reader is shown at all. Nothing checked it: a mutation reversing
// both comparisons in searchWorkItems passed every test in the suite, which
// means the screen could have been ranking the least relevant and oldest work
// first and the suite would have agreed.
//
// The rule the code states is: the strongest literal evidence first, and among
// equals the most recent, because a reader searching their own history is
// looking for what is still live.
//
// guards: searchWorkItems
func TestWorkSearchShowsTheStrongestMatchFirstAndThenTheMostRecent(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("worksort", "USER", nil)

	// One word, three tasks. Only the first carries it in its title, which is
	// worth workSearchTitleBonus on top of the term itself.
	write := func(week, title, result string) {
		id, version := server.draft(author, week, week+" 주간보고")
		response := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
			"summary": week + " 주간보고", "version": version,
			"items": []map[string]any{{
				"category": "개발", "title": title, "currentResult": result,
				"nextPlan": "계속", "progress": 50,
			}},
		}, author)
		if response.Code != http.StatusOK {
			t.Fatalf("write %s: %d %s", week, response.Code, response.Body.String())
		}
	}
	write("2026-02-02", "인증 연동 개편", "설계를 마쳤습니다")
	write("2026-02-09", "결산 자동화", "인증 연동 쪽 담당자와 협의했습니다")
	write("2026-02-16", "배치 정리", "인증 연동 문서를 읽었습니다")

	response := server.request(http.MethodGet, "/api/v1/work-items/search?q=인증", nil, author)
	if response.Code != http.StatusOK {
		t.Fatalf("work search: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Hits []struct {
				Title    string `json:"title"`
				Score    int    `json:"score"`
				LastWeek string `json:"lastWeek"`
			} `json:"hits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	hits := payload.Data.Hits
	if len(hits) != 3 {
		t.Fatalf("the word is in three tasks and the search returned %d: %+v", len(hits), hits)
	}
	// The title match outranks the two body matches, whatever their dates.
	if hits[0].Title != "인증 연동 개편" {
		t.Errorf("the task whose title is the query is not first: %+v", hits)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("a title match did not outscore a body match: %+v", hits)
	}
	// Between the two equal ones, the newer week first.
	if hits[1].Score != hits[2].Score {
		t.Fatalf("the two body matches were expected to score the same: %+v", hits)
	}
	if hits[1].LastWeek < hits[2].LastWeek {
		t.Errorf("equal matches are ordered oldest first: %+v", hits)
	}
}

// "비슷한 과거 업무를 찾지 못했습니다" is the same sentence whether the search
// looked and found nothing, could not look because the extension is missing, or
// could not look because nobody configured the model — and only the last two
// are something an administrator can act on. The code says so; nothing checked
// that the right sentence came out.
//
// guards: searchWorkItems
func TestWorkSearchSaysWhichPieceOfTheMeaningSearchIsMissing(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("worksemantic", "USER", nil)
	server.submitted(author, "2026-02-02", "의미 검색 사유")

	reason := func() string {
		response := server.request(http.MethodGet, "/api/v1/work-items/search?q=의미", nil, author)
		if response.Code != http.StatusOK {
			t.Fatalf("work search: %d %s", response.Code, response.Body.String())
		}
		var payload struct {
			Data struct {
				SemanticReason string `json:"semanticReason"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v", response.Body.String(), err)
		}
		return payload.Data.SemanticReason
	}

	capabilities := server.app.capabilities
	t.Cleanup(func() { server.app.capabilities = capabilities })

	// No extension: the answer names the extension, and the pass is not even
	// attempted — an attempt would report its own failure instead, which reads
	// as a broken service rather than an absent one.
	server.app.capabilities = databaseCapabilities{}
	if text := reason(); !strings.Contains(text, "pgvector") {
		t.Errorf("with no pgvector the reason is %q", text)
	}

	// Extension present, model unconfigured: the answer names the setting, and
	// says it is something that can be turned on.
	//
	// Claimed rather than detected, so this half runs on a database without the
	// extension too. It is a test of which sentence the code chooses, not of
	// pgvector — and gated on the real capability it silently did nothing on
	// CI's plain PostgreSQL, where the mutation that collapses the two
	// conditions into one survived while passing locally.
	server.app.capabilities = databaseCapabilities{Vector: true}
	text := reason()
	if !strings.Contains(text, "임베딩") {
		t.Errorf("with pgvector present and no embedding configured the reason is %q", text)
	}
	if strings.Contains(text, "pgvector") {
		t.Errorf("an installed extension was reported as missing: %q", text)
	}
}
