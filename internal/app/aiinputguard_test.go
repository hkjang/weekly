package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestWithNoAIGatewayEveryAIPathSaysSoRatherThanFailing checks that every AI
// path answers politely while the gateway is off. That is the door. Behind it,
// nothing in these handlers had ever run: with the feature disabled the body
// is unreachable, so the input checks that stand between a user and the gateway
// could all be inverted and the suite stayed green.
//
// The switch can be turned on without a gateway behind it. The endpoint below
// refuses connections immediately, which is enough to reach — and stop at —
// everything this file is about.

func (s *testServer) enableAIWithoutAGateway(t *testing.T) {
	t.Helper()
	if on := s.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{
			"ai.enabled": "true", "ai.endpoint": "http://127.0.0.1:1/v1", "ai.model": "weekly-1",
		},
	}, s.admin); on.Code != http.StatusOK {
		t.Fatalf("AI 를 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}
}

// guards: parseAIText
func TestTheTextToParseIsCheckedBeforeItIsSentAnywhere(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("ai_input", "USER", nil)
	server.enableAIWithoutAGateway(t)

	for _, refusal := range []struct {
		name string
		text string
	}{
		{"빈 글", ""},
		{"공백뿐인 글", "   \n\t  "},
		{"5만 자를 넘는 글", strings.Repeat("가", 50001)},
	} {
		w := server.request(http.MethodPost, "/api/v1/ai/reports/parse-text",
			map[string]any{"text": refusal.text}, author)
		if w.Code != http.StatusBadRequest || errorCode(w) != "INVALID_AI_TEXT" {
			t.Errorf("%s: %d %s", refusal.name, w.Code, w.Body.String())
		}
		// The refusal has to say what the limit is, because the person cannot
		// see it from the screen.
		if !strings.Contains(w.Body.String(), "50000") {
			t.Errorf("%s: 한도를 말하지 않습니다: %s", refusal.name, w.Body.String())
		}
	}

	// Text within the limit is not refused here — it goes on to the gateway,
	// which is not there, and that is a different answer with a different code.
	// Without this the length check could refuse everything and still look
	// like it was working.
	w := server.request(http.MethodPost, "/api/v1/ai/reports/parse-text",
		map[string]any{"text": "지난주에 큐 개선을 진행했습니다."}, author)
	if w.Code == http.StatusBadRequest {
		t.Errorf("한도 안의 글이 거부됐습니다: %s", w.Body.String())
	}
	if code := errorCode(w); code != "AI_ANALYSIS_FAILED" {
		t.Errorf("게이트웨이에 닿지 못한 것을 %q 로 답합니다: %s", code, w.Body.String())
	}
}

// guards: suggestDecisions
//
// A task with nothing written on it has nothing for the model to read, and the
// handler answers that itself rather than paying for a call that cannot help.
// Invert the check and the two swap: an empty task goes to the gateway, and a
// task with a full history comes back with nothing and a caveat explaining why.
func TestATaskWithNothingWrittenOnItDoesNotGoToTheGateway(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("결정 제안 조직", "AISUGGEST")
	owner := server.createUser("ai_suggest", "USER", &organisation)
	server.enableAIWithoutAGateway(t)

	// One reported week carrying a title and nothing else.
	id, version := server.draft(owner, "2026-08-24", "빈 내용 보고")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "빈 내용 보고", "version": version,
		"items": []map[string]any{
			{"category": "개발", "title": "아무것도 안 적힌 업무", "currentResult": "", "nextPlan": "", "issue": "", "progress": 0},
		},
	}, owner)
	if filled.Code != http.StatusOK {
		t.Fatalf("보고서 저장: %d %s", filled.Code, filled.Body.String())
	}
	next := decodeData(t, filled)["version"].(float64)
	if handed := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id),
		map[string]any{"version": int(next)}, owner); handed.Code != http.StatusOK {
		t.Fatalf("제출: %d %s", handed.Code, handed.Body.String())
	}

	workItemID := server.workItemNamed(server.lastCreatedUsername("ai_suggest"), "아무것도 안 적힌 업무")
	w := server.request(http.MethodPost,
		fmt.Sprintf("/api/v1/work-items/%d/decisions/suggest", workItemID), map[string]any{}, owner)
	if w.Code != http.StatusOK {
		t.Fatalf("적힌 것이 없는 업무인데 게이트웨이까지 갔습니다: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Candidates []struct {
				Title string `json:"title"`
			} `json:"candidates"`
			Caveat string `json:"caveat"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Candidates) != 0 {
		t.Errorf("읽을 것이 없는데 후보 %d개를 만들었습니다", len(envelope.Data.Candidates))
	}
	if strings.TrimSpace(envelope.Data.Caveat) == "" {
		t.Error("빈 결과를 돌려주면서 왜인지 말하지 않습니다")
	}
}
