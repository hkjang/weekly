package app

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Changing which weekday a reporting week begins on.
//
// It reads like a formatting preference and behaves like a data migration.
// Every stored report carries the week start it was written under, and every
// figure derived from reports is bucketed on that grid, so moving the grid
// leaves the existing reports off it. Measured on a live server: after moving
// Monday to Wednesday, the author's current report disappeared and they could
// file a second one for the same days, participation analytics read zero for
// every earlier week, and the period rollup counted one week of work as two.
//
// Refusing outright would be wrong — a company that genuinely reports on
// Wednesdays has to be able to say so — so the change is allowed once the
// administrator has been told what it will do.

// weekCoveringDays is "does this report cover the seven days beginning on that
// date", as SQL over a weekly_reports alias and one date placeholder.
//
// The unique key is (user, week_start), so an exact date match answers the
// question only while the grid has never moved. After it moves, the report an
// author filed on the old grid covers the same days under a different date —
// which is why weekIsFree refuses to let them file a second one. Anything that
// asks "has this person reported for this week" and answers with an exact match
// disagrees with that refusal for one transition week: the team recommendation
// mail asked a whole team for a report the product would not let them write.
func weekCoveringDays(alias string, placeholder int) string {
	day := "$" + strconv.Itoa(placeholder) + "::date"
	return alias + ".week_start <= " + day + " + 6 AND " + alias + ".week_start + 6 >= " + day
}

// weekdayNumbers maps the setting's names to PostgreSQL's day-of-week numbering.
var weekdayNumbers = map[string]int{
	"SUNDAY": 0, "MONDAY": 1, "TUESDAY": 2, "WEDNESDAY": 3,
	"THURSDAY": 4, "FRIDAY": 5, "SATURDAY": 6,
}

// misalignedReports counts stored reports that would not sit on the new grid.
//
// Counted in the database rather than by reading every row into Go. week_start
// is a date, so its day of the week is the same in any timezone and PostgreSQL
// can answer with one aggregate instead of streaming a multi-year table through
// an administrator's request.
func (a *App) misalignedReports(r *http.Request, weekday string) (int, string, error) {
	number, known := weekdayNumbers[strings.ToUpper(strings.TrimSpace(weekday))]
	if !known {
		return 0, "", errNotFound
	}
	var misaligned int
	var earliest *time.Time
	err := a.db.QueryRow(r.Context(), `SELECT
		count(*) FILTER (WHERE extract(dow from week_start) <> $1),
		min(week_start) FILTER (WHERE extract(dow from week_start) <> $1)
		FROM weekly_reports`, number).Scan(&misaligned, &earliest)
	if err != nil {
		return 0, "", err
	}
	if earliest == nil {
		return misaligned, "", nil
	}
	return misaligned, earliest.Format("2006-01-02"), nil
}

// weekStartChangeAllowed reports whether the change may proceed, writing the
// response itself when it may not.
func (a *App) weekStartChangeAllowed(w http.ResponseWriter, r *http.Request, weekday string, confirmed []string) bool {
	current := a.setting(r.Context(), "workflow.week_start", "MONDAY")
	if strings.EqualFold(current, weekday) {
		return true
	}
	misaligned, earliest, err := a.misalignedReports(r, weekday)
	if err != nil {
		a.logger.Error("check week start change", "error", err)
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "기존 보고서를 확인할 수 없습니다.")
		return false
	}
	if misaligned == 0 || slices.Contains(confirmed, "workflow.week_start") {
		if misaligned > 0 {
			a.logger.Warn("week start changed with existing reports off the new grid",
				"weekday", weekday, "misaligned", misaligned, "earliest", earliest)
		}
		return true
	}
	writeError(w, http.StatusConflict, "WEEK_START_NEEDS_CONFIRMATION", weekStartWarning(misaligned, earliest))
	return false
}

func weekStartWarning(misaligned int, earliest string) string {
	var message strings.Builder
	message.WriteString("이미 저장된 보고서 ")
	message.WriteString(strconv.Itoa(misaligned))
	message.WriteString("건이 새 주차 격자에서 벗어납니다")
	if earliest != "" {
		message.WriteString(" (가장 이른 주차 ")
		message.WriteString(earliest)
		message.WriteString(")")
	}
	message.WriteString(". 바꾸면 그 보고서들은 제출률·정시율 같은 참여 분석에서 빠지고, 기간 보고에서 한 주가 두 주로 집계될 수 있습니다. 전환 주에는 기존 보고서와 기간이 겹쳐 새 보고서를 만들 수 없으므로, 작성자는 기존 보고서를 열어 이어 써야 합니다. 그래도 진행하려면 확인이 필요합니다.")
	return message.String()
}
