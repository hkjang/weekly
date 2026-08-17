package app

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Term extraction for the administrator word cloud and keyword trends.
//
// A Korean morphological analyser needs a database extension or an external
// service, neither of which an offline site can assume. This uses a rule based
// approach instead: strip the particles and endings that attach to a stem, keep
// phrases of two adjacent tokens so compound terms survive, and let inverse
// document frequency push ubiquitous reporting boilerplate down rather than
// relying on an ever growing stopword list.

// unambiguousSuffixes are multi-character particles and verb endings. They are
// long enough that they never form the tail of a real noun, so they can be
// stripped on sight. Longest first, so the longest match wins.
var unambiguousSuffixes = []string{
	"하였습니다", "되었습니다", "했습니다", "합니다", "됩니다", "입니다", "있습니다", "없습니다",
	"으로서", "으로써", "에서는", "에게서", "이라고", "에서의", "으로의",
	"하였고", "하였다", "되었다", "하면서", "하도록", "하기로",
	"라고", "으로", "에서", "에게", "부터", "까지", "보다", "처럼", "마다",
	"이나", "조차", "밖에", "만큼", "이며", "이고", "와의", "과의", "로의", "에의",
	"에는", "에도", "이란", "라는", "이는", "들이", "들을", "들의",
	"하고", "하여", "해서", "했고", "되고", "되어",
}

// objectSuffixes are single characters that practically never end a Korean noun
// of three or more syllables, so they can be removed once the stem is long
// enough to stand on its own. "규칙을" is always 규칙 + 을.
var objectSuffixes = []string{"을", "를", "은", "는", "에"}

// ambiguousSuffixes are single characters that are particles in one word and the
// final syllable of a noun in another: 게이트웨"이", 성"과", 진척"도", 회"의".
// Stripping them blindly corrupts common terms, so they are only removed when
// the remaining stem is itself attested elsewhere in the same corpus.
var ambiguousSuffixes = []string{"이", "가", "의", "로", "와", "과", "도", "만"}

// analysisStopwords are function words and weekly report boilerplate that carry
// no information about what the work actually was.
var analysisStopwords = map[string]bool{
	"그리고": true, "그러나": true, "또한": true, "하지만": true, "따라서": true, "때문": true,
	"이번": true, "다음": true, "금주": true, "차주": true, "지난": true, "현재": true,
	"주간": true, "보고": true, "업무": true, "내용": true, "사항": true, "관련": true,
	"예정": true, "계획": true, "실적": true, "진행": true, "수행": true, "필요": true,
	"각각": true, "모두": true, "해당": true, "기타": true, "이상": true, "이하": true,
	"있음": true, "없음": true, "요약": true, "여부": true, "부분": true, "경우": true, "결과": true,
	"대한": true, "대해": true, "통해": true, "위해": true, "및": true,
	"the": true, "and": true, "for": true, "with": true, "from": true, "this": true,
	"that": true, "was": true, "are": true, "not": true, "has": true, "have": true,
}

type analysisTerm struct {
	Term      string  `json:"term"`
	Count     int     `json:"count"`
	Documents int     `json:"documents"`
	Weight    float64 `json:"weight"`
	Delta     int     `json:"delta"`
	Phrase    bool    `json:"phrase"`
}

// surfaceToken lowercases a token and removes the suffixes that can never be
// part of a noun. Ambiguous single characters are left in place for now.
func surfaceToken(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return ""
	}
	numeric := true
	for _, r := range token {
		if !unicode.IsDigit(r) {
			numeric = false
			break
		}
	}
	if numeric {
		return ""
	}
	for _, suffix := range unambiguousSuffixes {
		if !strings.HasSuffix(token, suffix) {
			continue
		}
		trimmed := strings.TrimSuffix(token, suffix)
		if len([]rune(trimmed)) >= 2 {
			token = trimmed
			break
		}
		// The token is nothing but an ending, so there is no term in it.
		return ""
	}
	if len([]rune(token)) < 2 || analysisStopwords[token] {
		return ""
	}
	return token
}

// stripAmbiguousSuffix removes a trailing single-character particle only when
// the stem is attested, which is what tells "시스템의" (particle) apart from
// "회의" (noun). An unattested stem means the character belongs to the word.
//
// A stem counts as attested when it appears bare, or when it appears with two
// different particles: "규칙을" plus "규칙의" is paradigm evidence for "규칙"
// even when the bare form never occurs in the corpus.
func stripAmbiguousSuffix(token string, attested map[string]bool) string {
	return stripAmbiguousSuffixWithParadigm(token, attested, nil)
}

func stripAmbiguousSuffixWithParadigm(token string, attested map[string]bool, paradigm map[string]map[string]bool) string {
	for _, suffix := range objectSuffixes {
		if !strings.HasSuffix(token, suffix) {
			continue
		}
		if stem := strings.TrimSuffix(token, suffix); len([]rune(stem)) >= 2 && !analysisStopwords[stem] {
			return stem
		}
		break
	}
	for _, suffix := range ambiguousSuffixes {
		if !strings.HasSuffix(token, suffix) {
			continue
		}
		stem := strings.TrimSuffix(token, suffix)
		if len([]rune(stem)) < 2 || analysisStopwords[stem] {
			break
		}
		if attested[stem] || len(paradigm[stem]) >= 2 {
			return stem
		}
		break
	}
	return token
}

// tokenizeLine splits one line into surface tokens, preserving order so that
// adjacent pairs can be joined into phrases.
func tokenizeLine(line string) []string {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if token := surfaceToken(field); token != "" {
			result = append(result, token)
		}
	}
	return result
}

