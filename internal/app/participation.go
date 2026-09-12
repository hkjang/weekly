package app

import (
	"net/http"
	"time"
)

// 내가 몇 주 연속으로 냈는가.
//
// The habit is the product. 참여 분석 has always been able to tell an
// administrator who is behind, and the person themselves — the one who can
// actually do something about it this week — had no way to see their own
// record at all. A streak is the cheapest honest form of that: it is built
// from the same weeks the arrears list counts, so the number a person sees is
// the number their organisation sees.
//
// Two rules keep it honest, and both are the difference between a streak and a
// scoreboard:
//
//   - The open week is never counted against anybody. A week becomes part of
//     the record only after its deadline, so a streak does not break every
//     Monday morning and mend itself on Friday. What this week is doing is
//     reported separately, as a fact rather than as a score.
//   - A week counts only from when the person was here. Somebody who joined in
//     March does not start with a broken record stretching back to January.
type participationView struct {
	// Streak is consecutive closed weeks filed, counting back from the most
	// recent closed week.
	Streak int `json:"streak"`
	// Best is the longest run in the window, which is what makes a streak worth
	// keeping: a person who lost one at 11 weeks can see they once did 11.
	Best int `json:"best"`
	// Owed and Filed describe the whole window, because a streak alone flatters
	// somebody who filed nothing for a year and then two weeks in a row.
	Owed   int `json:"owed"`
	Filed  int `json:"filed"`
	Window int `json:"window"`
	// ThisWeek is the open week: filed, or still to do. Not part of the streak.
	ThisWeekStart string `json:"thisWeekStart"`
	ThisWeekFiled bool   `json:"thisWeekFiled"`
	// LastMissed is the most recent closed week with nothing filed, empty when
	// there is none in the window. It is what a person needs to open next.
	LastMissed string `json:"lastMissed,omitempty"`
}

// participationWindowWeeks is how far back the record is read. A year is the
// longest period any other screen in the product reports on, and a streak
// longer than that is a number nobody can check against anything.
const participationWindowWeeks = 52

func (a *App) myParticipation(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r.Context())
	location := a.serviceLocation(r.Context())
	weekStart := a.setting(r.Context(), "workflow.week_start", "MONDAY")
	now := time.Now().In(location)
	current := currentWeekStart(now, weekStart)
	deadline := a.deadlineRule(r.Context())

	view := participationView{Window: participationWindowWeeks, ThisWeekStart: current.Format(dateLayout)}
	from := current.AddDate(0, 0, -7*participationWindowWeeks)

	// Every week of the grid this person was owed, newest first, with whether
	// they filed for the days it covers. The report is looked for by the days
	// rather than by the date, the way every other screen counts it: after the
	// administrator moves the week start, an exact match would read a filed
	// week as missing and break the streak of everybody in the service at once.
	rows, err := a.db.Query(r.Context(), `
		SELECT week.day::date,
		       EXISTS(SELECT 1 FROM weekly_reports r
		              WHERE r.user_id=u.id AND r.status <> 'DRAFT'
		                AND `+weekCoveringDaysOf("r", "week.day::date")+`)
		FROM users u
		CROSS JOIN generate_series($6::date, $2::date, interval '7 day') AS week(day)
		WHERE u.id=$1 AND week.day::date >= `+expectedFromWeek+` AND `+deadlinePassed+`
		ORDER BY week.day DESC`,
		// $3·$4·$5 are the timezone, days and hour the shared deadline fragments
		// name. They are positional because those fragments are shared with the
		// two administrator screens that count the same weeks.
		p.ID, current, deadline.Timezone, deadline.Days, deadline.Hour, from)
	if err != nil {
		a.logger.Error("participation", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "제출 기록을 조회할 수 없습니다.")
		return
	}
	defer rows.Close()
	running, counting := 0, true
	for rows.Next() {
		var day time.Time
		var filed bool
		if err := rows.Scan(&day, &filed); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "제출 기록을 읽을 수 없습니다.")
			return
		}
		view.Owed++
		if filed {
			view.Filed++
			running++
			if running > view.Best {
				view.Best = running
			}
			if counting {
				view.Streak = running
			}
			continue
		}
		running = 0
		counting = false
		if view.LastMissed == "" {
			view.LastMissed = day.Format(dateLayout)
		}
	}
	if err := rows.Err(); err != nil {
		a.logger.Error("participation read", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "제출 기록을 읽을 수 없습니다.")
		return
	}

	// The open week, reported as what it is. Counting it would make the streak
	// break every Monday; hiding it would leave the reader without the one week
	// they can still act on.
	if err := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM weekly_reports r
		WHERE r.user_id=$1 AND r.status <> 'DRAFT' AND `+weekCoveringDays("r", 2)+`)`,
		p.ID, current).Scan(&view.ThisWeekFiled); err != nil {
		a.logger.Error("participation current", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "제출 기록을 조회할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, view)
}
