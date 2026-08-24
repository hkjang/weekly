package app

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// v0.71 pinned the deadline instant so Go and SQL agree on when a week closes.
// It never checked the thing that instant is for: that a report handed in
// before it counts as on time and one handed in after does not.
//
// scripts/mutation-check.py: reversing either comparison in the participation
// query swaps the two counts, and the whole suite passes. Everyone would read
// as punctual, or nobody would.
//
// guards: analyticsParticipation
func TestOnTimeAndLateAreNotTheSameColumn(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("punctual", "USER", nil)

	// A week far enough back that its deadline has certainly passed.
	monday := currentWeekStart(time.Now(), "MONDAY").AddDate(0, 0, -7*4)
	week := monday.Format("2006-01-02")
	reportID := server.submitted(author, week, "정시성 시험")

	// The deadline is an instant in the service timezone. Computing it in UTC
	// puts it nine hours late and turns an early submission into a late one —
	// which is exactly the mistake the rule exists to prevent, so the test has
	// to make it the same way the query does.
	location, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	deadline := deadlineRule{Days: 7, Hour: 24}.instant(monday, location)

	measure := func(when time.Time) (onTime, late int) {
		t.Helper()
		if _, err := server.app.db.Exec(server.ctx(),
			`UPDATE weekly_reports SET submitted_at=$1 WHERE id=$2`, when, reportID); err != nil {
			t.Fatal(err)
		}
		w := server.request(http.MethodGet, "/api/v1/admin/analytics/participation?weeks=12", nil, server.admin)
		if w.Code != http.StatusOK {
			t.Fatalf("participation: %d %s", w.Code, w.Body.String())
		}
		trend, _ := decodeData(t, w)["trend"].([]any)
		for _, entry := range trend {
			row, _ := entry.(map[string]any)
			if row["weekStart"] != week {
				continue
			}
			on, _ := row["onTime"].(float64)
			delayed, _ := row["late"].(float64)
			return int(on), int(delayed)
		}
		t.Fatalf("week %s is not in the trend at all", week)
		return 0, 0
	}

	if on, late := measure(deadline.Add(-time.Hour)); on != 1 || late != 0 {
		t.Errorf("submitted an hour before the deadline: onTime=%d late=%d, want 1 and 0", on, late)
	}
	if on, late := measure(deadline.Add(time.Hour)); on != 0 || late != 1 {
		t.Errorf("submitted an hour after the deadline: onTime=%d late=%d, want 0 and 1", on, late)
	}
	// The boundary itself. "까지" includes the instant named, so a report handed
	// in exactly then is late by the rule as written — the point is that one
	// side owns it and the counts still add up.
	if on, late := measure(deadline); on+late != 1 {
		t.Errorf("submitted exactly at the deadline: onTime=%d late=%d — one report was counted %d times", on, late, on+late)
	}
}

// guards: searchWorkItems
//
// A one-character query matches most of a corpus, so the handler answers it with
// an empty result rather than scanning. It is not an error — the screen sends a
// request on every keystroke — but it must not be a search either.
//
// Reversing the comparison makes every real query answer empty and every short
// one scan, and nothing noticed.
func TestWorkItemSearchDoesNotScanOnOneCharacter(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("short_query", "USER", nil)
	server.submitted(author, "2026-08-24", "짧은 질의 시험")

	// Positive control: a real query reaches the search and reports its terms.
	full := server.request(http.MethodGet, "/api/v1/work-items/search?q="+url.QueryEscape("회선"), nil, author)
	if full.Code != http.StatusOK {
		t.Fatalf("a two-character query was refused: %d %s", full.Code, full.Body.String())
	}
	terms, _ := decodeData(t, full)["terms"].([]any)
	if len(terms) == 0 {
		t.Fatalf("a two-character query produced no terms, so the floor may be refusing it: %s", full.Body.String())
	}

	for _, query := range []string{"", "회", " "} {
		w := server.request(http.MethodGet, "/api/v1/work-items/search?q="+url.QueryEscape(query), nil, author)
		if w.Code != http.StatusOK {
			t.Errorf("query %q answered %d; a short query is not an error", query, w.Code)
			continue
		}
		data := decodeData(t, w)
		short, _ := data["terms"].([]any)
		hits, _ := data["hits"].([]any)
		if len(short) != 0 || len(hits) != 0 {
			t.Errorf("query %q was searched anyway: terms=%d hits=%d", query, len(short), len(hits))
		}
	}
}
