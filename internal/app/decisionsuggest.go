package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Suggesting decision candidates from what was already written.
//
// The roadmap fixed both the order and the limit of this feature: the explicit
// record comes first and suggestion afterwards, and the record's completeness
// must never depend on a model's recall. So this proposes and nothing else. A
// candidate is a filled-in form waiting for a person to correct and confirm; no
// suggestion is ever stored, and a work item with no candidates means the model
// found none, not that none were made.
//
// The screen has to say that, because a list that appears to be exhaustive and
// is not is worse than no list. That sentence is carried in the response rather
// than written into the UI, so it cannot be dropped by whoever renders it next.

const (
	decisionSuggestLimit = 5
	// How much reported text one request carries. Enough for a task's whole
	// history in most cases, bounded because a gateway request that times out
	// helps nobody.
	decisionSuggestChars = 12000
)

type decisionCandidate struct {
	Title     string `json:"title"`
	DecidedBy string `json:"decidedBy"`
	DecidedOn string `json:"decidedOn"`
	Rationale string `json:"rationale"`
	FollowUp  string `json:"followUp"`
	// Confidence is the model's own, kept so a reader can sort by it and
	// distrust the bottom of the list. It is not a threshold: nothing here is
	// filtered out on the model's say-so.
	Confidence float64 `json:"confidence"`
	// Evidence is the reported sentence the candidate came from, so the person
	// confirming can check it against the record rather than against the model.
	Evidence string `json:"evidence"`
}

type decisionSuggestion struct {
	Candidates []decisionCandidate `json:"candidates"`
	// Caveat travels with the payload so no screen can render the list without
	// it. Absence of candidates is not evidence of absence of decisions.
	Caveat  string `json:"caveat"`
	Weeks   int    `json:"weeks"`
	Scanned int    `json:"scannedCharacters"`
}

const decisionSuggestCaveat = "AI가 보고 내용에서 찾은 후보입니다. 확정 전까지 아무것도 저장되지 않으며, " +
	"여기 없다고 해서 결정이 없었다는 뜻은 아닙니다. 기록의 완전성은 사람이 적은 것에 달려 있습니다."

func decisionSuggestSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"candidates"},
		"properties": map[string]any{
			"candidates": map[string]any{
				"type": "array", "maxItems": decisionSuggestLimit,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"title", "decidedBy", "decidedOn", "rationale", "followUp", "confidence", "evidence"},
					"properties": map[string]any{
						"title":      map[string]any{"type": "string", "maxLength": 240},
						"decidedBy":  map[string]any{"type": "string", "maxLength": 120},
						"decidedOn":  map[string]any{"type": "string", "maxLength": 10},
						"rationale":  map[string]any{"type": "string", "maxLength": 2000},
						"followUp":   map[string]any{"type": "string", "maxLength": 2000},
						"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
						"evidence":   map[string]any{"type": "string", "maxLength": 2000},
					},
				},
			},
		},
	}
}

const decisionSuggestSystem = `당신은 주간업무보고에서 "결정사항"의 후보를 찾는 보조자입니다.

결정이란 방향을 바꾸거나 확정한 것입니다. 예: 범위를 줄이기로 함, 벤더 대신 자체 구현하기로 함,
일정을 연기하기로 함, 도구를 바꾸기로 함.

결정이 아닌 것: 진행 상황 보고, 계획 나열, 이슈 서술, 완료 보고.
이런 것을 결정으로 올리면 목록 전체를 신뢰할 수 없게 되므로, 확신이 없으면 넣지 마십시오.
찾지 못하면 빈 배열을 반환하십시오. 억지로 채우지 마십시오.

각 후보에 대해:
- title: 무엇을 정했는지 한 문장
- decidedBy: 본문에 결정자가 적혀 있을 때만 채우고, 없으면 빈 문자열
- decidedOn: 본문에서 알 수 있는 날짜(YYYY-MM-DD), 없으면 그 내용이 보고된 주차 시작일
- rationale: 왜 그렇게 정했는지. 본문에 근거가 없으면 빈 문자열
- followUp: 하기로 한 후속 조치. 없으면 빈 문자열
- evidence: 근거가 된 보고 원문 문장을 그대로 인용
- confidence: 0에서 1

추측으로 채우지 마십시오. 본문에 없는 것은 빈 문자열로 두십시오.`

