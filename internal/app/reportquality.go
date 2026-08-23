package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Reporting quality checks, run against the draft on screen.
//
// Every rule here is one a reviewer would raise, and every one of them is
// cheaper to fix while the author still has the report open. That is the whole
// design goal: the findings go to the writer first, before submission, not to a
// team leader a day later.
//
// The rules are deterministic and need no model, so they work in a deployment
// with the AI Gateway switched off. Language quality — vague wording, claims of
// completion without evidence — is a different kind of judgement and is left to
// the optional AI layer.

const (
	// A plan restated once can be a task that genuinely spans two weeks. Twice
	// in a row is a task being copied forward rather than moved forward.
	repeatedPlanWeeks = 2
	// How far back the checks look. Long enough for a persistent issue to show
	// itself, short enough that a task from last year is not evidence.
	qualityHistoryWeeks = 26
)

type qualityFinding struct {
	Rule string `json:"rule"`
	// Severity separates "this is probably wrong" from "this is worth a look".
	// A screen that shouts equally about everything gets ignored equally.
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Message  string `json:"message"`
}

type qualityReport struct {
	Week     string           `json:"week"`
	Checked  int              `json:"checked"`
	Findings []qualityFinding `json:"findings"`
}

// priorSnapshots returns a work item's weeks strictly before the given week,
// oldest first. The draft is what is being checked, so the saved copy of the
// same week must not be part of the evidence.
func priorSnapshots(item workItemView, week string) []workItemWeek {
	result := []workItemWeek{}
	for _, snapshot := range item.Weeks {
		if snapshot.WeekStart < week {
			result = append(result, snapshot)
		}
	}
	return result
}

// runsBackFrom counts how many consecutive weeks at the end of the history share
// the given key. Zero means the most recent week already differs.
func runsBackFrom(weeks []workItemWeek, key string, field func(workItemWeek) string) int {
	if key == "" {
		return 0
	}
	count := 0
	for index := len(weeks) - 1; index >= 0; index-- {
		if lineKey(field(weeks[index])) != key {
			break
		}
		count++
	}
	return count
}

