package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// 내 기록은 조직이 보는 것과 같은 주를 셉니다.
//
// guards: myParticipation
func TestMyRecordCountsTheWeeksTheArrearsListCounts(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("streaker", "USER", nil)

	location := server.app.serviceLocation(server.ctx())
	current := currentWeekStart(time.Now().In(location), "MONDAY")
	week := func(back int) string { return current.AddDate(0, 0, -7*back).Format(dateLayout) }

	file := func(weekStart string) {
		id, version := server.draft(author, weekStart, weekStart+" 보고")
		if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
			"summary": weekStart + " 보고", "version": version,
			"items": []map[string]any{{"category": "개발", "title": "한 일", "currentResult": "했습니다", "progress": 100}},
		}, author); w.Code != http.StatusOK {
			t.Fatalf("write %s: %d %s", weekStart, w.Code, w.Body.String())
		}
		if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id), nil, author); w.Code != http.StatusOK {
			t.Fatalf("submit %s: %d %s", weekStart, w.Code, w.Body.String())
		}
	}
	read := func() participationView {
		response := server.request(http.MethodGet, "/api/v1/me/participation", nil, author)
		if response.Code != http.StatusOK {
			t.Fatalf("participation: %d %s", response.Code, response.Body.String())
		}
		var payload struct {
			Data participationView `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v", response.Body.String(), err)
		}
		return payload.Data
	}

	// 지난 네 주 가운데 셋을 냈고, 가장 오래된 주는 빠뜨렸습니다.
	file(week(1))
	file(week(2))
	file(week(3))

	record := read()
	if record.Streak != 3 {
		t.Errorf("연속 %d주, want 3 (%+v)", record.Streak, record)
	}
	if record.Filed != 3 {
		t.Errorf("낸 주 %d, want 3", record.Filed)
	}
	if record.Owed < record.Filed {
		t.Errorf("낸 주가 내야 했던 주보다 많습니다: %+v", record)
	}

	// 이번 주는 아직 열려 있습니다. 연속에 세지 않고, 낼 것이 남았다고만 말합니다 —
	// 여기서 세면 월요일 아침마다 모두의 기록이 끊깁니다.
	if record.ThisWeekStart != current.Format(dateLayout) {
		t.Errorf("이번 주가 %q, want %q", record.ThisWeekStart, current.Format(dateLayout))
	}
	if record.ThisWeekFiled {
		t.Errorf("내지 않은 이번 주를 냈다고 합니다: %+v", record)
	}
	file(current.Format(dateLayout))
	after := read()
	if !after.ThisWeekFiled {
		t.Errorf("이번 주를 내고도 내지 않았다고 합니다: %+v", after)
	}
	if after.Streak != record.Streak {
		t.Errorf("아직 마감 전인 이번 주가 연속 기록을 %d에서 %d로 바꿨습니다", record.Streak, after.Streak)
	}

	// 빠뜨린 주가 있으면 끊기고, 그 주가 어디였는지 말합니다.
	newcomer := server.createUser("breaker", "USER", nil)
	brokenID, brokenVersion := server.draft(newcomer, week(1), "최근 한 주만")
	if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", brokenID), map[string]any{
		"summary": "최근 한 주만", "version": brokenVersion,
		"items": []map[string]any{{"category": "개발", "title": "한 일", "currentResult": "했습니다", "progress": 100}},
	}, newcomer); w.Code != http.StatusOK {
		t.Fatalf("write: %d %s", w.Code, w.Body.String())
	}
	if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", brokenID), nil, newcomer); w.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	// 계정이 만들어진 뒤의 주만 셉니다 — 3월에 들어온 사람이 1월부터 끊긴 기록을
	// 안고 시작하지는 않습니다.
	response := server.request(http.MethodGet, "/api/v1/me/participation", nil, newcomer)
	var payload struct {
		Data participationView `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	if payload.Data.Owed > 2 {
		t.Errorf("오늘 만든 계정이 %d주를 빚졌습니다: %+v", payload.Data.Owed, payload.Data)
	}
}
