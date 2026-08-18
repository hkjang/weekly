package app

import (
	"strings"
	"testing"
)

func TestSurfaceTokenStripsUnambiguousEndings(t *testing.T) {
	// Multi-character endings never form the tail of a real noun, so they go
	// without needing corpus evidence.
	cases := map[string]string{
		"시스템에서":    "시스템",
		"완료하였습니다":  "완료",
		"배포합니다":    "배포",
		"검증하였고":    "검증",
		"Pipeline": "pipeline",
		// Single-character particles are left for the corpus-aware pass.
		"게이트웨이를": "게이트웨이를",
		"시스템의":   "시스템의",
	}
	for input, want := range cases {
		if got := surfaceToken(input); got != want {
			t.Errorf("surfaceToken(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStripAmbiguousSuffixNeedsAnAttestedStem(t *testing.T) {
	attested := map[string]bool{"게이트웨이": true, "시스템": true, "인증모듈": true}
	cases := []struct{ input, want string }{
		{"게이트웨이를", "게이트웨이"},
		{"시스템의", "시스템"},
		{"인증모듈을", "인증모듈"},
		// "게이트웨" is not a word, so the trailing 이 belongs to the noun.
		{"게이트웨이", "게이트웨이"},
		// "성" is not attested, so 성과 keeps its shape.
		{"성과", "성과"},
		{"결과", "결과"},
	}
	for _, testCase := range cases {
		if got := stripAmbiguousSuffix(testCase.input, attested); got != testCase.want {
			t.Errorf("stripAmbiguousSuffix(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

func TestShortNounsAreNeverReducedToFragments(t *testing.T) {
	// Stripping "의" from "회의" would leave a meaningless single letter.
	attested := map[string]bool{"회": true, "보": true, "회의": true, "보안": true}
	for _, word := range []string{"회의", "보안", "성과", "제도"} {
		if got := stripAmbiguousSuffix(surfaceToken(word), attested); got != word {
			t.Errorf("%q became %q", word, got)
		}
	}
}

func TestSurfaceTokenRejectsNoise(t *testing.T) {
	for _, input := range []string{"", " ", "1", "2026", "가", "및", "관련", "진행", "the"} {
		if got := surfaceToken(input); got != "" {
			t.Errorf("surfaceToken(%q) = %q, want it dropped", input, got)
		}
	}
}

func TestTokenizeDocumentBuildsPhrasesWithinLines(t *testing.T) {
	accumulator := newTermAccumulator()
	for index := 0; index < 3; index++ {
		accumulator.addDocument("AI 게이트웨이 인증 연동 완료")
	}
	counts := accumulator.counted()
	if counts["게이트웨이"] == 0 {
		t.Errorf("unigram missing: %#v", counts)
	}
	if counts["ai 게이트웨이"] == 0 {
		t.Errorf("adjacent tokens must form a phrase: %#v", counts)
	}
	// A phrase must not span a line break, because the two words are unrelated.
	across := newTermAccumulator()
	across.addDocument("보안 점검\n결산 자동화")
	if across.counted()["점검 결산"] != 0 {
		t.Error("a phrase must not be built across two lines")
	}
}

func TestRankUsesDocumentFrequency(t *testing.T) {
	accumulator := newTermAccumulator()
	// "배포" appears in every document; "탐지모델" only in one.
	for index := 0; index < 8; index++ {
		accumulator.addDocument("배포 작업 배포 작업 배포 작업")
	}
	accumulator.addDocument("탐지모델 학습 탐지모델 학습 탐지모델 학습")
	terms := accumulator.rank(20, nil)
	distinctive, ubiquitous := -1, -1
	for index, term := range terms {
		if distinctive < 0 && strings.Contains(term.Term, "탐지모델") {
			distinctive = index
		}
		if ubiquitous < 0 && strings.Contains(term.Term, "배포") {
			ubiquitous = index
		}
	}
	if distinctive < 0 {
		t.Fatalf("the distinctive term was dropped: %#v", terms)
	}
	if ubiquitous >= 0 && ubiquitous < distinctive {
		t.Errorf("a term present in every document outranked a distinctive one: %#v", terms)
	}
}

func TestRankReportsChangeAgainstThePreviousWindow(t *testing.T) {
	accumulator := newTermAccumulator()
	for index := 0; index < 3; index++ {
		accumulator.addDocument("신규키워드 도입 신규키워드 도입")
	}
	terms := accumulator.rank(10, map[string]int{"신규키워드": 2, "신규키워드 도입": 2})
	for _, term := range terms {
		if !strings.Contains(term.Term, "신규키워드") {
			continue
		}
		if term.Delta != term.Count-2 {
			t.Errorf("%q delta = %d, want count(%d) minus the previous 2", term.Term, term.Delta, term.Count)
		}
		return
	}
	t.Fatalf("term missing from the ranking: %#v", terms)
}

func TestRankDropsSingletonPhrases(t *testing.T) {
	accumulator := newTermAccumulator()
	for index := 0; index < 8; index++ {
		accumulator.addDocument("반복 문구 반복 문구")
	}
	accumulator.addDocument("일회성 조합")
	for _, term := range accumulator.rank(50, nil) {
		if term.Phrase && term.Count < 2 {
			t.Errorf("a phrase seen once survived: %#v", term)
		}
	}
}

func TestSuppressRedundantParts(t *testing.T) {
	terms := []analysisTerm{
		{Term: "ai 게이트웨이", Count: 10, Phrase: true},
		{Term: "게이트웨이", Count: 11},
		{Term: "성능", Count: 9},
	}
	kept := map[string]bool{}
	for _, term := range suppressRedundantParts(terms) {
		kept[term.Term] = true
	}
	if !kept["ai 게이트웨이"] {
		t.Error("the phrase must survive")
	}
	if kept["게이트웨이"] {
		t.Error("a word almost always seen inside a phrase must be suppressed")
	}
	if !kept["성능"] {
		t.Error("an unrelated word must survive")
	}
}

func TestAccumulatorCountsDocumentsNotOccurrences(t *testing.T) {
	accumulator := newTermAccumulator()
	accumulator.addDocument("보안 보안 보안")
	accumulator.addDocument("보안")
	counts := accumulator.counted()
	if counts["보안"] != 4 {
		t.Errorf("count = %d, want 4 occurrences", counts["보안"])
	}
	if accumulator.documentF["보안"] != 2 {
		t.Errorf("documents = %d, want 2", accumulator.documentF["보안"])
	}
	if accumulator.total != 2 {
		t.Errorf("total = %d, want 2 documents", accumulator.total)
	}
	// A document with nothing extractable must not inflate the denominator.
	accumulator.addDocument("및 1 2026")
	if accumulator.total != 2 {
		t.Errorf("total = %d after an empty document, want 2", accumulator.total)
	}
}

func TestTokenizeLineHandlesPunctuationAndMixedScripts(t *testing.T) {
	tokens := tokenizeLine("AI-게이트웨이(v2): 인증/연동 완료!")
	joined := strings.Join(tokens, ",")
	for _, want := range []string{"게이트웨이", "인증", "연동", "완료"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tokens %q missing %q", joined, want)
		}
	}
}

func TestAmbiguousParticleOnlyStrippedWhenTheStemIsAttested(t *testing.T) {
	// "게이트웨이" ends in 이 but "게이트웨" is not a word, so it must stay whole.
	// "시스템의" ends in 의 and "시스템" is attested, so the particle goes.
	accumulator := newTermAccumulator()
	for index := 0; index < 3; index++ {
		accumulator.addDocument("게이트웨이 구축\n시스템 점검\n시스템의 상태\n성과 측정")
	}
	counts := accumulator.counted()
	if counts["게이트웨"] != 0 {
		t.Errorf("a noun ending in 이 was truncated: %#v", counts)
	}
	if counts["게이트웨이"] == 0 {
		t.Errorf("the whole noun is missing: %#v", counts)
	}
	if counts["시스템"] < 6 {
		t.Errorf("시스템 count = %d, want the particle form folded in", counts["시스템"])
	}
	if counts["시스템의"] != 0 {
		t.Errorf("the particle form survived: %#v", counts)
	}
	// 성과 must not become 성: the stem is not attested.
	if counts["성과"] == 0 {
		t.Errorf("성과 was truncated: %#v", counts)
	}
}

func TestSublinearFrequencyLetsRareTermsCompete(t *testing.T) {
	accumulator := newTermAccumulator()
	for index := 0; index < 10; index++ {
		accumulator.addDocument(strings.Repeat("반복단어 사용 ", 12))
	}
	accumulator.addDocument("희귀단어 도입 희귀단어 도입 희귀단어 도입")
	terms := accumulator.rank(30, nil)
	rare, common := -1, -1
	for index, term := range terms {
		if rare < 0 && strings.Contains(term.Term, "희귀단어") {
			rare = index
		}
		if common < 0 && strings.Contains(term.Term, "반복단어") {
			common = index
		}
	}
	if rare < 0 {
		t.Fatalf("the rare term is missing: %#v", terms)
	}
	if common >= 0 && common < rare {
		t.Errorf("raw repetition beat the distinctive term: rare=%d common=%d", rare, common)
	}
}

// Korean compound nouns are written with and without a space by different
// authors. Both spellings are the same concept and must not split the cloud.
func TestSpacingVariantsMergeIntoOneTerm(t *testing.T) {
	accumulator := newTermAccumulator()
	for range 3 {
		accumulator.addDocument("전표검증 자동화를 진행했습니다\n전표검증 결과를 확인했습니다")
	}
	for range 2 {
		accumulator.addDocument("전표 검증 절차를 정리했습니다\n전표 검증 담당자를 지정했습니다")
	}
	terms := accumulator.rank(30, nil)
	found := map[string]analysisTerm{}
	for _, term := range terms {
		found[term.Term] = term
	}
	if _, split := found["전표 검증"]; split {
		if _, also := found["전표검증"]; also {
			t.Fatalf("both spellings survived as separate terms: %+v", terms)
		}
	}
	merged, ok := found["전표검증"]
	if !ok {
		t.Fatalf("the more frequent spelling should be the canonical term, got %+v", terms)
	}
	if merged.Count < 10 {
		t.Errorf("merged count = %d, want every occurrence of both spellings", merged.Count)
	}
	if len(merged.Variants) == 0 {
		t.Errorf("the folded spelling should be reported as a variant, got %+v", merged)
	}
}

// The measured counter-case: these score 0.600 on trigram similarity, higher
// than the spelling variants above, and are different work. Merging by exact
// match after removing spaces keeps them apart where a threshold would not.
func TestDistinctTermsWithSharedWordsAreNotMerged(t *testing.T) {
	accumulator := newTermAccumulator()
	for range 4 {
		accumulator.addDocument("서버알파 점검 완료\n서버베타 점검 완료")
	}
	terms := accumulator.rank(50, nil)
	seen := map[string]bool{}
	for _, term := range terms {
		seen[term.Term] = true
		if len(term.Variants) > 0 {
			t.Errorf("%q must not absorb %v", term.Term, term.Variants)
		}
	}
	if !seen["서버알파 점검"] || !seen["서버베타 점검"] {
		t.Errorf("both distinct terms should survive, got %+v", terms)
	}
}
