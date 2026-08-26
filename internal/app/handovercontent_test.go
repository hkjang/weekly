package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// TestNobodyReadsSomebodyElsesHandover checks the door: the owner is let in and
// an outsider is not. Nothing checked what is behind it, so the whole body of
// handover() was unguarded — including the line that keeps the list to the
// leaver's own work. Inverting it hands the reader everybody else's tasks
// instead, and the suite stayed green.

type handoverBody struct {
	Items []struct {
		Title    string `json:"title"`
		AgeWeeks int    `json:"ageWeeks"`
		Stalled  bool   `json:"stalled"`
		NextPlan string `json:"nextPlan"`
	} `json:"items"`
}

func readHandover(t *testing.T, server *testServer, reader *http.Cookie, userID int64) handoverBody {
	t.Helper()
	w := server.request(http.MethodGet, fmt.Sprintf("/api/v1/handover?userId=%d", userID), nil, reader)
	if w.Code != http.StatusOK {
		t.Fatalf("read a handover: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data handoverBody `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

// guards: handover
func TestAHandoverCarriesTheLeaversOwnWorkAndNobodyElses(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("인수인계 조직", "HANDCONT")
	leaving := server.createUser("hand_leaving", "USER", &organisation)
	staying := server.createUser("hand_staying", "USER", &organisation)

	server.weekWithIssue(leaving, "2026-08-17", "떠나는 사람의 업무", "미해결 이슈", 40)
	server.weekWithIssue(staying, "2026-08-17", "남는 사람의 업무", "", 60)

	leavingID := server.userIDOf(server.lastCreatedUsername("hand_leaving"))
	view := readHandover(t, server, leaving, leavingID)

	titles := map[string]bool{}
	for _, item := range view.Items {
		titles[item.Title] = true
	}
	if !titles["떠나는 사람의 업무"] {
		t.Errorf("the leaver's own task is missing from their handover: %+v", view.Items)
	}
	if titles["남는 사람의 업무"] {
		t.Errorf("the handover carries somebody else's task: %+v", view.Items)
	}

	// The plan the last report left behind is what the new owner picks up from.
	for _, item := range view.Items {
		if item.Title == "떠나는 사람의 업무" && item.NextPlan == "" {
			t.Error("the handover dropped the plan the last report left behind")
		}
	}
}

// guards: handover
//
// Two keys, and they have to be checked separately. Every fixture that puts a
// stalled task against a moving one settles on the first key alone, so the age
// comparison can be reversed and the order still comes out right — which is
// what happened: the reader would meet the newest task first, and the one that
// has been waiting longest last.
func TestAHandoverPutsWhatIsStuckFirstAndThenTheOldest(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("hand_order", "USER", nil)

	// Both of these move every time they are reported, so neither is stalled
	// and the order rests entirely on age.
	server.weekWithIssue(owner, "2026-06-01", "오래된 업무", "", 10)
	id, version := server.draft(owner, "2026-08-17", "이번 주")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "이번 주", "version": version,
		"items": []map[string]any{
			{"category": "개발", "title": "오래된 업무", "currentResult": "나아갔습니다", "nextPlan": "계속", "issue": "", "progress": 60},
			{"category": "개발", "title": "막 시작한 업무", "currentResult": "시작", "nextPlan": "이어서", "issue": "", "progress": 5},
		},
	}, owner)
	if filled.Code != http.StatusOK {
		t.Fatalf("fill: %d %s", filled.Code, filled.Body.String())
	}
	next := decodeData(t, filled)["version"].(float64)
	if handed := server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id),
		map[string]any{"version": int(next)}, owner); handed.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", handed.Code, handed.Body.String())
	}

	view := readHandover(t, server, owner, server.userIDOf(server.lastCreatedUsername("hand_order")))
	if len(view.Items) < 2 {
		t.Fatalf("expected both tasks, got %+v", view.Items)
	}
	if view.Items[0].Stalled || view.Items[1].Stalled {
		t.Fatalf("both tasks should be moving, so age decides: %+v", view.Items)
	}
	if view.Items[0].Title != "오래된 업무" {
		t.Errorf("the task waiting longest should be read first, got %q", view.Items[0].Title)
	}
	if view.Items[0].AgeWeeks <= view.Items[1].AgeWeeks {
		t.Errorf("ageWeeks = %d then %d — the older one is not first",
			view.Items[0].AgeWeeks, view.Items[1].AgeWeeks)
	}
}

