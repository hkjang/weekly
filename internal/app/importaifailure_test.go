package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Reproduced on a deployment: with the gateway answering 500, the connection
// test in 관리자 설정 said "AI Gateway가 오류로 응답했습니다(HTTP 500). 게이트웨이 쪽
// 문제이므로 이 설정을 바꿔도 해결되지 않습니다." while the import screen showed
// "AI endpoint returned HTTP 500" on the file card. One failure, two screens,
// and only one of them written for the person reading it.

// guards: processImportFile, aiUserMessage
func TestAFailedImportSaysWhatTheAdministratorsTestWouldSay(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("import_ai_failure", "USER", nil)

	id, version := server.draft(author, "2026-08-24", "가져오기 실패 확인")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "가져오기 실패 확인", "version": version,
		"items": []map[string]any{{
			"category": "인프라", "title": "회선 이설", "currentResult": "3개 지사 완료",
			"nextPlan": "잔여 2개 지사", "issue": "일정 지연", "progress": 60,
		}},
	}, author)
	if filled.Code != http.StatusOK {
		t.Fatalf("보고서 저장: %d %s", filled.Code, filled.Body.String())
	}
	deck := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, author)
	if deck.Code != http.StatusOK {
		t.Fatalf("내보내기: %d %s", deck.Code, deck.Body.String())
	}

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gateway.Close()
	if on := server.request(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"settings": map[string]string{"ai.enabled": "true", "ai.endpoint": gateway.URL, "ai.model": "weekly-1"},
	}, server.admin); on.Code != http.StatusOK {
		t.Fatalf("AI 를 켜지 못했습니다: %d %s", on.Code, on.Body.String())
	}

	upload := server.uploadMany("/api/v1/import/pptx", "files",
		map[string][]byte{"실패할것.pptx": deck.Body.Bytes()}, author)
	if upload.Code != http.StatusAccepted && upload.Code != http.StatusOK {
		t.Fatalf("업로드: %d %s", upload.Code, upload.Body.String())
	}
	var envelope struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	status, message := awaitImportJob(t, server, envelope.Data.ID)
	if status != "FAILED" {
		t.Fatalf("게이트웨이가 500을 돌려줬는데 작업이 %q 로 끝났습니다", status)
	}

	// The administrator's own screen is the standard; the file card has to
	// meet it rather than leak the error the Go code passes around.
	want := aiUserMessage(&aiStatusError{Status: http.StatusInternalServerError})
	if message != want {
		t.Errorf("파일에 남은 말은 %q, 관리자 화면이 하는 말은 %q", message, want)
	}
	if strings.Contains(message, "AI endpoint returned") {
		t.Errorf("내부 오류 문자열이 화면에 그대로 남았습니다: %q", message)
	}
}
