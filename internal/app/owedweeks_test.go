package app

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// The week somebody joins is the first week they owe a report for. weekIsOwed
// says so with `>=`, and nothing ran that boundary: every participation test
// has people who joined long before the window it looks at, where `>` and `>=`
// give the same answer. Shift it by one operator and each person's arrears are
// short by exactly one week — the week they arrived, which is the one a new
// starter is most likely to miss.

// guards: analyticsParticipation
func TestTheWeekSomebodyJoinedCountsAgainstThem(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("참여 조직", "OWEDWEEKS")
	server.createUser("owed_newcomer", "USER", &organisation)
	username := server.lastCreatedUsername("owed_newcomer")
	id := server.userIDOf(username)

	// Joined four Mondays ago and has filed nothing since.
	joined := mondayWeeksAgo(4)
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE users SET created_at = $2::date AT TIME ZONE 'Asia/Seoul' WHERE id = $1`, id, joined); err != nil {
		t.Fatal(err)
	}

	// The arrears are read absolutely, against the closed weeks the same
	// response reports. Comparing two readings instead — joined here, joined a
	// week later — cancels the boundary out: both move together and the
	// difference stays one however the comparison is written. That is the trap
	// this whole line of work keeps finding, and the first draft of this test
	// walked into it.
	missed, closedSince := participationFor(t, server, username, joined)
	if closedSince == 0 {
		t.Fatalf("no closed week since %s to count", joined)
	}
	if missed != closedSince {
		t.Errorf("%s 에 들어와 한 건도 내지 않았으면 마감된 %d주 전부가 밀린 것인데, %d주로 셉니다",
			joined, closedSince, missed)
	}
}

func mondayWeeksAgo(weeks int) string {
	now := time.Now()
	offset := (int(now.Weekday()) + 6) % 7 // Monday is 0
	monday := now.AddDate(0, 0, -offset-7*weeks)
	return monday.Format("2006-01-02")
}

// participationFor returns the arrears the screen shows for one person, and
// how many weeks since joined the same response counts as closed.
func participationFor(t *testing.T, server *testServer, username, joined string) (int, int) {
	t.Helper()
	w := server.request(http.MethodGet, "/api/v1/admin/analytics/participation", nil, server.admin)
	if w.Code != http.StatusOK {
		t.Fatalf("read participation: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Missing []struct {
				Username    string `json:"username"`
				MissedWeeks int    `json:"missedWeeks"`
			} `json:"missing"`
			Trend []struct {
				WeekStart string `json:"weekStart"`
				Open      bool   `json:"open"`
			} `json:"trend"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	closed := 0
	for _, week := range envelope.Data.Trend {
		if !week.Open && week.WeekStart >= joined {
			closed++
		}
	}
	for _, reporter := range envelope.Data.Missing {
		if reporter.Username == username {
			return reporter.MissedWeeks, closed
		}
	}
	return 0, closed
}
