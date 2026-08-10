package app

import "testing"

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
