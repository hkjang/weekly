package app

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSuccessEnvelopeKeepsNullData(t *testing.T) {
	w := httptest.NewRecorder()
	writeData(w, 200, nil)
	if body := w.Body.String(); !strings.Contains(body, `"data":null`) {
		t.Fatalf("success envelope must retain data:null, got %s", body)
	}
}
