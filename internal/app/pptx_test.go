package app

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaultPPTXIsValidAndRenderable(t *testing.T) {
	template, err := defaultPPTX()
	if err != nil {
		t.Fatal(err)
	}
	placeholders, err := analyzePPTX(template)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"{{WEEK_SCHEDULE}}", "{{THIS_WEEK}}", "{{NEXT_WEEK}}"} {
		if !contains(placeholders, required) {
			t.Fatalf("missing placeholder %s", required)
		}
	}
	result, err := renderPPTX(template, map[string]string{
		"{{WEEK_SCHEDULE}}": "2026.01.05 ~ 01.11",
		"{{THIS_WEEK}}":     "• API 구현\n  인증 완료",
		"{{NEXT_WEEK}}":     "• 배포 검증",
		"{{ISSUES}}":        "-",
		"{{AUTHOR}}":        "홍길동",
		"{{TEAM}}":          "AI 엔지니어링",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result, []byte("{{THIS_WEEK}}")) {
		t.Fatal("rendered file still contains placeholder")
	}
	validatePPTXXML(t, result)
}

func validatePPTXXML(t *testing.T, body []byte) {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if !strings.HasSuffix(file.Name, ".xml") && !strings.HasSuffix(file.Name, ".rels") {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(stream)
		for {
			_, err = decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				stream.Close()
				t.Fatalf("invalid XML in %s: %v", file.Name, err)
			}
		}
		stream.Close()
	}
}

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password did not verify")
	}
	if verifyPassword(hash, "incorrect") {
		t.Fatal("incorrect password verified")
	}
}

func TestCurrentWeekStart(t *testing.T) {
	now := time.Date(2026, time.January, 8, 12, 0, 0, 0, time.UTC)
	tests := map[string]string{
		"SUNDAY": "2026-01-04", "MONDAY": "2026-01-05", "TUESDAY": "2026-01-06",
		"WEDNESDAY": "2026-01-07", "THURSDAY": "2026-01-08", "FRIDAY": "2026-01-02", "SATURDAY": "2026-01-03",
	}
	for weekday, expected := range tests {
		if got := currentWeekStart(now, weekday).Format("2006-01-02"); got != expected {
			t.Errorf("%s: got %s, want %s", weekday, got, expected)
		}
	}
	if got := currentWeekStart(now, "invalid").Format("2006-01-02"); got != "2026-01-05" {
		t.Fatalf("invalid setting fallback = %s", got)
	}
}

func TestSuppliedReferencePPTXRendering(t *testing.T) {
	template, err := os.ReadFile("../../1월5주간업무보고_AI엔지니어링.pptx")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("reference PPTX is not present")
	}
	if err != nil {
		t.Fatal(err)
	}
	report := &reportView{Items: []reportItem{{Category: "Weekly", Title: "서비스 구현", CurrentResult: "OIDC 연동 완료", NextPlan: "오프라인 배포 검증"}}}
	result, err := renderReferencePPTX(template, report, "AI엔지니어링 파트", time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	validatePPTXXML(t, result)
	text := pptxXMLText(t, result)
	if strings.Contains(text, "2026.1.26") {
		t.Fatal("old schedule remains in rendered reference")
	}
	if !strings.Contains(text, "OIDC 연동 완료") || !strings.Contains(text, "오프라인 배포 검증") {
		t.Fatal("report content was not inserted")
	}
}

func TestGeneratedReferenceStylePPTX(t *testing.T) {
	template, err := referenceStylePPTX()
	if err != nil {
		t.Fatal(err)
	}
	report := &reportView{Items: []reportItem{
		{Category: "플랫폼", Title: "OIDC", CurrentResult: "연동 완료", NextPlan: "운영 검증"},
		{Category: "배포", Title: "오프라인", CurrentResult: "이미지 생성", NextPlan: "반입 시험"},
	}}
	result, err := renderReferencePPTX(template, report, "AI엔지니어링 파트", time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	validatePPTXXML(t, result)
	text := pptxXMLText(t, result)
	for _, expected := range []string{"추진실적 (2026.8.3 ~ 2026.8.9)", "추진계획 (2026.8.10 ~ 2026.8.16)", "연동 완료", "반입 시험"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("generated reference is missing %q", expected)
		}
	}
	reader, err := zip.NewReader(bytes.NewReader(result), int64(len(result)))
	if err != nil {
		t.Fatal(err)
	}
	slides := 0
	for _, file := range reader.File {
		if isSlideXML(file.Name) {
			slides++
		}
	}
	// Two tasks, two pages. This used to assert four — the template's count —
	// which meant every export carried its unused pages into the meeting.
	if slides != 2 {
		t.Fatalf("got %d slides for a two-item report, want 2", slides)
	}
}

func TestReportItemLinesPreserveReadableListStructure(t *testing.T) {
	items := []reportItem{{Category: "플랫폼", Title: "인증", CurrentResult: "• OIDC 연동\n• 권한 검증"}}
	got := reportItemLines(items, "current")
	if !strings.Contains(got, "• [플랫폼] 인증\n  - OIDC 연동\n  - 권한 검증") {
		t.Fatalf("unexpected report lines: %q", got)
	}
}

