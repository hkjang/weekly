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

// guards: analyticsParticipation
//
// The trend carries two rates, and both are computed behind a guard against
// dividing by nobody. Turn either guard around and the rate is never computed:
// the screen reads a flat zero for every week while the counts beside it say
// otherwise. Nothing asserted either rate, so the analytics page could have
// reported total collapse and the suite would have agreed.
func TestTheParticipationRatesAgreeWithTheCountsBesideThem(t *testing.T) {
	server := newTestServer(t)
	organisation := server.createOrganization("비율 조직", "RATES")
	filer := server.createUser("rate_filer", "USER", &organisation)
	server.createUser("rate_silent", "USER", &organisation)

	week := mondayWeeksAgo(3)
	reportID := server.submitted(filer, week, week+" 주간보고")
	// Filed now, it would be three weeks late and the on-time rate would read
	// zero whether or not it is ever computed. Put the submission inside the
	// deadline so the second rate has something to be.
	if _, err := server.app.db.Exec(server.ctx(),
		`UPDATE weekly_reports SET submitted_at = ($2::date + interval '1 day') AT TIME ZONE 'Asia/Seoul'
			WHERE id = $1`, reportID, week); err != nil {
		t.Fatal(err)
	}

	w := server.request(http.MethodGet, "/api/v1/admin/analytics/participation", nil, server.admin)
	if w.Code != http.StatusOK {
		t.Fatalf("read participation: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Trend []struct {
				WeekStart      string  `json:"weekStart"`
				ActiveUsers    int     `json:"activeUsers"`
				Submitted      int     `json:"submitted"`
				OnTime         int     `json:"onTime"`
				SubmissionRate float64 `json:"submissionRate"`
				OnTimeRate     float64 `json:"onTimeRate"`
			} `json:"trend"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, row := range envelope.Data.Trend {
		if row.WeekStart != week {
			continue
		}
		found = true
		if row.Submitted == 0 || row.ActiveUsers == 0 {
			t.Fatalf("%s: 제출 %d건, 대상 %d명 — 픽스처가 그 주에 닿지 않았습니다",
				week, row.Submitted, row.ActiveUsers)
		}
		if row.SubmissionRate <= 0 {
			t.Errorf("%s: 제출 %d건 / 대상 %d명인데 제출률이 %.1f%% 입니다",
				week, row.Submitted, row.ActiveUsers, row.SubmissionRate)
		}
		if want := round1(float64(row.Submitted) * 100 / float64(row.ActiveUsers)); row.SubmissionRate != want {
			t.Errorf("%s: 제출률 %.1f%% 가 곁의 숫자(%d/%d = %.1f%%)와 어긋납니다",
				week, row.SubmissionRate, row.Submitted, row.ActiveUsers, want)
		}
		if row.OnTime > 0 && row.OnTimeRate <= 0 {
			t.Errorf("%s: 정시 %d건인데 정시율이 %.1f%% 입니다", week, row.OnTime, row.OnTimeRate)
		}
		if want := round1(float64(row.OnTime) * 100 / float64(row.Submitted)); row.OnTimeRate != want {
			t.Errorf("%s: 정시율 %.1f%% 가 곁의 숫자(%d/%d = %.1f%%)와 어긋납니다",
				week, row.OnTimeRate, row.OnTime, row.Submitted, want)
		}
	}
	if !found {
		t.Fatalf("추세에 %s 주가 없습니다", week)
	}
}
