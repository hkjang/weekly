package app

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// One hue family carries one meaning across the whole app. The rule exists
// because the two axes appear on the same screen: 기간 보고 화면은 업무 수행
// 상태를 칠한 차트와 보고서 결재 상태를 칠한 배지를 같은 카드에 놓는다.
//
// It was broken in both directions before this test existed. 제출됨 was the
// amber that charts use for 정체, and 마감 was the blue that charts use for
// 진행 — so orange meant "정상" on one half of the screen and "문제" on the
// other. The part-to-whole series palette also opened with the exact hex of
// 진행 and closed with the exact hex of 정체, which the chart file's own
// comment forbade.
//
// The palette is pinned here rather than derived, because classifying a hex
// into a hue family by arithmetic gets slate wrong (Tailwind's greys are
// blue-tinted) and light tints wrong (a 10% blue has almost no channel
// spread). A table forces whoever changes a colour to state what it means.
func TestColourVocabularyIsSharedAcrossBadgesAndCharts(t *testing.T) {
	// token -> what it means, and the exact colour it is. Both are pinned: the
	// meaning so two meanings cannot share a hue, the hex so changing a colour
	// fails here until whoever changed it has re-read the rule above. The
	// original bug would not have been caught by the meaning alone — 제출됨 was
	// amber #fef3c7 against 정체's #d97706, different hexes saying the same
	// wrong thing.
	type colour struct{ meaning, hex string }
	palette := map[string]colour{
		"--state-completed":      {"완료", "#16a34a"},
		"--state-progress":       {"진행", "#2563eb"},
		"--state-not-started":    {"멈춤", "#94a3b8"},
		"--state-stalled":        {"정체", "#d97706"},
		"--state-stalled-bg":     {"정체", "#fef3c7"},
		"--state-stalled-ink":    {"정체", "#92400e"},
		"--state-risk":           {"문제", "#dc2626"},
		"--status-draft-bg":      {"멈춤", "#f1f5f9"},
		"--status-draft-ink":     {"멈춤", "#475569"},
		"--status-submitted-bg":  {"진행", "#dbeafe"}, // 결재가 진행 중이라는 뜻이므로 진행과 같은 파랑
		"--status-submitted-ink": {"진행", "#1d4ed8"},
		"--status-revision-bg":   {"문제", "#fee2e2"},
		"--status-revision-ink":  {"문제", "#b91c1c"},
		"--status-approved-bg":   {"완료", "#dcfce7"},
		"--status-approved-ink":  {"완료", "#166534"},
		"--status-closed-bg":     {"멈춤", "#cbd5e1"}, // 마감은 움직이지 않으므로 회색
		"--status-closed-ink":    {"멈춤", "#1e293b"},
		"--series-1":             {"구성비", "#3b0764"},
		"--series-2":             {"구성비", "#5b21b6"},
		"--series-3":             {"구성비", "#7c3aed"},
		"--series-4":             {"구성비", "#a78bfa"},
		"--series-5":             {"구성비", "#c4b5fd"},
		"--series-6":             {"구성비", "#ddd6fe"},
		"--trend-new":            {"신규", "#0d9488"},
		"--trend-up":             {"진행", "#2563eb"},
		"--trend-flat":           {"멈춤", "#64748b"},
		"--trend-down":           {"멈춤", "#94a3b8"},
	}

	css, err := os.ReadFile("../../frontend/src/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`(--(?:state|status|series|trend)-[a-z0-9-]+):(#[0-9a-f]{3,8})`)
	found := map[string]string{}
	for _, match := range token.FindAllStringSubmatch(string(css), -1) {
		found[match[1]] = match[2]
	}
	if len(found) == 0 {
		t.Fatal("no palette tokens found in styles.css; did :root move?")
	}
	for name, hex := range found {
		want, ok := palette[name]
		if !ok {
			t.Errorf("%s has no declared meaning.\n"+
				"Add it to this table with what it means, and check no other meaning already uses that hue.", name)
			continue
		}
		if hex != want.hex {
			t.Errorf("%s changed from %s to %s.\n"+
				"It means %q. Confirm the new colour still reads as %q and nothing else does, then update this table.",
				name, want.hex, hex, want.meaning, want.meaning)
		}
	}
	for name := range palette {
		if _, ok := found[name]; !ok {
			t.Errorf("%s is declared here but no longer defined in styles.css", name)
		}
	}
	// The same hex must not carry two meanings.
	byHex := map[string][]string{}
	for name, hex := range found {
		byHex[hex] = append(byHex[hex], name)
	}
	for hex, names := range byHex {
		first := palette[names[0]].meaning
		for _, name := range names[1:] {
			if palette[name].meaning != first {
				t.Errorf("%s is %s for %s and %s for %s.\n"+
					"A colour says one thing. Pick a different hue for one of them.",
					hex, first, names[0], palette[name].meaning, name)
			}
		}
	}
}

// Chart fills and badge backgrounds drifted apart because each file wrote its
// own hex. They now both read the tokens, and this keeps it that way.
func TestChartsReadTheSharedPaletteRatherThanTheirOwnHexes(t *testing.T) {
	// Chrome that is not data: axis labels, grid lines, and the white halo
	// drawn around a mark so it stays visible on top of its own bar.
	allowed := map[string]bool{"#94a3b8": true, "#eef2f7": true, "#fff": true, "#334155": true, "#475569": true}
	hex := regexp.MustCompile(`'#[0-9a-fA-F]{3,6}'|"#[0-9a-fA-F]{3,6}"`)
	for _, name := range []string{"charts.tsx", "WordCloud.tsx"} {
		body, err := os.ReadFile("../../frontend/src/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range hex.FindAllString(string(body), -1) {
			value := strings.ToLower(strings.Trim(match, `'"`))
			if allowed[value] {
				continue
			}
			t.Errorf("%s writes the literal colour %s.\n"+
				"Data colours come from the tokens in styles.css (var(--state-…), var(--series-…)); "+
				"add it to the chrome allow list only if it carries no meaning.", name, value)
		}
	}
}

// A class defined twice with different colours is a bug that compiles: the
// later rule wins everywhere and the earlier one is dead, so the feature that
// wrote it sees a colour it never chose.
//
// This is not hypothetical. .delta-up and .delta-down were each written twice —
// once for 용어 언급 증감 (blue/grey, the trend axis) and once for 진척 증감
// (green/red) — and the second pair silently repainted the first. The word
// cloud's rising terms had been drawn in the falling colour for as long as both
// rules existed.
func TestNoClassIsDefinedTwiceWithDifferentColours(t *testing.T) {
	body, err := os.ReadFile("../../frontend/src/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	paint := regexp.MustCompile(`(?:^|;)\s*(color|background|background-color|border-color)\s*:\s*([^;]+)`)
	seen := map[string]map[string]string{}
	// Two spellings of one colour are not a conflict. var(--blue) and #2563eb
	// are the same paint, and `background:none` and `background:transparent`
	// both mean no paint. Only a genuinely different colour is a bug.
	tokenValue := map[string]string{}
	for _, match := range regexp.MustCompile(`(--[a-z0-9-]+):(#[0-9a-f]{3,8})`).FindAllStringSubmatch(string(body), -1) {
		tokenValue["var("+match[1]+")"] = match[2]
	}
	normalise := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		if resolved, ok := tokenValue[value]; ok {
			return resolved
		}
		if value == "none" {
			return "transparent"
		}
		return value
	}

	// The stylesheet is minified onto a few very long lines, so it is walked
	// rather than matched: the whole selector has to be the key. Matching only
	// the last fragment collapsed every ".tab.active", ".chip.active" and
	// ".row.active" into one ".active" and reported them as conflicts.
	css := string(body)
	context, selector := "", strings.Builder{}
	depth := 0
	for index := 0; index < len(css); index++ {
		switch css[index] {
		case '{':
			text := strings.Join(strings.Fields(selector.String()), " ")
			selector.Reset()
			if strings.HasPrefix(text, "@") {
				context = text
				depth++
				continue
			}
			close := strings.IndexByte(css[index:], '}')
			if close < 0 {
				index = len(css)
				continue
			}
			block := css[index+1 : index+close]
			index += close
			for _, part := range strings.Split(text, ",") {
				key := context + "|" + strings.TrimSpace(part)
				for _, declaration := range paint.FindAllStringSubmatch(block, -1) {
					property, value := declaration[1], normalise(declaration[2])
					if seen[key] == nil {
						seen[key] = map[string]string{}
					}
					if earlier, repeated := seen[key][property]; repeated && earlier != value {
						t.Errorf("%s is defined twice with different %s (%q then %q).\n"+
							"The later rule wins everywhere, so whichever feature wrote the earlier one "+
							"is showing a colour it did not choose. Give one of them its own class name.",
							key, property, earlier, value)
					}
					seen[key][property] = value
				}
			}
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 {
					context = ""
				}
			}
		default:
			selector.WriteByte(css[index])
		}
	}
}