// documentTokens is one document reduced to lines of tokens. Phrases are built
// within a line only, because two words either side of a line break are not
// related.
type documentTokens [][]string

func tokenizeDocument(text string) documentTokens {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	result := documentTokens{}
	for _, line := range strings.Split(normalized, "\n") {
		if tokens := tokenizeLine(line); len(tokens) > 0 {
			result = append(result, tokens)
		}
	}
	return result
}

type termAccumulator struct {
	attested  map[string]bool
	paradigm  map[string]map[string]bool
	counts    map[string]int
	documentF map[string]int
	phrase    map[string]bool
	total     int
	docTokens []documentTokens
}

func newTermAccumulator() *termAccumulator {
	return &termAccumulator{attested: map[string]bool{}, paradigm: map[string]map[string]bool{},
		counts: map[string]int{}, documentF: map[string]int{}, phrase: map[string]bool{}}
}

// addDocument records a document and the surface forms it contributes to the
// vocabulary. Counting is deferred until rank, when the whole corpus is known.
func (a *termAccumulator) addDocument(text string) {
	tokens := tokenizeDocument(text)
	if len(tokens) == 0 {
		return
	}
	a.total++
	a.docTokens = append(a.docTokens, tokens)
	for _, line := range tokens {
		for _, token := range line {
			a.attested[token] = true
			for _, suffix := range ambiguousSuffixes {
				if !strings.HasSuffix(token, suffix) {
					continue
				}
				if stem := strings.TrimSuffix(token, suffix); len([]rune(stem)) >= 2 {
					if a.paradigm[stem] == nil {
						a.paradigm[stem] = map[string]bool{}
					}
					a.paradigm[stem][suffix] = true
				}
				break
			}
		}
	}
}

// resolve applies corpus-aware normalization and counts unigrams and phrases.
func (a *termAccumulator) resolve() {
	if len(a.counts) > 0 || len(a.docTokens) == 0 {
		return
	}
	for _, document := range a.docTokens {
		seen := map[string]bool{}
		for _, line := range document {
			resolved := make([]string, 0, len(line))
			for _, token := range line {
				stem := stripAmbiguousSuffixWithParadigm(token, a.attested, a.paradigm)
				if len([]rune(stem)) >= 2 && !analysisStopwords[stem] {
					resolved = append(resolved, stem)
				}
			}
			for index, token := range resolved {
				a.counts[token]++
				seen[token] = true
				if index+1 < len(resolved) {
					phrase := token + " " + resolved[index+1]
					a.counts[phrase]++
					a.phrase[phrase] = true
					seen[phrase] = true
				}
			}
		}
		for term := range seen {
			a.documentF[term]++
		}
	}
}

// rank scores terms and drops the ones a reader would not act on.
//
// Term frequency is scaled sublinearly: a word repeated twenty times in one
// report is not twenty times as interesting as one used once, and without the
// scaling raw repetition drowns out the inverse document frequency signal
// entirely.
func (a *termAccumulator) rank(limit int, previous map[string]int) []analysisTerm {
	a.resolve()
	if a.total == 0 {
		return []analysisTerm{}
	}
	terms := make([]analysisTerm, 0, len(a.counts))
	for term, count := range a.counts {
		documents := a.documentF[term]
		if documents == 0 {
			documents = 1
		}
		isPhrase := a.phrase[term]
		// A phrase seen once is noise; a lone word in a small corpus is not.
		if isPhrase && count < 2 {
			continue
		}
		if count < 2 && a.total > 6 {
			continue
		}
		frequency := 1 + math.Log(float64(count))
		inverse := math.Log(1 + float64(a.total)/float64(documents))
		weight := frequency * inverse
		if isPhrase {
			// Compounds say more than their parts, so they are not penalised
			// for being rarer than the words they contain.
			weight *= 1.35
		}
		terms = append(terms, analysisTerm{
			Term: term, Count: count, Documents: documents,
			Weight: math.Round(weight*100) / 100,
			Delta:  count - previous[term], Phrase: isPhrase,
		})
	}
	terms = suppressRedundantParts(terms)
	sort.SliceStable(terms, func(left, right int) bool {
		if terms[left].Weight != terms[right].Weight {
			return terms[left].Weight > terms[right].Weight
		}
		if terms[left].Count != terms[right].Count {
			return terms[left].Count > terms[right].Count
		}
		return terms[left].Term < terms[right].Term
	})
	if limit > 0 && len(terms) > limit {
		terms = terms[:limit]
	}
	return terms
}

// counted exposes the resolved occurrence table for the comparison window.
func (a *termAccumulator) counted() map[string]int {
	a.resolve()
	return a.counts
}

// suppressRedundantParts removes a single word when one phrase accounts for
// most of its occurrences, so "ai", "게이트웨이" and "ai 게이트웨이" do not all
// crowd the same cloud. A word used across many different phrases survives,
// because no single phrase covers it.
func suppressRedundantParts(terms []analysisTerm) []analysisTerm {
	phraseCoverage := map[string]int{}
	for _, term := range terms {
		if !term.Phrase {
			continue
		}
		for _, part := range strings.Split(term.Term, " ") {
			if term.Count > phraseCoverage[part] {
				phraseCoverage[part] = term.Count
			}
		}
	}
	result := make([]analysisTerm, 0, len(terms))
	for _, term := range terms {
		if !term.Phrase && phraseCoverage[term.Term] >= (term.Count*3+4)/5 {
			continue
		}
		result = append(result, term)
	}
	return result
}
