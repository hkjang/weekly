package app

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConfirmImportPayloadAcceptsLegacyUISelectionField(t *testing.T) {
	body := `{"files":[{"id":7,"selected":true,"weekStart":"2026-08-10","summary":"summary","strategy":"CREATE","items":[{"category":"개발","title":"Import 복구","currentResult":"완료","nextPlan":"검증","issue":"","progress":80,"confidence":0.9}]}]}`
	request := httptest.NewRequest("POST", "/api/v1/import/1/confirm", strings.NewReader(body))
	response := httptest.NewRecorder()
	var input struct {
		Files []confirmImportFile `json:"files"`
	}

	if !decodeJSON(response, request, &input) {
		t.Fatalf("expected UI payload to decode, status=%d body=%s", response.Code, response.Body.String())
	}
	if len(input.Files) != 1 || input.Files[0].ID != 7 || !input.Files[0].Selected {
		t.Fatalf("unexpected decoded payload: %+v", input.Files)
	}
}

func TestFinalizeImportedAIResultAlignsExplicitRangeAndRequiresReview(t *testing.T) {
	result := aiWeeklyResult{
		WeekStart: "2026-08-02", DateConfidence: 0.95,
		ReportItems: []aiReportItem{{Title: "PPTX 분석", Confidence: 0.9, CategoryConfidence: 0.9, SourceSlides: []int{1}}},
	}
	detected := detectedWeek{
		Start: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC),
		Confidence: 0.98, Source: "slide_text",
	}
	decision := finalizeImportedAIResult(&result, detected, extractedPPTX{SlideCount: 4}, "MONDAY", time.UTC)
	if got := decision.WeekStart.Format("2006-01-02"); got != "2026-08-03" {
		t.Fatalf("aligned week = %s", got)
	}
	if decision.Status != "NEEDS_REVIEW" || decision.Confidence != 0.7 || decision.DateSource != "slide_text_aligned" {
		t.Fatalf("decision = %#v", decision)
	}
	if result.WeekStart != "2026-08-03" || !strings.Contains(strings.Join(result.Warnings, "\n"), "주차 시작 요일") {
		t.Fatalf("result = %#v", result)
	}
}

func TestFinalizeImportedAIResultFlagsTruncationAndLowCategoryConfidence(t *testing.T) {
	result := aiWeeklyResult{ReportItems: []aiReportItem{{Title: "분류 불명", Confidence: 0.9, CategoryConfidence: 0.4, SourceSlides: []int{2}}}}
	detected := detectedWeek{
		Start: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		Confidence: 0.98, Source: "slide_text",
	}
	decision := finalizeImportedAIResult(&result, detected, extractedPPTX{SlideCount: 4, Truncated: true, TruncatedSlides: []int{2, 4}}, "MONDAY", time.UTC)
	if decision.Status != "NEEDS_REVIEW" {
		t.Fatalf("status = %s", decision.Status)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "슬라이드 2, 4") || !strings.Contains(warnings, "업무 분류 신뢰도") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestFinalizeImportedAIResultKeepsHighConfidenceResultReady(t *testing.T) {
	result := aiWeeklyResult{ReportItems: []aiReportItem{{Title: "구조 보존", Confidence: 0.9, CategoryConfidence: 0.9, SourceSlides: []int{1}}}}
	detected := detectedWeek{
		Start: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Confidence: 0.98, Source: "slide_text",
	}
	decision := finalizeImportedAIResult(&result, detected, extractedPPTX{SlideCount: 4}, "TUESDAY", time.UTC)
	if decision.Status != "READY" || result.WeekStart != "2026-08-04" || len(result.Warnings) != 0 {
		t.Fatalf("decision=%#v result=%#v", decision, result)
	}
}

func TestUniquePositiveIDsKeepsStableSelection(t *testing.T) {
	got := uniquePositiveIDs([]int64{7, 3, 7, -1, 0, 5, 3})
	want := []int64{7, 3, 5}
	if len(got) != len(want) {
		t.Fatalf("unique ids = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unique ids = %v, want %v", got, want)
		}
	}
}

func TestRetryImportSelectionRejectsEmptyAndMalformedFileIDs(t *testing.T) {
	for _, body := range []string{`{"fileIds":[]}`, `{"fileIds":null}`, `{"fileIds":"7"}`} {
		var input struct {
			FileIDs json.RawMessage `json:"fileIds"`
		}
		if err := json.NewDecoder(strings.NewReader(body)).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if _, _, err := parseRetryFileIDs(input.FileIDs); err == nil {
			t.Errorf("body %s should be invalid", body)
		}
	}
	if ids, selected, err := parseRetryFileIDs(json.RawMessage(`[7,3,7]`)); err != nil || !selected || len(ids) != 2 || ids[0] != 7 || ids[1] != 3 {
		t.Fatalf("valid selection = %v selected=%v err=%v", ids, selected, err)
	}
	if ids, selected, err := parseRetryFileIDs(nil); err != nil || selected || ids != nil {
		t.Fatalf("omitted selection = %v selected=%v err=%v", ids, selected, err)
	}
	tooMany := make([]int64, 101)
	for index := range tooMany {
		tooMany[index] = 1
	}
	encoded, _ := json.Marshal(tooMany)
	if _, _, err := parseRetryFileIDs(encoded); !errors.Is(err, errTooManyRetryFiles) {
		t.Fatalf("too many duplicate IDs must still be rejected: %v", err)
	}
}

func TestNormalizeImportedItemsFormatsAndDeduplicates(t *testing.T) {
	items := normalizeImportedItems([]reportItem{
		{Category: "", Title: " OAuth ", CurrentResult: "연동 완료, 권한 검증 완료.", Progress: 60},
		{Category: "미분류", Title: "OAuth", CurrentResult: "• 권한 검증 완료.\n• 운영 문서 작성", NextPlan: "배포", Progress: 80},
	})
	if len(items) != 1 {
		t.Fatalf("normalized items = %#v", items)
	}
	item := items[0]
	if item.Category != "미분류" || item.Title != "OAuth" || item.Progress != 80 || item.SortOrder != 0 {
		t.Fatalf("normalized item metadata = %#v", item)
	}
	if strings.Count(item.CurrentResult, "권한 검증 완료.") != 1 || !strings.Contains(item.CurrentResult, "연동 완료") || !strings.Contains(item.CurrentResult, "운영 문서 작성") {
		t.Fatalf("normalized content = %q", item.CurrentResult)
	}
}
