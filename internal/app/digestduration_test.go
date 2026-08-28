package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The digest window says which tasks to consider — what has moved lately — and
// it was also cutting the history the durations are measured in. So the two
// sentences a reader ranks by reported the window back to them. Measured on a
// deployment: choosing 4, 8, 12 and 20 weeks made the same issue "4주째",
// "8주째", "12주째" and "20주째" in turn, while the 업무 추적 screen said 22
// weeks for the same task on the same day. The score is built from those
// numbers, so a 32-week stall ranked level with an 8-week one.
//
// guards: executiveDigest
func TestTheDigestDurationIsTheTasksNotTheWindows(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("기간 조직", "DURORG")
	leader := server.createUser("duration_leader", "ORG_MANAGER", &organisation)
	author := server.createUser("duration_author", "USER", &organisation)

	// The same issue, unmoved, every week for fifteen weeks up to the present.
	weeks := []string{}
	current := currentWeekStart(time.Now(), "MONDAY")
	for back := 14; back >= 0; back-- {
		weeks = append(weeks, current.AddDate(0, 0, -7*back).Format("2006-01-02"))
	}
	for _, week := range weeks {
		reportID, version := server.draft(author, week, "기간 시험")
		written := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID), map[string]any{
			"summary": "기간 시험", "version": version,
			"items": []map[string]any{{"category": "개발", "title": "오래 선 업무",
				"currentResult": "그대로", "nextPlan": "계속", "issue": "외부 승인 대기", "progress": 40}},
		}, author)
		if written.Code != http.StatusOK {
			t.Fatalf("write %s: %d %s", week, written.Code, written.Body.String())
		}
	}

	runFor := func(window int) (int, string) {
		t.Helper()
		response := server.request(http.MethodGet,
			fmt.Sprintf("/api/v1/digest?weeks=%d", window), nil, leader)
		if response.Code != http.StatusOK {
			t.Fatalf("digest at %d weeks: %d %s", window, response.Code, response.Body.String())
		}
		var body struct {
			Data struct {
				Entries []struct {
					Title   string `json:"title"`
					Grounds []struct {
						Kind string `json:"kind"`
						Text string `json:"text"`
					} `json:"grounds"`
				} `json:"entries"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode the digest: %v", err)
		}
		for _, entry := range body.Data.Entries {
			if entry.Title != "오래 선 업무" {
				continue
			}
			for _, ground := range entry.Grounds {
				if ground.Kind == "ISSUE" {
					return window, ground.Text
				}
			}
		}
		t.Fatalf("the task is not in the digest at %d weeks: %s", window, response.Body.String())
		return 0, ""
	}

	_, narrow := runFor(4)
	_, wide := runFor(12)
	if narrow != wide {
		t.Errorf("the duration changed with the window, so it describes the window:\n  4주: %s\n  12주: %s",
			narrow, wide)
	}
	// Fifteen weeks of the same issue, and the reader is shown a window of four.
	if !strings.Contains(narrow, "15주") {
		t.Errorf("the digest reports %q for an issue that has stood 15 weeks", narrow)
	}
}

// The pair sentence on 업무 인사이트 asks a reader to judge duplicated
// investment from "서로 다른 조직에서 %d주 동안 함께 진행 중", and the overlap
// was counted inside the loaded window. Measured on a deployment: the same pair
// read 4주 at weeks=4 and 52주 at weeks=104, with the default at 12. linkRank
// breaks ties between equally similar pairs by that number, so the ordering
// collapsed once they all reached the edge.
//
// guards: workGraph
func TestTheOverlapBetweenTwoTasksIsNotTheWindow(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("겹침 조직", "OVERORG")
	left := server.createChildOrganization("겹침 왼쪽", "OVERL", &organisation)
	right := server.createChildOrganization("겹침 오른쪽", "OVERR", &organisation)
	leader := server.createUser("overlap_leader", "ORG_MANAGER", &organisation)
	one := server.createUser("overlap_one", "USER", &left)
	two := server.createUser("overlap_two", "USER", &right)

	current := currentWeekStart(time.Now(), "MONDAY")
	for back := 11; back >= 0; back-- {
		week := current.AddDate(0, 0, -7*back).Format("2006-01-02")
		for _, who := range []*http.Cookie{one, two} {
			reportID, version := server.draft(who, week, "겹침 시험")
			written := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", reportID), map[string]any{
				"summary": "겹침 시험", "version": version,
				"items": []map[string]any{{"category": "개발", "title": "차세대 관제 통합 구축",
					"currentResult": "진행", "nextPlan": "계속", "issue": "", "progress": 40}},
			}, who)
			if written.Code != http.StatusOK {
				t.Fatalf("write %s: %d %s", week, written.Code, written.Body.String())
			}
		}
	}

	overlapAt := func(window int) int {
		t.Helper()
		response := server.request(http.MethodGet,
			fmt.Sprintf("/api/v1/insights/work-graph?weeks=%d", window), nil, leader)
		if response.Code != http.StatusOK {
			t.Fatalf("work graph at %d weeks: %d %s", window, response.Code, response.Body.String())
		}
		var body struct {
			Data struct {
				Duplicates []struct {
					OverlapWeeks int `json:"overlapWeeks"`
					Left         struct {
						Title string `json:"title"`
					} `json:"left"`
				} `json:"duplicates"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode the work graph: %v", err)
		}
		for _, link := range body.Data.Duplicates {
			if link.Left.Title == "차세대 관제 통합 구축" {
				return link.OverlapWeeks
			}
		}
		t.Fatalf("the pair is not in the graph at %d weeks", window)
		return 0
	}

	narrow, wide := overlapAt(4), overlapAt(52)
	if narrow != wide {
		t.Errorf("the overlap changed with the window, so it describes the window: 4주 창=%d, 52주 창=%d",
			narrow, wide)
	}
	if narrow != 12 {
		t.Errorf("the two ran together for 12 weeks, the graph says %d", narrow)
	}
}
