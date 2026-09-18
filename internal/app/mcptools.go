package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The tools in this file exist because of what the surface could not answer.
//
// Audited by asking it the questions a department actually asks and reading
// what came back: 이번 주 누가 안 냈나 — a count, never a name. 그 사람이 무슨
// 일을 했나 — a search result carrying a one line summary and no body, with no
// way to open the report it named. 이번 달 일정은 — nothing at all, because the
// 업무 상황판 shipped four versions after the MCP surface did and nobody joined
// them up.
//
// A tool that is missing is not a silent failure: it is a model answering from
// the three tools it has, confidently, about a question none of them covers.

// mcpMissingLimit is how many names one 미제출자 answer carries. Same hundred
// as everywhere else on this surface, with the true count beside it.
const mcpMissingLimit = 100

// mcpScheduleLimit is how many board rows one answer carries.
const mcpScheduleLimit = 100

type mcpMissingPerson struct {
	UserID       int64  `json:"userId"`
	Username     string `json:"username"`
	DisplayName  string `json:"displayName"`
	Organization string `json:"organization,omitempty"`
	// LastWeek is the last week this person filed anything at all, or empty if
	// they never have. A name with no history beside it reads as neglect; more
	// often it is somebody who joined last month.
	LastWeek string `json:"lastWeek,omitempty"`
}