// suggestDecisions reads one task's reported text and proposes candidates.
func (a *App) suggestDecisions(w http.ResponseWriter, r *http.Request) {
	workItemID, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	_, visible, err := a.workItemViewer(r.Context(), p, workItemID)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "WORK_ITEM_NOT_FOUND", "업무를 찾을 수 없습니다.")
		return
	}
	if err != nil {
		a.logger.Error("suggest decisions", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "결정 후보를 찾을 수 없습니다.")
		return
	}
	if !visible {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "조회 권한 범위 밖의 업무입니다.")
		return
	}
	cfg, err := a.aiConfig(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "AI_UNAVAILABLE", "관리자가 AI Gateway를 설정하고 활성화해야 합니다.")
		return
	}

	text, weeks, err := a.workItemReportedText(r.Context(), workItemID)
	if err != nil {
		a.logger.Error("suggest decisions", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", "결정 후보를 찾을 수 없습니다.")
		return
	}
	if strings.TrimSpace(text) == "" {
		writeData(w, http.StatusOK, decisionSuggestion{
			Candidates: []decisionCandidate{}, Caveat: decisionSuggestCaveat, Weeks: weeks})
		return
	}

	var structured struct {
		Candidates []decisionCandidate `json:"candidates"`
	}
	if _, err := callStructuredAI(r.Context(), cfg, "decision_candidates", decisionSuggestSystem, text,
		decisionSuggestSchema(), &structured); err != nil {
		a.logger.Error("suggest decisions", "error", err, "trace", traceIDFromContext(r.Context()))
		writeError(w, http.StatusBadGateway, "AI_FAILED", "AI가 결정 후보를 만들지 못했습니다.")
		return
	}

	candidates := []decisionCandidate{}
	for _, candidate := range structured.Candidates {
		candidate.Title = strings.TrimSpace(candidate.Title)
		if candidate.Title == "" {
			continue
		}
		candidate.DecidedBy = strings.TrimSpace(candidate.DecidedBy)
		candidate.Rationale = strings.TrimSpace(candidate.Rationale)
		candidate.FollowUp = strings.TrimSpace(candidate.FollowUp)
		candidate.Evidence = strings.TrimSpace(candidate.Evidence)
		// A date the model invented is worse than no date: the form defaults to
		// today, which the person confirming will notice and fix.
		if _, parseErr := time.Parse("2006-01-02", strings.TrimSpace(candidate.DecidedOn)); parseErr != nil {
			candidate.DecidedOn = ""
		}
		candidates = append(candidates, candidate)
		if len(candidates) == decisionSuggestLimit {
			break
		}
	}
	a.audit(r, p, "decision.suggest", "work_item", fmt.Sprint(workItemID),
		map[string]any{"candidates": len(candidates), "weeks": weeks})
	writeData(w, http.StatusOK, decisionSuggestion{
		Candidates: candidates, Caveat: decisionSuggestCaveat, Weeks: weeks, Scanned: len(text)})
}

// workItemReportedText gathers what was written about one task, oldest week
// first, labelled by week so a date in the answer can be traced back.
func (a *App) workItemReportedText(ctx context.Context, workItemID int64) (string, int, error) {
	rows, err := a.db.Query(ctx, `SELECT r.week_start, i.current_result, i.next_plan, i.issue, i.management_ask
		FROM report_items i JOIN weekly_reports r ON r.id=i.report_id
		WHERE i.work_item_id=$1 ORDER BY r.week_start`, workItemID)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	var builder strings.Builder
	weeks := 0
	for rows.Next() {
		var week time.Time
		var current, plan, issue, ask string
		if err := rows.Scan(&week, &current, &plan, &issue, &ask); err != nil {
			return "", 0, err
		}
		weeks++
		if builder.Len() >= decisionSuggestChars {
			continue
		}
		builder.WriteString("## " + week.Format("2006-01-02") + " 주차\n")
		for _, field := range []struct{ label, value string }{
			{"실적", current}, {"계획", plan}, {"이슈", issue}, {"상위 조직 요청", ask},
		} {
			if strings.TrimSpace(field.value) != "" {
				builder.WriteString(field.label + ": " + strings.TrimSpace(field.value) + "\n")
			}
		}
		builder.WriteString("\n")
	}
	return builder.String(), weeks, rows.Err()
}
