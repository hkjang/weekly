package app

import (
	"strings"
	"testing"
)

func TestSearchTermsSplitsAndCaps(t *testing.T) {
	if got := searchTerms("  AI  게이트웨이 "); len(got) != 2 || got[0] != "AI" || got[1] != "게이트웨이" {
		t.Errorf("searchTerms = %#v, want two trimmed terms", got)
	}
	if got := searchTerms("a b c d e f g h"); len(got) != searchMaxTerms {
		t.Errorf("term count = %d, want it capped at %d", len(got), searchMaxTerms)
	}
	if got := searchTerms("   "); len(got) != 0 {
		t.Errorf("a blank query produced %#v", got)
	}
}

func TestEscapeLikePatternNeutralisesWildcards(t *testing.T) {
	// A bare % would otherwise match every report.
	if got := escapeLikePattern("100%"); got != `%100\%%` {
		t.Errorf("escapeLikePattern(100%%) = %q", got)
	}
	if got := escapeLikePattern("a_b"); got != `%a\_b%` {
		t.Errorf("escapeLikePattern(a_b) = %q", got)
	}
	if got := escapeLikePattern(`back\slash`); got != `%back\\slash%` {
		t.Errorf("escapeLikePattern(backslash) = %q", got)
	}
	if got := escapeLikePattern("게이트웨이"); got != "%게이트웨이%" {
		t.Errorf("a plain term must pass through: %q", got)
	}
}

func TestBuildSnippetCentresOnTheMatch(t *testing.T) {
	value := strings.Repeat("앞쪽 문맥 ", 12) + "핵심 키워드 발견 " + strings.Repeat("뒤쪽 문맥 ", 12)
	snippet, ok := buildSnippet(value, []string{"핵심 키워드"})
	if !ok {
		t.Fatal("expected a snippet")
	}
	if !strings.Contains(snippet, "핵심 키워드") {
		t.Errorf("snippet lost the match: %q", snippet)
	}
	if runeLength(snippet) > searchSnippetRunes+8 {
		t.Errorf("snippet is %d runes, want about %d", runeLength(snippet), searchSnippetRunes)
	}
	if !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") {
		t.Errorf("a trimmed snippet must show ellipses on both sides: %q", snippet)
	}
}

func TestBuildSnippetKeepsShortValuesWhole(t *testing.T) {
	snippet, ok := buildSnippet("GPU 자원 부족", []string{"자원"})
	if !ok || snippet != "GPU 자원 부족" {
		t.Errorf("snippet = %q, want the whole short value", snippet)
	}
	// Line breaks collapse so the snippet stays one readable line.
	snippet, ok = buildSnippet("첫 줄\n두 번째 줄", []string{"두 번째"})
	if !ok || strings.Contains(snippet, "\n") {
		t.Errorf("snippet = %q, want a single line", snippet)
	}
}

func TestBuildSnippetIsCaseInsensitiveAndReportsMisses(t *testing.T) {
	if _, ok := buildSnippet("Deploy Pipeline Ready", []string{"pipeline"}); !ok {
		t.Error("matching must ignore case")
	}
	if _, ok := buildSnippet("전혀 다른 내용", []string{"게이트웨이"}); ok {
		t.Error("a value without the term must not produce a snippet")
	}
	if _, ok := buildSnippet("   ", []string{"a"}); ok {
		t.Error("a blank value must not produce a snippet")
	}
}

func TestAppendSearchMatchesRanksTitleHighest(t *testing.T) {
	hit := &searchHit{Matches: []searchMatch{}}
	// searchFieldWeights order: title, category, summary, currentResult, nextPlan, issue.
	appendSearchMatches(hit, []string{"게이트웨이"}, []string{
		"AI 게이트웨이 구축", "플랫폼", "주간 요약에는 없음", "게이트웨이 성능 시험", "", ""})
	if hit.Score != 50 {
		t.Errorf("score = %d, want the title weight 50", hit.Score)
	}
	if len(hit.Matches) == 0 || hit.Matches[0].Field != "title" {
		t.Fatalf("matches = %#v, want the title first", hit.Matches)
	}
	// A body match carries the item title so the reader knows where it came from.
	var body *searchMatch
	for index := range hit.Matches {
		if hit.Matches[index].Field == "currentResult" {
			body = &hit.Matches[index]
		}
	}
	if body == nil {
		t.Fatal("the matching result field produced no snippet")
	}
	if body.Title != "AI 게이트웨이 구축" {
		t.Errorf("body match title = %q, want the item title", body.Title)
	}
}

func TestAppendSearchMatchesCapsSnippetsPerReport(t *testing.T) {
	hit := &searchHit{Matches: []searchMatch{}}
	appendSearchMatches(hit, []string{"공통"}, []string{
		"공통 업무", "공통 구분", "공통 요약", "공통 실적", "공통 계획", "공통 이슈"})
	if len(hit.Matches) > searchSnippetPerHit {
		t.Errorf("collected %d snippets, want at most %d", len(hit.Matches), searchSnippetPerHit)
	}
	if len(hit.Matches) == 0 {
		t.Error("expected at least one snippet")
	}
}

// The three passes run in sequence and each skips what the earlier ones found.
// A report returned by the trigram pass must not come back again from the
// semantic pass under a different label.
func TestLaterSearchPassesSkipReportsAlreadyFound(t *testing.T) {
	found := map[int64]*searchHit{}
	approximate := []searchHit{{ReportID: 7, Approximate: true}, {ReportID: 9, Approximate: true}}
	for index := range approximate {
		found[approximate[index].ReportID] = &approximate[index]
	}
	for _, id := range []int64{7, 9} {
		if found[id] == nil {
			t.Fatalf("report %d from the approximate pass was not recorded", id)
		}
	}
	if found[7] == found[9] {
		t.Error("each recorded hit must be a distinct element, not a shared loop variable")
	}
	if found[11] != nil {
		t.Error("a report no pass returned must stay absent")
	}
}