// mcpMissingSubmitters is who owes a report for these seven days.
//
// Counted the way the arrears list counts: a DRAFT is not a submission, and the
// report is looked for by the days it covers rather than the date it is filed
// under — otherwise the week the administrator moves the grid names everybody,
// for a week in which everybody reported.
//
// Deliberately not offered to members. The counts already are: a member reads
// their organisation's 제출률 on the screen and through the overview tool. A
// list of names is a different thing to hand somebody, and the product has
// always kept 미제출 명단 with the people who do something about it.
func (a *App) mcpMissingSubmitters(ctx context.Context, p *principal, week string) (map[string]any, error) {
	where := ""
	args := []any{week}
	if p.Role != "ADMIN" {
		if p.OrganizationID == nil {
			return map[string]any{"weekStart": week, "activeUsers": 0, "submitted": 0,
				"missingTotal": 0, "people": []mcpMissingPerson{},
				"note": "이 계정은 조직에 속해 있지 않아 집계할 대상이 없습니다."}, nil
		}
		args = append(args, *p.OrganizationID)
		where = " AND u.organization_id IN " + orgSubtree(len(args))
	}
	filed := ` EXISTS(SELECT 1 FROM weekly_reports r
		WHERE r.user_id=u.id AND r.status<>'DRAFT' AND ` + weekCoveringDays("r", 1) + `)`

	result := map[string]any{"weekStart": week}
	var active, submitted int
	if err := a.db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE`+filed+`)
		FROM users u WHERE u.active=true`+where, args...).Scan(&active, &submitted); err != nil {
		return nil, err
	}
	result["activeUsers"] = active
	result["submitted"] = submitted
	result["missingTotal"] = active - submitted
	if active > 0 {
		result["submissionRate"] = round1(float64(submitted) * 100 / float64(active))
	} else {
		result["submissionRate"] = float64(0)
	}

	args = append(args, mcpMissingLimit)
	rows, err := a.db.Query(ctx, `
		SELECT u.id, u.username, u.display_name, coalesce(o.name,''),
		       coalesce((SELECT max(r.week_start)::text FROM weekly_reports r
		                  WHERE r.user_id=u.id AND r.status<>'DRAFT'), '')
		FROM users u LEFT JOIN organizations o ON o.id=u.organization_id
		WHERE u.active=true`+where+` AND NOT`+filed+`
		ORDER BY coalesce(o.name,''), u.display_name, u.id
		LIMIT $`+asString(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	people := []mcpMissingPerson{}
	for rows.Next() {
		var person mcpMissingPerson
		if err := rows.Scan(&person.UserID, &person.Username, &person.DisplayName,
			&person.Organization, &person.LastWeek); err != nil {
			return nil, err
		}
		people = append(people, person)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result["people"] = people
	if missing := active - submitted; missing > len(people) {
		result["note"] = fmt.Sprintf(
			"미제출 %d명 중 %d명만 이름을 담았습니다. 이 목록을 전체로 보고 요약하지 마세요.",
			missing, len(people))
	}
	return result, nil
}

// mcpReportDetail is one report's body.
//
// The search tool finds reports and returns a summary line for each; until now
// there was no second step. A model asked what somebody did could name the
// report and quote its one sentence, and nothing on the surface could open it —
// so the answer it gave was the summary, presented as the week's work.
//
// Permission is the screens' own: canViewReport. A report the caller may not
// read and a report that does not exist are refused in the same words, because
// telling them apart is telling the caller that a report exists.
func (a *App) mcpReportDetail(ctx context.Context, p *principal, id int64) (map[string]any, error) {
	if !a.canViewReport(ctx, p, id) {
		return nil, errMCPReportUnreachable
	}
	report, err := a.loadReport(ctx, id)
	// Reachable only by a race: canViewReport has already read the row, so a
	// report that is gone by the time this line runs was deleted between the
	// two queries. Answering that with "볼 수 없거나 존재하지 않습니다" is
	// exactly right, and it keeps a benign race out of the incident log.
	if errors.Is(err, errNotFound) {
		return nil, errMCPReportUnreachable
	}
	// A database that is down is not a report that does not exist. Folding the
	// two together tells the caller a false thing it will repeat — 그 보고서는
	// 없습니다 — and keeps the real failure out of the log, which is the same
	// trade the screens already refuse to make.
	if err != nil {
		return nil, err
	}
	data := map[string]any{
		"id": report.ID, "userId": report.UserID, "username": report.Username,
		"displayName": report.DisplayName, "weekStart": report.WeekStart,
		"status": report.Status, "sourceType": report.SourceType,
		"summary": report.Summary, "version": report.Version,
		"submittedAt": report.SubmittedAt, "reviewedAt": report.ReviewedAt,
		"reviewedBy": report.ReviewedBy, "updatedAt": report.UpdatedAt,
		"items": report.Items, "comments": report.Comments,
		"itemCount": len(report.Items),
	}
	// The materials a leader composed from are whole reports of their own, and
	// a leader's report can carry thirty of them. They are named rather than
	// inlined: the caller learns the report was written from other people's and
	// can open any of them with this same tool, instead of being handed the
	// department's entire week to answer a question about one person's.
	if len(report.IncludedMaterials) > 0 {
		included := []map[string]any{}
		for _, material := range report.IncludedMaterials {
			included = append(included, map[string]any{
				"userId": material.UserID, "displayName": material.DisplayName,
				"organizationName": material.OrganizationName,
				"reportId":         material.ReportID, "status": material.Status,
				"summary": material.Summary, "itemCount": len(material.Items),
			})
		}
		data["includedMaterials"] = included
		data["includedMaterialsNote"] = "이 보고서는 팀원 보고서를 재료로 삼아 작성되었습니다. 각 재료의 본문은 reportId 로 weekly_report_detail 을 다시 호출해 읽으세요."
	}
	return data, nil
}

// mcpRefusal is a failure the caller caused and can fix, as distinct from one
// the service caused and the caller can only report.
//
// The surface had one error path: log it and say "분석 중 오류가 발생했습니다".
// That sentence is correct for a database that is down and useless for a
// mistyped argument, and the log line it wrote beside it told whoever reads
// logs that the service had failed when it had not. A refusal is answered in
// the caller's own terms and never logged as an incident.
type mcpRefusal struct{ message string }

func (refusal mcpRefusal) Error() string { return refusal.message }

func mcpRefuse(format string, args ...any) error {
	return mcpRefusal{message: fmt.Sprintf(format, args...)}
}

// errMCPReportUnreachable is "no such report, or not yours" as one answer.
var errMCPReportUnreachable = mcpRefuse("그 보고서를 볼 수 없거나 존재하지 않습니다.")

// mcpScheduleTasks is the 업무 상황판 as a tool result.
//
// The board is the newest surface in the product and was invisible here, so a
// model asked what the department has on this month answered from weekly
// reports — which describe the week that happened, not the work that is
// planned, overdue or due on Friday.
func (a *App) mcpScheduleTasks(ctx context.Context, p *principal, from, to time.Time, team bool, state, today string) (map[string]any, error) {
	tasks, err := a.scheduleTasksBetween(ctx, p, from, to, team)
	if err != nil {
		return nil, err
	}
	kept := []scheduleTaskView{}
	for _, task := range tasks {
		switch state {
		case "OPEN":
			if task.Done {
				continue
			}
		case "DONE":
			if !task.Done {
				continue
			}
		case "OVERDUE":
			if task.Done || task.EndDate >= today {
				continue
			}
		case "URGENT":
			if task.Done || task.Priority != priorityUrgent {
				continue
			}
		}
		kept = append(kept, task)
	}
	// Counted over everything the window holds, not over the page returned, so
	// the strip and the rows cannot disagree the way a screen's never can.
	summary := summariseSchedule(kept, today)
	total := len(kept)
	if len(kept) > mcpScheduleLimit {
		kept = kept[:mcpScheduleLimit]
	}
	rows := []map[string]any{}
	for _, task := range kept {
		row := map[string]any{
			"id": task.ID, "title": task.Title, "assignee": task.DisplayName,
			"userId": task.UserID, "startDate": task.StartDate, "endDate": task.EndDate,
			"done": task.Done, "priority": task.Priority,
			"priorityLabel": priorityLabel(task.Priority),
		}
		for key, value := range map[string]string{
			"category": task.Category, "organization": task.OrganizationName,
			"note": task.Note, "srId": task.SRID, "srUrl": task.SRURL,
			"doneBy": task.DoneByName,
		} {
			if value != "" {
				row[key] = value
			}
		}
		if !task.Done && task.EndDate < today {
			row["overdue"] = true
		}
		rows = append(rows, row)
	}
	data := map[string]any{
		"from": from.Format(dateLayout), "to": to.Format(dateLayout),
		"today": today, "scope": scopeSelf, "state": state,
		"tasks": rows, "total": total, "summary": summary,
	}
	if team {
		data["scope"] = scopeTeam
	}
	if total > len(rows) {
		data["note"] = fmt.Sprintf(
			"조건에 맞는 %d건 중 %d건만 반환했습니다. summary 의 수치는 %d건 전체를 센 것입니다. 이 목록을 전체로 보고 요약하지 마세요.",
			total, len(rows), total)
	}
	return data, nil
}

// mcpTextSearch is the content search as a tool result.
//
// The report search filters by week and status; nothing on this surface could
// ask what a report *says*. An agent asked "AI 게이트웨이 문제가 어느 주에
// 있었나" had to page weeks and read summaries, or answer from the one line the
// search returns — and the product has had a three-pass content search, with
// the caller's own visibility rules and snippets, the whole time.
//
// The screen's answer is kept whole rather than re-scored: `reason` says why a
// thin result stayed thin, and `fuzzy`/`semantic` say the match was not
// literal. A model that is not told the match was approximate reports it as an
// exact quotation.
func (a *App) mcpTextSearch(r *http.Request, p *principal, query string) (map[string]any, error) {
	result, err := a.searchReportsFor(r, p, query)
	if err != nil {
		return nil, err
	}
	data := map[string]any{
		"query": result.Query, "terms": result.Terms, "hits": result.Hits,
		"returned": len(result.Hits), "truncated": result.Truncated,
		"fuzzy": result.Fuzzy, "semantic": result.Semantic,
	}
	notes := []string{}
	if result.Reason != "" {
		notes = append(notes, result.Reason)
	}
	if result.Truncated {
		notes = append(notes, fmt.Sprintf(
			"조건에 맞는 보고서가 더 있어 상위 %d건만 반환했습니다. 이 목록을 전체로 보고 요약하지 마세요.",
			len(result.Hits)))
	}
	if result.Fuzzy || result.Semantic {
		notes = append(notes, "일부 결과는 글자가 그대로 일치한 것이 아니라 비슷한 표기(approximate) 또는 뜻이 가까운 것(semantic)입니다. 해당 항목을 원문 인용처럼 다루지 마세요.")
	}
	if len(result.Hits) > 0 {
		notes = append(notes, "본문 전체는 reportId 로 weekly_report_detail 을 호출해 읽으세요.")
	}
	if len(notes) > 0 {
		data["note"] = strings.Join(notes, " ")
	}
	return data, nil
}

// mcpOwnReport is the caller's report covering the week weekStart names,
// defaulting to this week. Found by the days it covers, the way every screen
// finds it, so a grid that moved does not hide it.
func (a *App) mcpOwnReport(r *http.Request, p *principal, arguments map[string]any) (int64, error) {
	ctx := r.Context()
	week, err := mcpDateArgument(arguments, "weekStart")
	if err != nil {
		return 0, mcpRefusal{message: err.Error()}
	}
	grid := a.setting(ctx, "workflow.week_start", "MONDAY")
	start := currentWeekStart(time.Now().In(a.serviceLocation(ctx)), grid)
	if week != "" {
		parsed, _ := time.ParseInLocation(dateLayout, week, a.serviceLocation(ctx))
		start = currentWeekStart(parsed, grid)
	}
	var id int64
	err = a.db.QueryRow(ctx, `SELECT r.id FROM weekly_reports r
		WHERE r.user_id=$1 AND `+weekCoveringDays("r", 2)+`
		ORDER BY r.week_start DESC LIMIT 1`, p.ID, start.Format(dateLayout)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, mcpRefuse("%s 주에 본인의 보고서가 없습니다. weekly_report_create 로 만들 수 있습니다.", start.Format(dateLayout))
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// mcpScheduleWindow reads the board window out of tool arguments, defaulting to
// the month the service is in — the same default the board opens on.
func mcpScheduleWindow(arguments map[string]any, today time.Time) (time.Time, time.Time, error) {
	location := today.Location()
	from := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, location)
	to := from.AddDate(0, 1, -1)
	fromText, err := mcpDateArgument(arguments, "from")
	if err != nil {
		return from, to, err
	}
	toText, err := mcpDateArgument(arguments, "to")
	if err != nil {
		return from, to, err
	}
	if fromText != "" {
		from, _ = time.ParseInLocation(dateLayout, fromText, location)
		to = from.AddDate(0, 1, -1)
	}
	if toText != "" {
		to, _ = time.ParseInLocation(dateLayout, toText, location)
	}
	if to.Before(from) {
		return from, to, mcpRefuse("to 는 from 보다 빠를 수 없습니다.")
	}
	if to.Sub(from) > scheduleSpanDays*24*time.Hour {
		return from, to, mcpRefuse("한 번에 조회할 수 있는 기간은 최대 %d일입니다.", scheduleSpanDays)
	}
	return from, to, nil
}

// mcpScheduleStates is the vocabulary of the board filter, declared in the
// schema and enforced here — the lesson the status filter taught.
var mcpScheduleStates = []string{"ALL", "OPEN", "DONE", "OVERDUE", "URGENT"}

// mcpCanReadNames is who may be handed a list of people who have not reported.
func mcpCanReadNames(p *principal) bool {
	return p.Role == "ADMIN" || p.Role == "TEAM_LEADER" || p.Role == "ORG_MANAGER"
}

// mcpNewTools is the part of the tool list this file supplies, kept beside the
// code that answers it so the two cannot drift.
func mcpNewTools(p *principal, readOnly map[string]any) []map[string]any {
	tools := []map[string]any{
		{
			"name": "weekly_report_detail", "title": "주간보고 본문 열기",
			"description": "보고서 한 건의 업무 목록, 금주 실적, 차주 계획, 이슈, 건의사항, 검토 의견을 그대로 읽습니다. " +
				"reportId 를 주면 그 보고서(권한 범위 안), 주지 않으면 호출한 계정 본인의 보고서를 weekStart(생략하면 이번 주)로 찾습니다. " +
				"본인 보고서를 고치기 전에 이것으로 읽어 version 을 얻으세요.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"reportId":  map[string]any{"type": "integer", "description": "보고서 id. 생략하면 본인의 보고서"},
				"weekStart": map[string]any{"type": "string", "format": "date", "description": "reportId 가 없을 때 찾을 주(그 날이 든 주). 생략하면 이번 주"},
			}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"id": map[string]any{"type": "integer"}, "userId": map[string]any{"type": "integer"},
				"username": map[string]any{"type": "string"}, "displayName": map[string]any{"type": "string"},
				"weekStart":   map[string]any{"type": "string", "format": "date"},
				"status":      map[string]any{"type": "string", "enum": mcpReportStatuses},
				"sourceType":  map[string]any{"type": "string"},
				"summary":     map[string]any{"type": "string"},
				"version":     map[string]any{"type": "integer", "description": "수정할 때 weekly_report_update 에 그대로 넣는 값. 다른 곳에서 먼저 저장되면 거부된다"},
				"submittedAt": map[string]any{"type": []string{"string", "null"}}, "reviewedAt": map[string]any{"type": []string{"string", "null"}},
				"reviewedBy": map[string]any{"type": "string"}, "updatedAt": map[string]any{"type": "string"},
				"items":                 map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"itemCount":             map[string]any{"type": "integer"},
				"comments":              map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"includedMaterials":     map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"includedMaterialsNote": map[string]any{"type": "string"},
			}, "required": []string{"id", "weekStart", "status", "summary", "version", "items"}},
			"annotations": readOnly,
		},
		{
			"name": "weekly_reports_text_search", "title": "주간보고 내용 검색",
			"description": "보고서 본문(요약·업무명·실적·계획·이슈)에서 말로 찾습니다. 어느 주인지 모를 때 쓰세요. " +
				"주차나 상태로 거르려면 weekly_reports_search 를 쓰세요. " +
				"일치한 부분을 스니펫으로 돌려주며, 결과가 적으면 오타·어미가 다른 표기와 뜻이 가까운 보고서까지 넓혀 찾고 그 사실을 함께 알려 줍니다.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"q": map[string]any{"type": "string", "minLength": 2, "description": "찾을 말. 공백으로 나눈 최대 6개 낱말을 모두 포함하는 보고서를 찾는다"},
			}, "required": []string{"q"}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"query":     map[string]any{"type": "string"},
				"terms":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"hits":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"returned":  map[string]any{"type": "integer"},
				"truncated": map[string]any{"type": "boolean"},
				"fuzzy":     map[string]any{"type": "boolean", "description": "글자가 그대로 일치하지 않은 결과가 섞여 있다"},
				"semantic":  map[string]any{"type": "boolean", "description": "뜻으로 찾은 결과가 섞여 있다"},
				"note":      map[string]any{"type": "string", "description": "근사·의미 일치 경고와 넓히지 못한 이유. 있으면 반드시 읽을 것"},
			}, "required": []string{"query", "terms", "hits", "returned", "truncated"}},
			"annotations": readOnly,
		},
		{
			"name": "schedule_board_tasks", "title": "업무 상황판 일정 조회",
			"description": "부서 업무 상황판의 일정을 기간·범위·상태로 조회합니다. 지연(마감일이 지난 미완료), 오늘 진행, 긴급 건수를 함께 돌려줍니다. " +
				"주간보고가 지난 주에 한 일이라면 이 도구는 지금 진행 중이거나 앞으로 마감할 일입니다.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"from":  map[string]any{"type": "string", "format": "date", "description": "YYYY-MM-DD 시작일. 생략하면 이번 달 1일"},
				"to":    map[string]any{"type": "string", "format": "date", "description": "YYYY-MM-DD 종료일. 생략하면 from 으로부터 한 달"},
				"scope": map[string]any{"type": "string", "enum": []string{scopeSelf, scopeTeam}, "description": "SELF는 본인 일정, TEAM은 소속 조직 상황판. 생략하면 TEAM"},
				"state": map[string]any{"type": "string", "enum": mcpScheduleStates, "description": "ALL 전체, OPEN 미완료, DONE 완료, OVERDUE 마감 지난 미완료, URGENT 미완료 긴급. 생략하면 ALL"},
			}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"from": map[string]any{"type": "string", "format": "date"}, "to": map[string]any{"type": "string", "format": "date"},
				"today": map[string]any{"type": "string", "format": "date", "description": "서비스 시간대의 오늘. 지연·오늘 판정의 기준"},
				"scope": map[string]any{"type": "string"}, "state": map[string]any{"type": "string"},
				"tasks":   map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"total":   map[string]any{"type": "integer"},
				"summary": map[string]any{"type": "object"},
				"note":    map[string]any{"type": "string"},
			}, "required": []string{"from", "to", "today", "tasks", "total", "summary"}},
			"annotations": readOnly,
		},
	}
	// Offered only to the readers who may act on it: listing a tool the caller
	// cannot call is worse than not having it, because an agent reads the list
	// as what it may do and spends a turn finding out otherwise.
	if mcpCanReadNames(p) {
		tools = append(tools, map[string]any{
			"name": "weekly_missing_submitters", "title": "주차별 미제출자 명단",
			"description": "그 주차에 보고서를 내지 않은 사람의 이름을 권한 범위 안에서 돌려줍니다. 임시저장(DRAFT)은 제출로 세지 않습니다. " +
				"weekly_submission_overview 가 몇 명인지 알려 준다면 이 도구는 누구인지 알려 줍니다.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"weekStart": map[string]any{"type": "string", "format": "date", "description": "YYYY-MM-DD 주차 시작일. 생략하면 현재 주차"},
			}},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"weekStart": map[string]any{"type": "string"}, "activeUsers": map[string]any{"type": "integer"},
				"submitted": map[string]any{"type": "integer"}, "missingTotal": map[string]any{"type": "integer"},
				"submissionRate": map[string]any{"type": "number"},
				"people":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			}, "required": []string{"weekStart", "activeUsers", "submitted", "missingTotal", "people"}},
			"annotations": readOnly,
		})
	}
	return tools
}

// callMCPNewTool answers the tools in this file. The second return says whether
// the name belonged here at all, so the original switch keeps its own default.
func (a *App) callMCPNewTool(r *http.Request, p *principal, name string, arguments map[string]any) (any, error, bool) {
	switch name {
	case "weekly_report_detail":
		id := int64(mcpArgumentInt(arguments, "reportId", 0, 0, 1<<53))
		if _, given := arguments["reportId"]; given && id <= 0 {
			return nil, mcpRefuse("reportId 는 weekly_reports_search 가 돌려준 보고서 id 여야 합니다."), true
		}
		if id <= 0 {
			// 내 보고서. The question a model asks before it writes — "what is
			// in my report this week" — has no id yet, and making it search
			// for one first was a turn spent on the obvious.
			var err error
			id, err = a.mcpOwnReport(r, p, arguments)
			if err != nil {
				return nil, err, true
			}
		}
		data, err := a.mcpReportDetail(r.Context(), p, id)
		return data, err, true
	case "weekly_missing_submitters":
		if !mcpCanReadNames(p) {
			return nil, mcpRefuse("미제출자 명단은 팀장 이상만 조회할 수 있습니다."), true
		}
		week, err := mcpDateArgument(arguments, "weekStart")
		if err != nil {
			return nil, mcpRefusal{message: err.Error()}, true
		}
		if week == "" {
			week = currentWeekStart(time.Now().In(a.serviceLocation(r.Context())),
				a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format(dateLayout)
		}
		data, err := a.mcpMissingSubmitters(r.Context(), p, week)
		return data, err, true
	case "weekly_reports_text_search":
		query := mcpArgumentString(arguments, "q")
		if runeLength(query) < 2 {
			return nil, mcpRefuse("q 는 두 글자 이상이어야 합니다. 찾을 말을 넣으세요."), true
		}
		data, err := a.mcpTextSearch(r, p, query)
		return data, err, true
	case "schedule_board_tasks":
		location := a.serviceLocation(r.Context())
		today := time.Now().In(location)
		from, to, err := mcpScheduleWindow(arguments, today)
		if err != nil {
			return nil, mcpRefusal{message: err.Error()}, true
		}
		scope := strings.ToUpper(mcpArgumentString(arguments, "scope"))
		if scope == "" {
			scope = scopeTeam
		}
		if scope != scopeSelf && scope != scopeTeam {
			return nil, mcpRefuse("조회 범위는 SELF 또는 TEAM이어야 합니다."), true
		}
		state := strings.ToUpper(mcpArgumentString(arguments, "state"))
		if state == "" {
			state = "ALL"
		}
		if !contains(mcpScheduleStates, state) {
			return nil, mcpRefuse("state 는 %s 중 하나여야 합니다.", strings.Join(mcpScheduleStates, ", ")), true
		}
		data, err := a.mcpScheduleTasks(r.Context(), p, from, to, scope == scopeTeam, state, today.Format(dateLayout))
		return data, err, true
	}
	return nil, nil, false
}
