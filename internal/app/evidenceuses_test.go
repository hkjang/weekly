package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// 내 페이지를 근거로 쓴 보고를 빠짐없이 찾는 일.
//
// This panel is opened by the owner of a page, a deck or a commit who is about
// to change it and needs to know who built on it. It had no test at all, and it
// answered "50건 중 12건만 보여 줍니다" with nowhere to go from there — which
// the paging check's own opening argument names as the answer that is not one.
//
// guards: evidenceUses
func TestEveryCitationOfAPageCanBeReachedWithinTheCallersScope(t *testing.T) {
	server := newTestServer(t)
	mine := server.createOrganization("근거 내 조직", "EVMINE")
	theirs := server.createOrganization("근거 남의 조직", "EVTHEIRS")
	leader := server.createUser("evlead", "TEAM_LEADER", &mine)
	member := server.createUser("evmate", "USER", &mine)
	stranger := server.createUser("evouter", "USER", &theirs)

	const page = "CONF-4210"
	cite := func(cookie *http.Cookie, week, title, sourceTitle string) {
		id, version := server.draft(cookie, week, week+" 보고")
		if w := server.request(http.MethodPut, fmt.Sprintf("/api/v1/reports/%d", id), map[string]any{
			"summary": week + " 보고", "version": version,
			"items": []map[string]any{{"category": "개발", "title": title, "currentResult": "했습니다", "progress": 50}},
		}, cookie); w.Code != http.StatusOK {
			t.Fatalf("write %s: %d %s", week, w.Code, w.Body.String())
		}
		if _, err := server.app.db.Exec(server.ctx(), `
			INSERT INTO report_item_sources(report_item_id, kind, reference, title)
			SELECT i.id, 'CONFLUENCE', $2, $3 FROM report_items i WHERE i.report_id = $1`,
			id, page, sourceTitle); err != nil {
			t.Fatalf("cite the page: %v", err)
		}
	}

	// Sixty citations inside the caller's scope, past the fifty the panel sends
	// at once, and one outside it.
	monday := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)
	weeks := []string{}
	for index := 0; index < 60; index++ {
		week := monday.AddDate(0, 0, 7*index).Format(dateLayout)
		weeks = append(weeks, week)
		// The page was renamed at some point, and the citations kept the name
		// it had when each was written.
		name := "설계 페이지"
		if index == 0 {
			name = "옛 설계 페이지"
		}
		cite(member, week, fmt.Sprintf("페이지를 근거로 쓴 업무 %d", index), name)
	}
	cite(stranger, "2026-03-02", "남의 조직이 같은 페이지를 근거로 쓴 업무", "설계 페이지")

	read := func(offset int) (uses []map[string]any, total int) {
		path := fmt.Sprintf("/api/v1/evidence/uses?kind=CONFLUENCE&reference=%s&offset=%d", page, offset)
		response := server.request(http.MethodGet, path, nil, leader)
		if response.Code != http.StatusOK {
			t.Fatalf("evidence uses: %d %s", response.Code, response.Body.String())
		}
		var payload struct {
			Data struct {
				Uses  []map[string]any `json:"uses"`
				Total int              `json:"total"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v", response.Body.String(), err)
		}
		return payload.Data.Uses, payload.Data.Total
	}

	first, total := read(0)
	if total != len(weeks) {
		t.Fatalf("the panel counts %d citations, want the %d inside the caller's scope", total, len(weeks))
	}
	if len(first) != evidenceUseLimit {
		t.Fatalf("the first page carries %d, want the cap of %d", len(first), evidenceUseLimit)
	}
	// The rest is reachable, and it is the rest — not the same page again.
	rest, _ := read(len(first))
	if len(rest) != total-len(first) {
		t.Fatalf("the second page carries %d, want the remaining %d", len(rest), total-len(first))
	}
	seen := map[any]bool{}
	for _, use := range append(append([]map[string]any{}, first...), rest...) {
		if seen[use["reportItemId"]] {
			t.Fatalf("the same citation came back twice: %v", use["reportItemId"])
		}
		seen[use["reportItemId"]] = true
	}
	if len(seen) != total {
		t.Errorf("paging through reached %d of %d citations", len(seen), total)
	}

	// Nothing from outside the caller's scope, in the list or in the count —
	// the panel names what the reader may already open, and says how many.
	for _, use := range first {
		if title, _ := use["title"].(string); title == "남의 조직이 같은 페이지를 근거로 쓴 업무" {
			t.Fatalf("a citation from another organisation was named: %v", use)
		}
	}

	// The panel is headed by the name the source has now, not the one it had
	// when the oldest citation was written. A reader opening "무엇이 이 위에
	// 세워졌나" for a page that was renamed last year should not be told the
	// old name back.
	response := server.request(http.MethodGet,
		"/api/v1/evidence/uses?kind=CONFLUENCE&reference="+page, nil, leader)
	var titled struct {
		Data struct {
			Title string `json:"title"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &titled); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	if titled.Data.Title != "설계 페이지" {
		t.Errorf("the panel is headed %q, want the name the most recent citation recorded", titled.Data.Title)
	}

	// A caller asking with half the question is refused rather than answered
	// about everything that shares a kind. Both halves, separately: one
	// condition standing in for two is the shape that answers "every
	// CONFLUENCE citation in the service" to somebody who typed a page id.
	for _, query := range []string{"kind=CONFLUENCE", "reference=" + page, ""} {
		refused := server.request(http.MethodGet, "/api/v1/evidence/uses?"+query, nil, leader)
		if refused.Code != http.StatusBadRequest {
			t.Errorf("a lookup with %q answered %d %s", query, refused.Code, refused.Body.String())
		}
	}

	// The page size is the caller's to narrow and not to widen: fifty is what
	// this panel sends at once, and a caller asking for a thousand is asking
	// for the cap to stop meaning anything.
	for _, asked := range []struct {
		query string
		want  int
		// limit=0 is clamped up to one rather than falling back to the default:
		// asking for none is asking for fewer, and this door answers "fewer"
		// the same way everywhere in the product.
	}{{"&limit=5", 5}, {"&limit=9999", evidenceUseLimit}, {"&limit=0", 1}} {
		uses, _ := func() ([]map[string]any, int) {
			path := fmt.Sprintf("/api/v1/evidence/uses?kind=CONFLUENCE&reference=%s%s", page, asked.query)
			response := server.request(http.MethodGet, path, nil, leader)
			if response.Code != http.StatusOK {
				t.Fatalf("evidence uses%s: %d %s", asked.query, response.Code, response.Body.String())
			}
			var payload struct {
				Data struct {
					Uses  []map[string]any `json:"uses"`
					Total int              `json:"total"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode %s: %v", response.Body.String(), err)
			}
			return payload.Data.Uses, payload.Data.Total
		}()
		if len(uses) != asked.want {
			t.Errorf("%s returned %d rows, want %d", asked.query, len(uses), asked.want)
		}
	}
}