// checkReportQuality compares one draft against the author's own history.
// blocked carries what each task is still waiting on, so a check can tell a
// stall the author has explained from one they have not. v0.17 set the rule
// this follows: telling somebody to act on what they have already acted on is
// how a whole checklist gets ignored.
func checkReportQuality(week string, items []reportItem, history []workItemView,
	blocked map[int64]blockedNote, cfg rollupConfig) qualityReport {
	byKey := map[string]workItemView{}
	for _, item := range history {
		byKey[planMatchKey(item.Title)] = item
	}
	report := qualityReport{Week: week, Findings: []qualityFinding{}}

	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}
		report.Checked++
		known, exists := byKey[planMatchKey(title)]
		if !exists {
			continue
		}
		weeks := priorSnapshots(known, week)
		if len(weeks) == 0 {
			continue
		}
		previous := weeks[len(weeks)-1]

		// Progress does not run backwards on its own. Either last week was
		// overstated or this week is a typo, and both are worth one look now.
		if item.Progress < previous.Progress {
			report.Findings = append(report.Findings, qualityFinding{
				Rule: "PROGRESS_REGRESSED", Severity: "WARN", Title: title,
				Message: fmt.Sprintf("지난주 %d%%보다 낮은 %d%%로 적었습니다. 지난주 보고가 과했거나 이번 주 입력이 잘못됐을 수 있습니다.",
					previous.Progress, item.Progress),
			})
		}

		// A plan carried forward word for word, while the progress figure does
		// not move, is the clearest sign of a task being restated.
		planRun := runsBackFrom(weeks, lineKey(item.NextPlan), func(week workItemWeek) string { return week.NextPlan })
		if planRun >= repeatedPlanWeeks && item.Progress <= previous.Progress {
			finding := qualityFinding{
				Rule: "PLAN_REPEATED", Severity: "WARN", Title: title,
				Message: fmt.Sprintf("같은 차주 계획을 %d주째 그대로 적었고 진척도도 오르지 않았습니다. 계획을 쪼개거나 막힌 이유를 이슈로 적어 주세요.",
					planRun+1),
			}
			// This warning asks the author to record why they are stuck. Someone
			// who declared a blocker has done exactly that, so repeating the ask
			// is the nagging v0.17 warned about. The finding stays — a plan
			// unchanged for weeks is still worth a look — but it becomes a note
			// about whether the dependency still holds rather than a demand.
			if note, waiting := blocked[known.ID]; waiting {
				finding.Severity = "INFO"
				finding.Message = fmt.Sprintf("같은 차주 계획을 %d주째 그대로 적었습니다. %s 그 관계가 아직 유효한지 확인하세요.",
					planRun+1, note.sentence())
			}
			report.Findings = append(report.Findings, finding)
		}

		// Something was promised for this week and the result box is empty.
		if strings.TrimSpace(previous.NextPlan) != "" && strings.TrimSpace(item.CurrentResult) == "" {
			report.Findings = append(report.Findings, qualityFinding{
				Rule: "PLAN_WITHOUT_RESULT", Severity: "WARN", Title: title,
				Message: "지난주에 계획한 일인데 이번 주 실적이 비어 있습니다. 진행한 내용이나 못 한 이유를 적어 주세요.",
			})
		}

		// The same issue text, week after week, means the current approach is
		// not working. Reporting it again is not the same as escalating it.
		issueRun := runsBackFrom(weeks, lineKey(item.Issue), func(week workItemWeek) string { return week.Issue })
		if strings.TrimSpace(item.Issue) != "" && issueRun >= cfg.PersistentIssueWeeks {
			message := fmt.Sprintf("같은 이슈를 %d주째 보고하고 있습니다. 같은 방식으로는 풀리지 않는 문제일 수 있으니 상위 조직 요청으로 올리는 것을 검토하세요.", issueRun+1)
			if strings.TrimSpace(item.ManagementAsk) != "" {
				// Already escalated. Saying so keeps the finding honest instead
				// of nagging about something the author has acted on.
				message = fmt.Sprintf("같은 이슈를 %d주째 보고하고 있습니다. 상위 조직 요청은 이미 적혀 있으니 회신 여부를 확인하세요.", issueRun+1)
			}
			report.Findings = append(report.Findings, qualityFinding{
				Rule: "ISSUE_PERSISTED", Severity: "INFO", Title: title, Message: message,
			})
		}
	}

	// Warnings first, then by the order the items appear on screen, so the list
	// reads in the same direction the author is working.
	sort.SliceStable(report.Findings, func(left, right int) bool {
		return report.Findings[left].Severity == "WARN" && report.Findings[right].Severity != "WARN"
	})
	return report
}

// reportQuality checks the draft the caller sends, not the saved copy.
//
// It takes the items in the request body on purpose: the checks are worth most
// before the report is saved, and a check that can only see saved content would
// answer about a version the author has already moved past.
func (a *App) reportQuality(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WeekStart string       `json:"weekStart"`
		Items     []reportItem `json:"items"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	week := strings.TrimSpace(input.WeekStart)
	if week == "" {
		week = currentWeekStart(time.Now().In(a.serviceLocation(r.Context())),
			a.setting(r.Context(), "workflow.week_start", "MONDAY")).Format("2006-01-02")
	}
	parsed, err := time.Parse("2006-01-02", week)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WEEK", "주차는 YYYY-MM-DD 형식이어야 합니다.")
		return
	}
	p := currentPrincipal(r.Context())
	since := parsed.AddDate(0, 0, -7*qualityHistoryWeeks).Format("2006-01-02")
	history, err := a.loadWorkItems(r.Context(), scopeForPrincipal(p, true), since)
	if err != nil {
		a.logger.Error("report quality", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "보고 품질을 점검할 수 없습니다.")
		return
	}
	ids := make([]int64, 0, len(history))
	for _, item := range history {
		ids = append(ids, item.ID)
	}
	blocked, err := a.blockedNotes(r.Context(), ids)
	if err != nil {
		a.logger.Error("report quality dependencies", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "보고 품질을 점검할 수 없습니다.")
		return
	}
	writeData(w, http.StatusOK, checkReportQuality(week, input.Items, history, blocked, a.rollupConfig(r.Context())))
}
