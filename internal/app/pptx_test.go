package app

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
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
	if got := currentWeekStart(now, "MONDAY").Format("2006-01-02"); got != "2026-01-05" {
		t.Fatalf("got %s", got)
	}
	if got := currentWeekStart(now, "SUNDAY").Format("2006-01-02"); got != "2026-01-04" {
		t.Fatalf("got %s", got)
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
	for _, expected := range []string{"추진실적 (2026.8.3 ~ 2026.8.7)", "추진계획 (2026.8.10 ~ 2026.8.14)", "연동 완료", "반입 시험"} {
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
	if slides != 4 {
		t.Fatalf("got %d slides, want 4", slides)
	}
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
