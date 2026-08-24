package app

import (
	"strconv"
	"strings"
)

// Issues written in the editor never reached the built-in deck.
//
// The token contract has always had {{ISSUES}}, so an organisation that uploads
// its own template gets them. The reference template that ships with the product
// has two content columns — 추진실적 and 추진계획 — and no third place, so on
// every default installation the issue an author wrote went into the database
// and not into the room. An issue that is not in the meeting material does not
// get raised at the meeting.
//
// Rather than change the shape of a page organisations have standardised on,
// the issues get a page of their own, appended only when there are any.
const (
	issueSlideMargin     = 640000
	issueSlideTitleTop   = 520000
	issueSlideBodyTop    = 1180000
	issueSlideLinesShown = 22
)

// issueSlide builds the 이슈 및 애로사항 page, and reports whether there is
// anything to put on it.
func issueSlide(items []reportItem, width, height int) (builtSlide, bool) {
	runs := issueRuns(items)
	if len(runs) == 0 {
		return builtSlide{}, false
	}
	contentWidth := width - 2*issueSlideMargin
	shapes := textBox(2, "IssueBand", 0, 0, width, 320000, shapeStyle{Fill: "B45309"}, nil)
	shapes += textBox(3, "IssueTitle", issueSlideMargin, issueSlideTitleTop, contentWidth, 520000,
		shapeStyle{}, []textRun{{Text: "이슈 및 애로사항", Size: 2400, Bold: true, Color: "7C2D12"}})
	shapes += textBox(4, "IssueBody", issueSlideMargin, issueSlideBodyTop, contentWidth,
		height-issueSlideBodyTop-issueSlideMargin, shapeStyle{Fill: "FFFBEB", Line: "FDE68A", Radius: true}, runs)
	return builtSlide{Shapes: shapes}, true
}

// issueRuns turns each item's issue into a heading line and its details, in the
// order the report lists them.
func issueRuns(items []reportItem) []textRun {
	runs := []textRun{}
	for _, item := range items {
		content := strings.TrimSpace(item.Issue)
		if content == "" {
			continue
		}
		heading := strings.TrimSpace(item.Title)
		if category := strings.TrimSpace(item.Category); category != "" {
			heading = "[" + category + "] " + heading
		}
		if heading == "" {
			heading = "제목 없음"
		}
		spacing := 0
		if len(runs) > 0 {
			spacing = 120000
		}
		runs = append(runs, textRun{Text: heading, Size: 1300, Bold: true, Color: "7C2D12",
			Bullet: "•", SpaceBefore: spacing})
		for _, detail := range reportContentLines(content) {
			runs = append(runs, textRun{Text: detail, Size: 1200, Color: "44403C", Indent: 1, Bullet: "-"})
			if len(runs) >= issueSlideLinesShown {
				break
			}
		}
		if len(runs) >= issueSlideLinesShown {
			// A page holds what a page holds. Saying how much is left beats
			// letting the rest fall off the bottom without a word.
			remaining := countIssues(items) - countIssueHeadings(runs)
			if remaining > 0 {
				runs = append(runs, textRun{
					Text:  "이 페이지에 담지 못한 이슈가 " + strconv.Itoa(remaining) + "건 더 있습니다. 주간보고 화면에서 전체를 확인하세요.",
					Size:  1100,
					Color: "9A3412", SpaceBefore: 160000})
			}
			break
		}
	}
	return runs
}

func countIssues(items []reportItem) int {
	total := 0
	for _, item := range items {
		if strings.TrimSpace(item.Issue) != "" {
			total++
		}
	}
	return total
}

func countIssueHeadings(runs []textRun) int {
	headings := 0
	for _, run := range runs {
		if run.Bold {
			headings++
		}
	}
	return headings
}
