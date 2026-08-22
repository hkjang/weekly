package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrepareClonedReportStructureModeResetsWeeklyContent(t *testing.T) {
	source := []reportItem{{ID: 9, Category: "개발", Title: "복제 기능", CurrentResult: "완료", NextPlan: "배포", Issue: "없음", Progress: 80, SortOrder: 7}}
	summary, items := prepareClonedReport("원본 요약", source, "STRUCTURE")

	if summary != "" || len(items) != 1 {
		t.Fatalf("unexpected structure clone: summary=%q items=%+v", summary, items)
	}
	item := items[0]
	if item.ID != 0 || item.Category != "개발" || item.Title != "복제 기능" || item.CurrentResult != "" || item.NextPlan != "" || item.Issue != "" || item.Progress != 0 || item.SortOrder != 0 {
		t.Fatalf("structure clone retained stale weekly data: %+v", item)
	}
}

func TestPrepareClonedReportFullModeCopiesContentWithoutIdentity(t *testing.T) {
	source := []reportItem{{ID: 9, Category: "개발", Title: "복제 기능", CurrentResult: "완료", NextPlan: "배포", Issue: "없음", Progress: 80, SortOrder: 4}}
	summary, items := prepareClonedReport("원본 요약", source, "FULL")

	if summary != "원본 요약" || len(items) != 1 {
		t.Fatalf("unexpected full clone: summary=%q items=%+v", summary, items)
	}
	item := items[0]
	if item.ID != 0 || item.CurrentResult != "완료" || item.NextPlan != "배포" || item.Issue != "없음" || item.Progress != 80 || item.SortOrder != 0 {
		t.Fatalf("full clone mismatch: %+v", item)
	}
}

// A page size that arrives as nonsense must not become a page of nonsense.
// The list is what a screen shows; a limit of 0 would show nothing and a limit
// of 100000 would be the silent LIMIT 500 again with extra steps.
func TestClampQueryIntHoldsThePageInsideItsBounds(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", reportPageDefault},            // absent
		{"?limit=abc", reportPageDefault},  // unreadable
		{"?limit=", reportPageDefault},     // empty
		{"?limit=50", 50},                  // in range
		{"?limit=0", 1},                    // below the floor
		{"?limit=-10", 1},                  // negative
		{"?limit=9999", reportPageMaximum}, // above the ceiling
		{"?limit=500", reportPageMaximum},  // exactly the ceiling
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/reports"+testCase.query, nil)
		if got := clampQueryInt(request, "limit", reportPageDefault, 1, reportPageMaximum); got != testCase.want {
			t.Errorf("limit%q = %d, want %d", testCase.query, got, testCase.want)
		}
	}
	// Offset differs from limit in one way that matters: zero is a real value,
	// not a missing one, so the floor cannot be 1.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/reports?offset=0", nil)
	if got := clampQueryInt(request, "offset", 0, 0, 1_000_000); got != 0 {
		t.Errorf("offset=0 read as %d; a first page is offset zero, not offset one", got)
	}
}
