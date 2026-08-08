package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCallWeeklyAIStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		responseFormat, ok := request["response_format"].(map[string]any)
		if !ok || responseFormat["type"] != "json_schema" {
			t.Fatalf("structured response format missing: %#v", request["response_format"])
		}
		content := `{"summary":"AI 주간 요약","weekStart":"2026-08-03","dateConfidence":0.95,"reportItems":[{"category":"플랫폼","title":"AI Gateway","currentResult":"인증 완료","nextPlan":"부하 시험","issue":"","progress":90,"confidence":0.93}],"warnings":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()

	result, raw, err := callWeeklyAI(t.Context(), aiConfiguration{Endpoint: server.URL, APIKey: "test-key", Model: "test-model", Timeout: 5 * time.Second, MaxInput: 50000}, "FREE_TEXT", "금주 인증 완료")
	if err != nil {
		t.Fatal(err)
	}
	if result.WeekStart != "2026-08-03" || len(result.ReportItems) != 1 || result.ReportItems[0].Title != "AI Gateway" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !strings.Contains(raw, "AI 주간 요약") {
		t.Fatalf("raw response not retained: %s", raw)
	}
}

func TestValidateAIResultRejectsEmptyItems(t *testing.T) {
	result := aiWeeklyResult{ReportItems: []aiReportItem{{Title: "  "}}}
	if err := validateAIResult(&result); err == nil {
		t.Fatal("empty titled items must be rejected")
	}
}
