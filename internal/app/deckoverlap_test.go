package app

import (
	"regexp"
	"strconv"
	"testing"
)

// TestTheAskReachesTheDeckAndStaysApartFromTheIssue checks that both headings
// and both texts are on the page. Neither says where. When both blocks are
// present the issue block takes half the height so the ask block has somewhere
// to go — remove that and the ask is drawn on top of the issue, with every
// string still in the file and every assertion still passing. A slide is
// geometry, and a test that only reads the text cannot see it.

// Anchored on cNvPr: the geometry preset carries a name="adj" attribute of its
// own, and matching that one walks the offsets one shape out of step.
var shapeGeometry = regexp.MustCompile(
	`<p:cNvPr id="\d+" name="([A-Za-z]+)"/>[\s\S]*?<a:off x="(-?\d+)" y="(-?\d+)"/><a:ext cx="(\d+)" cy="(\d+)"/>`)

// blockBox returns the top and height of the named shape.
func blockBox(t *testing.T, shapes, name string) (int, int) {
	t.Helper()
	for _, match := range shapeGeometry.FindAllStringSubmatch(shapes, -1) {
		if match[1] != name {
			continue
		}
		top, err := strconv.Atoi(match[3])
		if err != nil {
			t.Fatalf("%s top: %v", name, err)
		}
		height, err := strconv.Atoi(match[5])
		if err != nil {
			t.Fatalf("%s height: %v", name, err)
		}
		return top, height
	}
	t.Fatalf("the page has no shape named %s: %s", name, shapes)
	return 0, 0
}

// guards: issueSlide
func TestTheAskBlockIsBelowTheIssueBlockAndNotOnTopOfIt(t *testing.T) {
	width, height := 12192000, 6858000

	both, has := issueSlide([]reportItem{
		{Category: "인프라", Title: "회선 이설", Issue: "임대 일정 지연", ManagementAsk: "사업부 예산 승인"},
	}, width, height)
	if !has {
		t.Fatal("an issue and an ask together produced no page")
	}

	issueTop, issueHeight := blockBox(t, both.Shapes, "IssueBody")
	askTop, askHeight := blockBox(t, both.Shapes, "AskBody")
	if issueHeight <= 0 || askHeight <= 0 {
		t.Fatalf("a block with no height: 이슈 %d, 요청 %d", issueHeight, askHeight)
	}
	if askTop < issueTop+issueHeight {
		t.Errorf("요청 블록이 이슈 블록 위에 겹칩니다: 이슈 %d..%d, 요청 %d 부터",
			issueTop, issueTop+issueHeight, askTop)
	}
	if askTop+askHeight > height {
		t.Errorf("요청 블록이 슬라이드 아래로 넘어갑니다: %d > %d", askTop+askHeight, height)
	}

	// With no ask to make room for, the issue block keeps the whole area — the
	// other half of the same decision.
	alone, has := issueSlide([]reportItem{{Title: "회선 이설", Issue: "임대 일정 지연"}}, width, height)
	if !has {
		t.Fatal("an issue alone produced no page")
	}
	_, aloneHeight := blockBox(t, alone.Shapes, "IssueBody")
	if aloneHeight <= issueHeight {
		t.Errorf("이슈만 있을 때가 더 좁습니다: 혼자 %d, 요청과 함께 %d", aloneHeight, issueHeight)
	}
}
