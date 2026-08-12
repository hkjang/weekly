package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		content := `{"summary":"AI 주간 요약","weekStart":"2026-08-03","dateConfidence":0.95,"reportItems":[{"category":"플랫폼","title":"AI Gateway","currentResults":["인증 완료","권한 검증 완료"],"nextPlans":["부하 시험"],"issues":[],"progress":90,"confidence":0.93,"sourceSlides":[],"categoryConfidence":0.91}],"warnings":[]}`
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
	if result.ReportItems[0].CurrentResult != "• 인증 완료\n• 권한 검증 완료" || len(result.ReportItems[0].SourceSlides) != 0 || result.ReportItems[0].CategoryConfidence != result.ReportItems[0].Confidence {
		t.Fatalf("model-facing atomic values were not projected to the public contract: %#v", result.ReportItems[0])
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

func TestFormatAIListTextSplitsCommaSeparatedTasks(t *testing.T) {
	got := formatAIListText("OIDC 연동 완료, 권한 검증 완료, 운영 배포 준비.")
	want := "• OIDC 연동 완료\n• 권한 검증 완료\n• 운영 배포 준비."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := formatAIListText("처리량 1,000건 검증 완료."); got != "처리량 1,000건 검증 완료." {
		t.Fatalf("prose or thousands separator was changed: %q", got)
	}
	if got := formatAIListText("OIDC 연동 완료, 권한 검증 완료."); got != "• OIDC 연동 완료\n• 권한 검증 완료." {
		t.Fatalf("two atomic tasks were not split: %q", got)
	}
	for _, prose := range []string{
		"검토 결과, 설정을 유지했습니다.",
		"버전 v1.2, v1.3을 지원합니다.",
		"좌표 1, 2를 비교했습니다.",
		"서울, 부산, 대구를 방문했습니다.",
	} {
		if got := formatAIListText(prose); got != prose {
			t.Errorf("natural prose was split: got %q, want %q", got, prose)
		}
	}
}

func TestValidateAIResultStableMergesDuplicateTasksAndEvidence(t *testing.T) {
	result := aiWeeklyResult{ReportItems: []aiReportItem{
		{Category: "플랫폼", Title: "인증", CurrentResult: "- 연동 완료", Progress: 40, Confidence: 0.9, CategoryConfidence: 0.8, SourceSlides: []int{2, 1, 2}},
		{Category: "플랫폼", Title: "인증", CurrentResult: "• 연동 완료.", NextPlan: "권한 검증", Progress: 70, Confidence: 0.7, CategoryConfidence: 0.6, SourceSlides: []int{2}},
		{Category: "보안", Title: "인증", CurrentResult: "정책 검토", Progress: 30, Confidence: 0.8, CategoryConfidence: 0.5, SourceSlides: []int{1}},
	}}
	context := aiValidationContext{Mode: "PPTX_NORMALIZED", SourceSlides: map[int]bool{1: true, 2: true}}
	if err := validateAIResult(&result, context); err != nil {
		t.Fatal(err)
	}
	if len(result.ReportItems) != 2 {
		t.Fatalf("same category/title was not merged stably: %#v", result.ReportItems)
	}
	item := result.ReportItems[0]
	if item.CurrentResult != "• 연동 완료" || item.NextPlan != "권한 검증" || item.Progress != 70 || item.Confidence != 0.7 || item.CategoryConfidence != 0.6 {
		t.Fatalf("unexpected conservative merge: %#v", item)
	}
	if len(item.SourceSlides) != 2 || item.SourceSlides[0] != 1 || item.SourceSlides[1] != 2 {
		t.Fatalf("source slide union = %#v", item.SourceSlides)
	}
}

func TestValidateAIWeeklyStructureRejectsPackedFacts(t *testing.T) {
	value := aiStructuredWeeklyResult{ReportItems: []aiStructuredReportItem{{Title: "인증", CurrentResults: []string{"OIDC 연동 완료, 권한 검증 완료"}}}}
	if err := validateAIWeeklyStructure(value); err == nil || !strings.Contains(err.Error(), "exactly one fact") {
		t.Fatalf("packed array fact should require correction, got %v", err)
	}
	value.ReportItems[0].CurrentResults = []string{"OIDC 연동 완료", "권한 검증 완료"}
	if err := validateAIWeeklyStructure(value); err != nil {
		t.Fatalf("atomic facts were rejected: %v", err)
	}
}

func TestCallWeeklyAICorrectsInvalidPPTXSlideEvidenceOnce(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		messages, _ := json.Marshal(request["messages"])
		if calls == 2 && !strings.Contains(string(messages), "source slide 99") {
			t.Fatalf("corrective request omitted validation reason: %s", messages)
		}
		slide := 99
		if calls == 2 {
			slide = 2
		}
		content := `{"summary":"","weekStart":"","dateConfidence":0,"reportItems":[{"category":"","title":"인증","currentResults":["연동 완료"],"nextPlans":[],"issues":[],"progress":100,"confidence":0.8,"sourceSlides":[` + strconv.Itoa(slide) + `],"categoryConfidence":0.9}],"warnings":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()

	input := "=== SLIDE 2 (SOURCE slide2.xml) ===\n인증 연동 완료"
	result, _, err := callWeeklyAI(t.Context(), aiConfiguration{Endpoint: server.URL, Model: "test", Timeout: 3 * time.Second, MaxInput: 50000}, "PPTX_NORMALIZED", input)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(result.ReportItems) != 1 || result.ReportItems[0].Category != "미분류" || result.ReportItems[0].CategoryConfidence != 0.4 || result.ReportItems[0].SourceSlides[0] != 2 {
		t.Fatalf("corrected result = %#v, calls = %d", result, calls)
	}
}