// guards: handover
func TestAHandoverPutsAStuckTaskAheadOfAMovingOne(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("hand_stuck", "USER", nil)

	// The stalled one is the newer of the two, so only the stall key can put
	// it first.
	server.weekWithIssue(owner, "2026-06-01", "움직이는 오래된 업무", "", 10)
	id, version := server.draft(owner, "2026-08-17", "이번 주")
	filled := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
		"summary": "이번 주", "version": version,
		"items": []map[string]any{
			{"category": "개발", "title": "움직이는 오래된 업무", "currentResult": "나아갔습니다", "nextPlan": "계속", "issue": "", "progress": 70},
			{"category": "개발", "title": "멈춘 업무", "currentResult": "그대로", "nextPlan": "계속", "issue": "", "progress": 20},
		},
	}, owner)
	if filled.Code != http.StatusOK {
		t.Fatalf("fill: %d %s", filled.Code, filled.Body.String())
	}
	next := decodeData(t, filled)["version"].(float64)
	server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id), map[string]any{"version": int(next)}, owner)

	id2, version2 := server.draft(owner, "2026-08-24", "다음 주")
	filled2 := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id2), map[string]any{
		"summary": "다음 주", "version": version2,
		"items": []map[string]any{
			{"category": "개발", "title": "움직이는 오래된 업무", "currentResult": "또 나아갔습니다", "nextPlan": "계속", "issue": "", "progress": 90},
			{"category": "개발", "title": "멈춘 업무", "currentResult": "그대로", "nextPlan": "계속", "issue": "", "progress": 20},
		},
	}, owner)
	if filled2.Code != http.StatusOK {
		t.Fatalf("fill again: %d %s", filled2.Code, filled2.Body.String())
	}
	next2 := decodeData(t, filled2)["version"].(float64)
	server.request(http.MethodPost, fmt.Sprintf("/api/v1/reports/%d/submit", id2), map[string]any{"version": int(next2)}, owner)

	view := readHandover(t, server, owner, server.userIDOf(server.lastCreatedUsername("hand_stuck")))
	if len(view.Items) < 2 {
		t.Fatalf("expected both tasks, got %+v", view.Items)
	}
	if view.Items[0].Title != "멈춘 업무" {
		t.Errorf("what is stuck should be read first even though it is newer, got %q", view.Items[0].Title)
	}
}

// guards: handover
func TestAHandoverForAnIdentifierThatIsNotOneIsRefused(t *testing.T) {
	server := newTestServer(t)
	owner := server.createUser("hand_badid", "USER", nil)

	for _, id := range []string{"0", "-3", "abc"} {
		w := server.request(http.MethodGet, "/api/v1/handover?userId="+id, nil, owner)
		if w.Code != http.StatusBadRequest || errorCode(w) != "INVALID_USER" {
			t.Errorf("담당자 식별자 %q: %d %s", id, w.Code, w.Body.String())
		}
	}
}

// guards: handover
//
// The decisions are fetched for a separately built list of the leaver's task
// ids, not for the entries the loop below emits. Point that list at anybody
// else and every entry comes back with an empty decision list — the one part
// of a handover that says why the work went the way it did — while the tasks,
// the progress and the plans all still look right.
func TestAHandoverCarriesTheDecisionsRecordedOnTheLeaversWork(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("결정 인수인계", "HANDDEC")
	leaving := server.createUser("hand_dec", "USER", &organisation)

	workItemID := workItemOf(t, server, leaving, "2026-08-24", "결정이 붙은 업무")
	recordDecision(t, server, leaving, workItemID, "자체 구현으로 가기로 했다")

	w := server.request(http.MethodGet,
		fmt.Sprintf("/api/v1/handover?userId=%d", server.userIDOf(server.lastCreatedUsername("hand_dec"))), nil, leaving)
	if w.Code != http.StatusOK {
		t.Fatalf("read the handover: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Items []struct {
				WorkItemID int64 `json:"workItemId"`
				Decisions  []struct {
					Title string `json:"title"`
				} `json:"decisions"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, item := range envelope.Data.Items {
		if item.WorkItemID != workItemID {
			continue
		}
		for _, decision := range item.Decisions {
			if decision.Title == "자체 구현으로 가기로 했다" {
				return
			}
		}
		t.Fatalf("the task is in the handover but the decision recorded on it is not: %s", w.Body.String())
	}
	t.Fatalf("the task carrying the decision is missing from the handover: %s", w.Body.String())
}
