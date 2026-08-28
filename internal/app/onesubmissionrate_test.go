package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// One deployment, one meaning for 제출률.
//
// Two screens showed the number and called it the same thing. 관리자 참여 분석
// counted every report that is not a 초안; 분석 화면 also excluded the ones a
// leader had sent back. Measured on a 305-account deployment for the same week:
// 91.1% and 82.0%, and the 9.1 points were exactly the 28 reports in 반려.
// Neither screen said which question it was answering, so a leader reading both
// had two numbers and no way to choose.
//
// This does not assert which definition is right. It asserts they are the same
// one, which is the part a reader depends on and the part that drifted.
//
// guards: analyticsOverview, analyticsParticipation
func TestBothScreensCountTheSameSubmissions(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("한 정의 조직", "ONERATE")
	week := "2026-08-24"

	// One report in each state a week can end in, so any definition that
	// disagrees about any of them shows up here.
	type writerState struct {
		stem  string
		state string
	}
	for _, who := range []writerState{
		{"rate_draft", "DRAFT"},
		{"rate_submitted", "SUBMITTED"},
		{"rate_returned", "REVISION_REQUESTED"},
		{"rate_approved", "APPROVED"},
	} {
		cookie := server.createUser(who.stem, "USER", &organization)
		id, version := server.draft(cookie, week, who.stem+" 보고")
		if who.state == "DRAFT" {
			continue
		}
		filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
			"summary": who.stem + " 보고", "version": version,
			"items": []any{map[string]any{"title": "제출률 시험", "category": "운영",
				"currentResult": "한 일", "nextPlan": "할 일", "issue": "", "progress": 40}},
		}, cookie)
		if filled.Code != http.StatusOK {
			t.Fatalf("%s 항목 저장이 %d: %s", who.stem, filled.Code, filled.Body.String())
		}
		version = int(decodeData(t, filled)["version"].(float64))
		set := server.request(http.MethodPut, "/api/v1/admin/settings",
			map[string]any{"settings": map[string]string{"workflow.enabled": "true"}}, server.admin)
		if set.Code != http.StatusOK {
			t.Fatalf("workflow 설정이 %d: %s", set.Code, set.Body.String())
		}
		submitted := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id),
			map[string]any{"version": version}, cookie)
		if submitted.Code != http.StatusOK {
			t.Fatalf("%s 제출이 %d: %s", who.stem, submitted.Code, submitted.Body.String())
		}
		switch who.state {
		case "REVISION_REQUESTED":
			sent := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/reject", id),
				map[string]any{"comment": "근거를 더 적어 주세요"}, server.admin)
			if sent.Code != http.StatusOK {
				t.Fatalf("반려가 %d: %s", sent.Code, sent.Body.String())
			}
		case "APPROVED":
			ok := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/approve", id), map[string]any{}, server.admin)
			if ok.Code != http.StatusOK {
				t.Fatalf("승인이 %d: %s", ok.Code, ok.Body.String())
			}
		}
	}

	overview := decodeData(t, server.request(http.MethodGet, "/api/v1/analytics/overview?week="+week, nil, server.admin))
	fromOverview := int(overview["submittedUsers"].(float64))

	participation := server.request(http.MethodGet, "/api/v1/admin/analytics/participation?weeks=8", nil, server.admin)
	if participation.Code != http.StatusOK {
		t.Fatalf("참여 분석이 %d: %s", participation.Code, participation.Body.String())
	}
	var envelope struct {
		Data struct {
			Trend []struct {
				WeekStart string `json:"weekStart"`
				Submitted int    `json:"submitted"`
			} `json:"trend"`
		} `json:"data"`
	}
	if err := json.Unmarshal(participation.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range envelope.Data.Trend {
		if entry.WeekStart != week {
			continue
		}
		found = true
		if entry.Submitted != fromOverview {
			t.Errorf("같은 주 %s 의 제출 수가 다릅니다 — 참여 분석 %d명, 분석 화면 %d명.\n"+
				"  두 화면 모두 이 숫자를 제출률이라고 부릅니다. 정의가 하나여야 합니다.\n"+
				"  상태별: %v", week, entry.Submitted, fromOverview, overview["statusCounts"])
		}
	}
	if !found {
		t.Fatalf("참여 분석에 %s 주가 없습니다: %+v", week, envelope.Data.Trend)
	}
}
