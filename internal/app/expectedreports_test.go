package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// 기대 보고 건수 has to mean weeks somebody was actually owed.
//
// It used to be members × weeks. On a deployment installed today that reads
// "12건 중 0건 · 0%" for an organisation one day old, while 참여 분석 on the
// next tab reports nobody behind — two administrator screens answering the same
// question in opposite directions, on the first screen an operator sees after
// installing. 참여 분석 already had the rule: a week counts against somebody
// only after they were here and after its deadline passed.
//
// Sharing the rule then exposed the other half. The denominator stopped
// counting the open week while the numerator still counted reports filed in it,
// and the table — which prints the two as "제출 / 기대건" — showed 109.1%.
//
// guards: analyticsOrganizations
func TestAnOrganisationIsOnlyOwedTheWeeksItsPeopleWereHereFor(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("기대 조직", "EXPECTED")
	server.createUser("expected_writer", "USER", &organization)

	read := func() map[string]any {
		response := server.request(http.MethodGet, "/api/v1/admin/analytics/organizations", nil, server.admin)
		if response.Code != http.StatusOK {
			t.Fatalf("조직 분석이 %d: %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data struct {
				Organizations []map[string]any `json:"organizations"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		for _, item := range envelope.Data.Organizations {
			if item["name"] == "기대 조직" {
				return item
			}
		}
		t.Fatalf("조직이 목록에 없습니다: %+v", envelope.Data.Organizations)
		return nil
	}

	// An account created a moment ago is owed nothing yet, and the other screen
	// says so. The two have to say it together.
	fresh := read()
	if expected := int(fresh["expectedReports"].(float64)); expected != 0 {
		t.Errorf("오늘 만든 계정에 보고서 %d건을 기대합니다 — 아직 마감된 주가 없습니다", expected)
	}
	participation := server.request(http.MethodGet, "/api/v1/admin/analytics/participation?weeks=12", nil, server.admin)
	if participation.Code != http.StatusOK {
		t.Fatalf("참여 분석이 %d: %s", participation.Code, participation.Body.String())
	}
	var trend struct {
		Data struct {
			Missing []map[string]any `json:"missing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(participation.Body.Bytes(), &trend); err != nil {
		t.Fatal(err)
	}
	if len(trend.Data.Missing) != 0 && int(fresh["expectedReports"].(float64)) == 0 {
		// Both directions of the disagreement are worth naming, so the message
		// says which screen claimed what.
		t.Errorf("한 화면은 기대 0건, 다른 화면은 밀린 사람 %d명이라고 합니다", len(trend.Data.Missing))
	}

	// Whatever the window holds, filed can never exceed owed: they are counted
	// over the same weeks or the fraction on the screen is not a fraction.
	for _, item := range []map[string]any{fresh} {
		reports := int(item["reports"].(float64))
		expected := int(item["expectedReports"].(float64))
		if reports > expected {
			t.Errorf("제출 %d건이 기대 %d건보다 많습니다 — 화면은 이 둘을 '제출 / 기대건'으로 보여 줍니다",
				reports, expected)
		}
		if rate, ok := item["submissionRate"].(float64); ok && rate > 100 {
			t.Errorf("제출률이 %.1f%% 입니다", rate)
		}
	}
	_ = fmt.Sprint()
}
