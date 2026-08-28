package app

import (
	"fmt"
	"net/http"
	"testing"
)

// 확정 has to mean the words that were 확정된.
//
// workflow.enabled defaults to false, so 제출 goes straight to CLOSED with no
// reviewer — the state most deployments end every week in. Editing a SUBMITTED
// or an APPROVED report already dropped it back to DRAFT and cleared the
// review; CLOSED was left out. Measured on the default before this test
// existed: submit, then rewrite the summary and every item, and the report
// stayed 확정 with its original 제출시각, with nothing recording that the text
// had changed since. A leader reading 팀 주간보고 saw 확정 beside a time that
// belonged to different words.
//
// Each run needs its own week: one writer may hold one report per week.
func weekOf(workflow string) string {
	if workflow == "true" {
		return "2026-08-17"
	}
	return "2026-08-24"
}

// guards: updateReport
func TestEditingAFinishedReportTakesItBackToDraft(t *testing.T) {
	server := newTestServer(t)
	organization := server.createOrganization("확정 조직", "CLOSEDIT")
	writer := server.createUser("closed_writer", "USER", &organization)

	for _, mode := range []struct {
		workflow  string
		submitted string
	}{
		{"false", "CLOSED"},
		{"true", "SUBMITTED"},
	} {
		t.Run("workflow_"+mode.workflow, func(t *testing.T) {
			set := server.request(http.MethodPut, "/api/v1/admin/settings",
				map[string]any{"settings": map[string]string{"workflow.enabled": mode.workflow}}, server.admin)
			if set.Code != http.StatusOK {
				t.Fatalf("workflow 설정이 %d: %s", set.Code, set.Body.String())
			}

			id, version := server.draft(writer, weekOf(mode.workflow), "확정 전에 쓴 내용")
			path := fmt.Sprintf("/api/v1/reports/%d", id)

			saved := server.request(http.MethodPut, path, map[string]any{
				"summary": "확정 전에 쓴 내용", "version": version,
				"items": []any{map[string]any{"title": "확정 시험", "category": "운영",
					"currentResult": "원래 실적", "nextPlan": "", "issue": "", "progress": 10}},
			}, writer)
			if saved.Code != http.StatusOK {
				t.Fatalf("저장이 %d: %s", saved.Code, saved.Body.String())
			}
			version = int(decodeData(t, saved)["version"].(float64))

			submitted := server.request(http.MethodPost, path+"/submit", map[string]any{"version": version}, writer)
			if submitted.Code != http.StatusOK {
				t.Fatalf("제출이 %d: %s", submitted.Code, submitted.Body.String())
			}
			if status := decodeData(t, submitted)["status"]; status != mode.submitted {
				t.Fatalf("제출 뒤 상태가 %v, %s 를 기대합니다", status, mode.submitted)
			}
			after := decodeData(t, server.request(http.MethodGet, path, nil, writer))
			if after["submittedAt"] == nil {
				t.Fatal("제출시각이 비어 있습니다")
			}

			edited := server.request(http.MethodPut, path, map[string]any{
				"summary": "확정된 뒤에 바꾼 내용", "version": int(after["version"].(float64)),
				"items": []any{map[string]any{"title": "확정 시험", "category": "운영",
					"currentResult": "전혀 다른 실적", "nextPlan": "", "issue": "", "progress": 95}},
			}, writer)
			if edited.Code != http.StatusOK {
				t.Fatalf("수정이 %d: %s", edited.Code, edited.Body.String())
			}

			final := decodeData(t, server.request(http.MethodGet, path, nil, writer))
			if final["status"] != "DRAFT" {
				t.Errorf("%s 인 보고서를 고쳤는데 상태가 %v 입니다 — 작성 중으로 돌아가야 합니다",
					mode.submitted, final["status"])
			}
			if final["submittedAt"] != nil {
				t.Errorf("고친 뒤에도 제출시각이 남아 있습니다: %v — 그 시각은 다른 글의 것입니다",
					final["submittedAt"])
			}

			// The history has to say why, so the change is visible to whoever
			// looks later rather than only to whoever made it.
			var moves int
			if err := server.app.db.QueryRow(server.ctx(),
				`SELECT count(*) FROM report_status_history WHERE report_id=$1 AND to_status='DRAFT'`, id).
				Scan(&moves); err != nil {
				t.Fatal(err)
			}
			if moves == 0 {
				t.Error("작성 중으로 되돌린 사실이 상태 이력에 없습니다")
			}
		})
	}
}
