package app

import (
	"net/http/httptest"
	"strings"
	"testing"
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