func TestDistributeReferenceItemsBalancesLargeSingleCategory(t *testing.T) {
	items := make([]reportItem, 12)
	for index := range items {
		items[index] = reportItem{ID: int64(index + 1), Category: "플랫폼", Title: fmt.Sprintf("업무 %02d", index+1), CurrentResult: "구현 완료", NextPlan: "검증 예정"}
	}

	groups := distributeReferenceItems(items, 4)
	for index, group := range groups {
		if len(group) != 3 {
			t.Fatalf("slide %d has %d items, want 3", index+1, len(group))
		}
		body := referenceTextBody(group, "current")
		if count := strings.Count(body, "<a:t>플랫폼</a:t>"); count != 1 {
			t.Fatalf("slide %d category heading count = %d, want 1", index+1, count)
		}
	}
	assertReferenceDistribution(t, items, groups)
}

func TestDistributeReferenceItemsDoesNotPileExtraCategoriesOnLastSlide(t *testing.T) {
	items := make([]reportItem, 7)
	for index := range items {
		items[index] = reportItem{ID: int64(index + 1), Category: fmt.Sprintf("구분 %d", index+1), Title: fmt.Sprintf("업무 %d", index+1), CurrentResult: "완료", NextPlan: "예정"}
	}

	groups := distributeReferenceItems(items, 4)
	minimumSize, maximumSize := len(items), 0
	for index, group := range groups {
		if len(group) == 0 {
			t.Fatalf("slide %d is empty", index+1)
		}
		if len(group) < minimumSize {
			minimumSize = len(group)
		}
		if len(group) > maximumSize {
			maximumSize = len(group)
		}
	}
	if maximumSize-minimumSize > 1 || len(groups[len(groups)-1]) > 2 {
		t.Fatalf("unbalanced category distribution: %v", referenceGroupSizes(groups))
	}
	assertReferenceDistribution(t, items, groups)
}

func TestDistributeReferenceItemsUsesLongerColumnAndExpectedWraps(t *testing.T) {
	items := []reportItem{
		{ID: 1, Category: "플랫폼", Title: "짧은 업무 1", CurrentResult: "완료", NextPlan: "예정"},
		{ID: 2, Category: "플랫폼", Title: "짧은 업무 2", CurrentResult: "완료", NextPlan: "예정"},
		{ID: 3, Category: "플랫폼", Title: "긴 업무", CurrentResult: "완료", NextPlan: strings.Repeat("장문 계획 ", 80)},
		{ID: 4, Category: "플랫폼", Title: "짧은 업무 3", CurrentResult: "완료", NextPlan: "예정"},
		{ID: 5, Category: "플랫폼", Title: "짧은 업무 4", CurrentResult: "완료", NextPlan: "예정"},
	}

	groups := distributeReferenceItems(items, 3)
	if len(groups[1]) != 1 || groups[1][0].ID != 3 {
		t.Fatalf("long item should occupy its own middle slide: sizes=%v groups=%#v", referenceGroupSizes(groups), groups)
	}
	assertReferenceDistribution(t, items, groups)
}

func TestDistributeReferenceItemsPreservesEveryItemExactlyOnce(t *testing.T) {
	items := make([]reportItem, 19)
	for index := range items {
		items[index] = reportItem{
			ID:            int64(index + 1),
			Category:      fmt.Sprintf("구분 %d", index%5),
			Title:         fmt.Sprintf("고유 업무 %02d", index+1),
			CurrentResult: strings.Repeat("실적 ", index%4+1),
			NextPlan:      strings.Repeat("계획 ", index%6+1),
		}
	}

	groups := distributeReferenceItems(items, 4)
	assertReferenceDistribution(t, items, groups)
}

func TestReferenceTextBodySplitsTwoCommasIntoSeparateParagraphs(t *testing.T) {
	body := referenceTextBody([]reportItem{{
		Category: "플랫폼", Title: "인증", CurrentResult: "OIDC 연동 완료, 권한 검증 완료, 운영 배포 준비",
	}}, "current")

	// One category, one title, and three independently rendered detail paragraphs.
	if count := strings.Count(body, "<a:p>"); count != 5 {
		t.Fatalf("paragraph count = %d, want 5: %s", count, body)
	}
	for _, detail := range []string{"OIDC 연동 완료", "권한 검증 완료", "운영 배포 준비"} {
		if count := strings.Count(body, "<a:t>"+detail+"</a:t>"); count != 1 {
			t.Fatalf("detail %q paragraph count = %d, want 1", detail, count)
		}
	}
}

func assertReferenceDistribution(t *testing.T, items []reportItem, groups [][]reportItem) {
	t.Helper()
	seen := make(map[int64]int, len(items))
	flattened := make([]int64, 0, len(items))
	for _, group := range groups {
		for _, item := range group {
			seen[item.ID]++
			flattened = append(flattened, item.ID)
		}
	}
	if len(flattened) != len(items) {
		t.Fatalf("distributed item count = %d, want %d", len(flattened), len(items))
	}
	for index, item := range items {
		if seen[item.ID] != 1 {
			t.Fatalf("item %d occurrence count = %d, want 1", item.ID, seen[item.ID])
		}
		if flattened[index] != item.ID {
			t.Fatalf("item order at %d = %d, want %d", index, flattened[index], item.ID)
		}
	}
}

func referenceGroupSizes(groups [][]reportItem) []int {
	result := make([]int, len(groups))
	for index := range groups {
		result[index] = len(groups[index])
	}
	return result
}

func pptxXMLText(t *testing.T, body []byte) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var result strings.Builder
	for _, file := range reader.File {
		if !strings.HasPrefix(file.Name, "ppt/slides/slide") || !strings.HasSuffix(file.Name, ".xml") {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		result.Write(data)
	}
	return result.String()
}
