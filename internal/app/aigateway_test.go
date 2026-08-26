package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A gateway that answers, so the code that reads its answer can be run.
//
// Everything between "the model replied" and "the screen shows it" was
// unreachable: the tests either left the feature off, or turned it on and
// pointed it at nothing. That covers refusing politely — it cannot cover
// filtering a blank candidate out of a reply, or stopping at the limit, or any
// of the handling that decides what a person ends up reading. Those branches
// could be inverted at will.
//
// The endpoint is used as the POST target verbatim, so a local server is enough.

// aiGateway starts a gateway that answers every call with content, and points
// the deployment's settings at it. content is what the model "returned": the
// JSON string the structured-output contract puts in the message.
func (s *testServer) aiGateway(t *testing.T, content any) *httptest.Server {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}},
		})
	}))
	t.Cleanup(gateway.Close)

	if on := s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": gateway.URL, "ai.model": "weekly-1",
		},
	}, s.admin); on.Code != http.StatusOK {
		t.Fatalf("AI 를 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}
	return gateway
}

func suggestFor(t *testing.T, server *testServer, cookie *http.Cookie, workItemID int64) []decisionCandidate {
	t.Helper()
	w := server.request(http.MethodPost,
		fmt.Sprintf("/api/v1/work-items/%d/decisions/suggest", workItemID), map[string]any{}, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("제안 요청: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Candidates []decisionCandidate `json:"candidates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data.Candidates
}

// guards: suggestDecisions
func TestABlankCandidateFromTheModelIsNotOfferedAsADecision(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("제안 조직", "AIGATE")
	owner := server.createUser("ai_gate", "USER", &organisation)

	server.aiGateway(t, map[string]any{"candidates": []any{
		map[string]any{"title": "  ", "decidedBy": "회의", "decidedOn": "2026-08-24"},
		map[string]any{"title": "자체 구현으로 가기로 했다", "decidedBy": "회의", "decidedOn": "2026-08-24"},
		map[string]any{"title": "", "decidedBy": "회의", "decidedOn": "2026-08-24"},
	}})
	server.weekWithIssue(owner, "2026-08-24", "결정이 필요한 업무", "외부 연동 지연", 40)
	workItemID := server.workItemNamed(server.lastCreatedUsername("ai_gate"), "결정이 필요한 업무")

	candidates := suggestFor(t, server, owner, workItemID)
	if len(candidates) != 1 {
		t.Fatalf("제목이 있는 후보 하나만 남아야 하는데 %d개입니다: %+v", len(candidates), candidates)
	}
	if candidates[0].Title != "자체 구현으로 가기로 했다" {
		t.Errorf("남은 후보가 %q 입니다", candidates[0].Title)
	}
}

// guards: suggestDecisions
func TestTheModelCannotPushMoreCandidatesThanTheScreenAccepts(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("제안 조직", "AIGATE2")
	owner := server.createUser("ai_gate_many", "USER", &organisation)

	// Twice the limit, all of them well formed. The schema asks for at most
	// five; a gateway is not obliged to honour what it was asked.
	many := []any{}
	for index := 0; index < decisionSuggestLimit*2; index++ {
		many = append(many, map[string]any{
			"title": fmt.Sprintf("결정 후보 %d", index+1), "decidedBy": "회의", "decidedOn": "2026-08-24",
		})
	}
	server.aiGateway(t, map[string]any{"candidates": many})
	server.weekWithIssue(owner, "2026-08-24", "후보가 쏟아지는 업무", "외부 연동 지연", 40)
	workItemID := server.workItemNamed(server.lastCreatedUsername("ai_gate_many"), "후보가 쏟아지는 업무")

	candidates := suggestFor(t, server, owner, workItemID)
	if len(candidates) != decisionSuggestLimit {
		t.Errorf("상한이 %d인데 %d개가 나왔습니다", decisionSuggestLimit, len(candidates))
	}
}

func parseWith(t *testing.T, server *testServer, cookie *http.Cookie, text string) *httptest.ResponseRecorder {
	t.Helper()
	return server.request(http.MethodPost, "/api/v1/ai/reports/parse-text",
		map[string]any{"text": text}, cookie)
}

// guards: parseAIText
//
// The model is asked for one fact per array element, and the contract is worth
// something only if a reply that ignores it is refused. A bullet list crammed
// into one element becomes one report line reading "- 첫째\n- 둘째", which is
// what the author then has to take apart by hand — the opposite of the help
// this feature is for.
func TestAReplyThatCramsAListIntoOneFactIsRefused(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("ai_atomic", "USER", nil)

	server.aiGateway(t, map[string]any{
		"summary": "지난주 요약", "weekStart": "2026-08-24", "dateConfidence": 0.9,
		"reportItems": []any{map[string]any{
			"category": "개발", "title": "큐 개선",
			"currentResults": []string{"- 첫째 일을 했습니다\n- 둘째 일도 했습니다"},
			"nextPlans":      []string{"다음 주에 부하를 겁니다"},
			"issues":         []string{},
			"progress":       50, "confidence": 0.8, "categoryConfidence": 0.8,
			"sourceSlides": []int{},
		}},
		"warnings": []string{},
	})

	w := parseWith(t, server, author, "지난주에 큐 개선을 진행했습니다.")
	if w.Code == http.StatusOK {
		t.Fatalf("한 칸에 목록을 담은 답이 그대로 통과했습니다: %s", w.Body.String())
	}
	if code := errorCode(w); code != "AI_ANALYSIS_FAILED" {
		t.Errorf("코드가 %q 입니다: %s", code, w.Body.String())
	}
}

// guards: parseAIText
//
// The rule has two halves — one fact, and no list marker — and the case above
// breaks both at once, so it cannot tell whether the handler needs both or
// either. These two break one each. A single bulleted fact is still a bullet
// that lands in the report; two facts split by a newline are still two facts in
// one box.
func TestEachHalfOfTheOneFactRuleIsEnforcedOnItsOwn(t *testing.T) {
	for _, broken := range []struct {
		name  string
		value string
	}{
		{"글머리표 하나에 사실 하나", "- 큐를 둘로 나눴습니다"},
		{"글머리표 없이 사실 둘", "큐를 둘로 나눴습니다\n부하도 걸었습니다"},
	} {
		t.Run(broken.name, func(t *testing.T) {
			server := newTestServer(t)
			author := server.createUser("ai_half", "USER", nil)
			server.aiGateway(t, map[string]any{
				"summary": "지난주 요약", "weekStart": "2026-08-24", "dateConfidence": 0.9,
				"reportItems": []any{map[string]any{
					"category": "개발", "title": "큐 개선",
					"currentResults": []string{broken.value},
					"nextPlans":      []string{"다음 주에 부하를 겁니다"},
					"issues":         []string{},
					"progress":       50, "confidence": 0.8, "categoryConfidence": 0.8,
					"sourceSlides": []int{},
				}},
				"warnings": []string{},
			})
			if w := parseWith(t, server, author, "지난주에 큐 개선을 진행했습니다."); w.Code == http.StatusOK {
				t.Errorf("%s: 그대로 통과했습니다: %s", broken.name, w.Body.String())
			}
		})
	}
}

// guards: parseAIText
//
// And the other half: a reply that keeps the contract has to come back whole.
// Checking only the refusal would let the validation reject everything and
// still look correct.
func TestAWellFormedReplyBecomesTheReportItWasAskedFor(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("ai_ok", "USER", nil)

	server.aiGateway(t, map[string]any{
		"summary": "지난주 요약", "weekStart": "2026-08-24", "dateConfidence": 0.9,
		"reportItems": []any{map[string]any{
			"category": "개발", "title": "큐 개선",
			"currentResults": []string{"큐를 둘로 나눴습니다"},
			"nextPlans":      []string{"다음 주에 부하를 겁니다"},
			"issues":         []string{"외부 연동이 늦습니다"},
			"progress":       50, "confidence": 0.8, "categoryConfidence": 0.8,
			"sourceSlides": []int{},
		}},
		"warnings": []string{},
	})

	w := parseWith(t, server, author, "지난주에 큐를 둘로 나눴습니다.")
	if w.Code != http.StatusOK {
		t.Fatalf("규약을 지킨 답이 거부됐습니다: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, expected := range []string{"큐 개선", "큐를 둘로 나눴습니다", "다음 주에 부하를 겁니다", "외부 연동이 늦습니다"} {
		if !containsAny(body, expected) {
			t.Errorf("모델이 답한 %q 가 결과에 없습니다: %s", expected, body)
		}
	}
}
