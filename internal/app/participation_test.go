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
// 기록에 드는 주는 마감이 지난 주뿐이고, 마감은 달력이 아니라 규칙이 정합니다.
// 기본 규칙(시작일로부터 7일 뒤, 그날이 끝나는 자정)에서 지난주는 월요일
// 하루 내내 아직 열려 있어, 시험이 "지난주" 를 달력으로 세면 월요일마다
// 연속 2주로 읽었습니다. 그래서 주는 규칙이 닫은 가장 최근 주부터 세고, 같은
// 시나리오를 오늘이 무슨 요일이든 지난주가 열려 있는 규칙으로 한 번 더
// 돌립니다 — 월요일이 오기를 기다리지 않아도 그 경로가 시험에 듭니다.
//
// guards: myParticipation
func TestMyRecordCountsTheWeeksTheArrearsListCounts(t *testing.T) {
	t.Run("default deadline", func(t *testing.T) { participationRecordScenario(t, "") })
	// 13일 뒤 자정: 일요일 밤에도 지난주의 마감이 내일이라 열려 있습니다.
	t.Run("last week still open", func(t *testing.T) { participationRecordScenario(t, "13") })
}

// closedWeekStart is the most recent week whose deadline has passed under the
// rule — the newest week the record may count. Weeks are started on Monday
// in the service timezone, the way the test files them.
func closedWeekStart(now time.Time, rule deadlineRule) time.Time {
	week := currentWeekStart(now, "MONDAY")
	for week.AddDate(0, 0, rule.Days).Add(time.Duration(rule.Hour) * time.Hour).After(now) {
		week = week.AddDate(0, 0, -7)
	}
	return week
}

func participationRecordScenario(t *testing.T, deadlineDays string) {
	server := newTestServer(t)
	if deadlineDays != "" {
		if set := server.request(http.MethodPut, "/api/v1/admin/settings",
			map[string]any{"settings": map[string]string{"workflow.deadline_days": deadlineDays}}, server.admin); set.Code != http.StatusOK {
			t.Fatalf("마감 설정이 %d: %s", set.Code, set.Body.String())
		}
	}
	author := server.createUser("streaker", "USER", nil)

	location := server.app.serviceLocation(server.ctx())
	now := time.Now().In(location)
	current := currentWeekStart(now, "MONDAY")
	closed := closedWeekStart(now, server.app.deadlineRule(server.ctx()))
	if deadlineDays != "" && !closed.Before(current.AddDate(0, 0, -7)) {
		t.Fatalf("이 규칙에서는 지난주(%s)가 열려 있어야 하는데 닫힌 주가 %s 입니다", current.AddDate(0, 0, -7).Format(dateLayout), closed.Format(dateLayout))
	}
	// week(1) is the most recent closed week, week(2) the one before it.
	week := func(back int) string { return closed.AddDate(0, 0, -7*(back-1)).Format(dateLayout) }

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

	// 끊긴 기록. 최근 세 주를 이어 냈지만 그 앞에 빠뜨린 주가 있고, 그보다 더
	// 앞은 넉 주를 이어 냈습니다 — 연속은 3, 최고는 4, 마지막으로 빠뜨린 주는
	// 그 사이의 한 주입니다. 최고 기록이 없으면 11주를 이어 가다 한 번 놓친
	// 사람에게 남는 것이 "1주" 뿐이고, 그것은 스트릭이 격려가 아니라 벌이 되는
	// 지점입니다.
	keeper := server.createUser("recordkeeper", "USER", nil)
	fileFor := func(cookie *http.Cookie, weekStart string) {
		id, version := server.draft(cookie, weekStart, weekStart+" 보고")
		if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
			"summary": weekStart + " 보고", "version": version,
			"items": []map[string]any{{"category": "개발", "title": "한 일", "currentResult": "했습니다", "progress": 100}},
		}, cookie); w.Code != http.StatusOK {
			t.Fatalf("write %s: %d %s", weekStart, w.Code, w.Body.String())
		}
		if w := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id), nil, cookie); w.Code != http.StatusOK {
			t.Fatalf("submit %s: %d %s", weekStart, w.Code, w.Body.String())
		}
	}
	for _, back := range []int{8, 7, 6, 5, 3, 2, 1} {
		fileFor(keeper, week(back))
	}
	keeperResponse := server.request(http.MethodGet, "/api/v1/me/participation", nil, keeper)
	var keeperPayload struct {
		Data participationView `json:"data"`
	}
	if err := json.Unmarshal(keeperResponse.Body.Bytes(), &keeperPayload); err != nil {
		t.Fatalf("decode %s: %v", keeperResponse.Body.String(), err)
	}
	broken := keeperPayload.Data
	if broken.Streak != 3 {
		t.Errorf("연속 %d주, want 3 (%+v)", broken.Streak, broken)
	}
	if broken.Best != 4 {
		t.Errorf("최고 %d주, want 4 — 끊기기 전의 기록이 남지 않습니다 (%+v)", broken.Best, broken)
	}
	if broken.LastMissed != week(4) {
		t.Errorf("마지막으로 빠뜨린 주가 %q, want %q — 가장 최근이 아니라 가장 오래된 것을 말하면 이어 쓸 곳을 잘못 가리킵니다",
			broken.LastMissed, week(4))
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
