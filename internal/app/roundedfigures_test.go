package app

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
)

// Every percentage this product publishes is rounded to one decimal.
//
// admin_analytics.go calls round1 six times and rollup.go ten; analytics.go
// called it never, and its two figures left the API raw. Measured on a running
// deployment for the same week and the same people: 참여 분석 answered 90.8 and
// /api/v1/analytics/overview answered 90.78947368421052. The screen formats it
// with toFixed(1), so the screen was right either way — the API was not, and
// the MCP tool weekly_submission_overview is built on this endpoint, so an AI
// client asked to summarise a week was handed sixteen digits of a percentage.
//
// guards: analyticsOverview
func TestPublishedPercentagesAreRounded(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("반올림 조직", "ROUNDED")
	// Three people, one report: 1/3 is 33.333…, which is the only shape that
	// shows an unrounded figure at all. The first version of this test made
	// four and measured 25% — exact, so reverting the fix still passed it. A
	// fixture that cannot produce the state is not a test of it.
	for _, stem := range []string{"round_one", "round_two"} {
		server.createUser(stem, "USER", &organization)
	}
	// The one who asks has to be a leader — /api/v1/analytics/overview is not
	// for a plain writer — and has to be inside the same organisation, so the
	// population is these three and the ratio is 1/3.
	writer := server.createUser("round_writer", "TEAM_LEADER", &organization)
	id, version := server.draft(writer, "2026-08-24", "반올림 시험")
	filled := server.request(http.MethodPut, "/api/v1/reports/"+itoa(id), map[string]any{
		"summary": "반올림 시험", "version": version,
		"items": []any{map[string]any{"title": "반올림", "category": "운영",
			"currentResult": "한 일", "nextPlan": "", "issue": "", "progress": 33}},
	}, writer)
	if filled.Code != http.StatusOK {
		t.Fatalf("저장이 %d: %s", filled.Code, filled.Body.String())
	}
	submitted := server.request(http.MethodPost, "/api/v1/reports/"+itoa(id)+"/submit",
		map[string]any{"version": int(decodeData(t, filled)["version"].(float64))}, writer)
	if submitted.Code != http.StatusOK {
		t.Fatalf("제출이 %d: %s", submitted.Code, submitted.Body.String())
	}

	// Asked as somebody inside the organisation, not as the administrator: an
	// ADMIN is scoped to the whole deployment, so the population became every
	// account the harness had made and the ratio came out exact. That is the
	// second way this test failed to test itself.
	response := server.request(http.MethodGet, "/api/v1/analytics/overview?week=2026-08-24", nil, writer)
	if response.Code != http.StatusOK {
		t.Fatalf("분석이 %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for key, value := range envelope.Data {
		number, ok := value.(float64)
		if !ok || number == math.Trunc(number) {
			continue
		}
		if rounded := math.Round(number*10) / 10; math.Abs(number-rounded) > 1e-9 {
			t.Errorf("%s 가 소수 첫째 자리를 넘습니다: %v — 다른 분석 화면은 모두 한 자리로 내보냅니다", key, number)
		}
	}
	// The raw body, because a float that survives rounding still has to be
	// short on the wire: this is what an MCP client reads.
	if body := response.Body.String(); strings.Contains(body, "3333333") {
		t.Errorf("응답 본문에 반올림되지 않은 값이 있습니다: %s", body)
	}
}
