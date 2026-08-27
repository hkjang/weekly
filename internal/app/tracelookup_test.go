package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// api.ts appends "(추적 ID …)" to the message of any 5xx, so the reader is
// handed an identifier and asked to quote it. That is a promise that somebody
// can look it up. Of the places that answer 5xx, most wrote nothing to the log
// at all, and the operator was left holding an id that appeared nowhere.
//
// guards: requestMetrics, writeError
func TestTheTraceIdOnTheReadersScreenIsInTheLog(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("trace_lookup", "USER", nil)

	// A failure with no logging of its own. Hiding the table the handler reads
	// makes a real 500 out of a real request rather than a simulated one.
	if _, err := server.app.db.Exec(server.ctx(),
		`ALTER TABLE work_items RENAME TO work_items_hidden`); err != nil {
		t.Fatalf("hide the table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = server.app.db.Exec(server.ctx(), `ALTER TABLE work_items_hidden RENAME TO work_items`)
	})

	response := server.request(http.MethodGet, "/api/v1/work-items", nil, author)
	if response.Code < 500 {
		t.Fatalf("this case needs a 5xx, got %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		TraceID string `json:"traceId"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the refusal: %v", err)
	}
	if body.TraceID == "" {
		t.Fatal("a 5xx carried no trace id for the reader to quote")
	}
	// What the operator does: grep the logs for what the reader read out.
	if !server.logged(body.TraceID) {
		t.Errorf("the reader was shown trace %s and the log does not contain it", body.TraceID)
	}
	// And the line has to say which request it was, not only that one failed.
	for _, fragment := range []string{"/api/v1/work-items", body.Error.Code} {
		if fragment == "" {
			continue
		}
		if !server.logged(fragment) {
			t.Errorf("the log line does not name %q", fragment)
		}
	}
}

// Below 5xx the reader is shown no id, and a line for every refused request
// would bury the ones somebody is actually asking about.
//
// guards: requestMetrics
func TestAnOrdinaryRefusalIsNotLoggedAsAFailedRequest(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("trace_quiet", "USER", nil)
	response := server.request(http.MethodGet, "/api/v1/reports/999999", nil, author)
	if response.Code < 400 || response.Code >= 500 {
		t.Fatalf("this case needs a 4xx, got %d", response.Code)
	}
	var body struct {
		TraceID string `json:"traceId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the refusal: %v", err)
	}
	if server.logged("request failed", body.TraceID) {
		t.Error("an ordinary refusal was written to the log as a failed request")
	}
}
