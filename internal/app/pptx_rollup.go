package app

import (
	"fmt"
	"sort"
	"strings"
)

// The weekly deck has a fixed four slide table. A period rollup covers up to a
// year of merged work, so pouring it into that fixed frame overflows the cells
// and prints unreadable slides. The rollup gets its own layout instead: a
// cover, the executive insights, a scannable status table and detail pages,
// each paginated so no slide is ever overfull.

const (
	rollupMargin        = 480000
	rollupCanvasWidth   = defaultSlideCX
	rollupCanvasHeight  = defaultSlideCY
	rollupContentTop    = 1180000
	rollupContentBottom = 6420000
	rollupFooterY       = 6470000
)

var rollupContentWidth = rollupCanvasWidth - 2*rollupMargin

// rollupDetailBudget caps how much of each item's text reaches the deck. A
// month can afford full detail; a year has to stay skimmable, and whatever is
// dropped is announced rather than silently cut.
func rollupDetailBudget(kind string) (linesPerItem int, linesPerSlide int) {
	// linesPerSlide is measured in rendered lines and stays just under the real
	// capacity of the detail column so wrapped text still fits.
	switch kind {
	case periodYear:
		return 3, 28
	case periodHalf:
		return 4, 28
	case periodQuarter:
		return 5, 28
	default:
		return 8, 28
	}
}

// buildRollupDeck lays out the whole period report.
func buildRollupDeck(view rollupView) ([]byte, error) {
	slides := []builtSlide{coverSlide(view)}
	slides = append(slides, insightSlides(view)...)
	slides = append(slides, statusTableSlides(view)...)
	slides = append(slides, detailSlides(view)...)
	// Decisions before risk: the period explains itself, then asks. A deck that
	// ends on the ask is one the room can act on.
	if asks := askSlide(view); asks != nil {
		slides = append(slides, *asks)
	}
	if decisions := decisionSlide(view); decisions != nil {
		slides = append(slides, *decisions)
	}
	if risk := riskSlide(view); risk != nil {
		slides = append(slides, *risk)
	}
	title := fmt.Sprintf("%s %s 업무보고", view.Label, view.ScopeLabel)
	return buildPPTX(rollupCanvasWidth, rollupCanvasHeight, title, slides)
}

func rollupHeader(id int, title, subtitle string) string {
	shapes := textBox(id, "Title", rollupMargin, 330000, rollupContentWidth-2600000, 620000,
		shapeStyle{}, []textRun{{Text: title, Size: 2200, Bold: true, Color: "0F172A"}})
	if subtitle != "" {
		shapes += textBox(id+1, "Subtitle", rollupMargin+rollupContentWidth-2600000, 380000, 2600000, 520000,
			shapeStyle{}, []textRun{{Text: subtitle, Size: 1100, Color: "64748B"}})
	}
	return shapes
}

func rollupFooter(id int, left, right string) string {
	return textBox(id, "Footer", rollupMargin, rollupFooterY, rollupContentWidth-1800000, 300000,
		shapeStyle{}, []textRun{{Text: left, Size: 900, Color: "94A3B8"}}) +
		textBox(id+1, "PageLabel", rollupMargin+rollupContentWidth-1800000, rollupFooterY, 1800000, 300000,
			shapeStyle{}, []textRun{{Text: right, Size: 900, Color: "94A3B8"}})
}

func coverSlide(view rollupView) builtSlide {
	insights := view.Insights
	shapes := textBox(2, "Band", 0, 0, rollupCanvasWidth, 2450000, shapeStyle{Fill: "172554"}, nil)
	shapes += textBox(3, "Kind", rollupMargin, 620000, 6000000, 340000, shapeStyle{},
		[]textRun{{Text: rollupKindLabel(view.Kind) + " 업무보고", Size: 1200, Bold: true, Color: "7DD3FC"}})
	shapes += textBox(4, "Period", rollupMargin, 960000, 8000000, 700000, shapeStyle{},
		[]textRun{{Text: view.Label + " · " + view.ScopeLabel, Size: 2800, Bold: true, Color: "FFFFFF"}})
	shapes += textBox(5, "Range", rollupMargin, 1680000, 8000000, 380000, shapeStyle{},
		[]textRun{{Text: fmt.Sprintf("%s ~ %s · 주간보고 %d건 취합", view.Start, view.End, insights.SourceReports), Size: 1200, Color: "BFDBFE"}})

	tiles := []struct{ label, value, note string }{
		{"완료율", fmt.Sprintf("%.1f%%", insights.CompletionRate), fmt.Sprintf("%d / %d건 완료", insights.CompletedItems, insights.TotalItems)},
		{"평균 진척도", fmt.Sprintf("%.1f%%", insights.AverageProgress), fmt.Sprintf("기간 중 %+.1f%%p", insights.ProgressGain)},
		{"이슈 업무", fmt.Sprint(insights.IssueItems), fmt.Sprintf("이슈 지속 %d건", insights.PersistentIssues)},
		{"보고 커버리지", fmt.Sprintf("%.0f%%", insights.ReportCoverage), fmt.Sprintf("%d개 주차 중 %d개", insights.ExpectedWeeks, insights.ReportedWeeks)},
	}
	tileWidth := (rollupContentWidth - 3*180000) / 4
	id := 10
	for index, tile := range tiles {
		x := rollupMargin + index*(tileWidth+180000)
		shapes += textBox(id, "Tile", x, 2720000, tileWidth, 1320000,
			shapeStyle{Fill: "FFFFFF", Line: "E2E8F0", Radius: true}, []textRun{
				{Text: tile.label, Size: 1000, Bold: true, Color: "64748B"},
				{Text: tile.value, Size: 2400, Bold: true, Color: "0F172A", SpaceBefore: 300},
				{Text: tile.note, Size: 900, Color: "94A3B8", SpaceBefore: 200},
			})
		id += 2
	}
	shapes += textBox(id, "SummaryHead", rollupMargin, 4300000, rollupContentWidth, 320000, shapeStyle{},
		[]textRun{{Text: "종합 요약", Size: 1200, Bold: true, Color: "1E293B"}})
	shapes += textBox(id+1, "Summary", rollupMargin, 4640000, rollupContentWidth, 1560000,
		shapeStyle{Fill: "F8FAFC", Line: "E2E8F0", Radius: true},
		[]textRun{{Text: view.Summary, Size: 1200, Color: "334155"}})
	shapes += rollupFooter(id+3, fmt.Sprintf("중복 %d건 제거 · 동일 업무 %d건 병합", view.Insights.DuplicatesCut, view.Insights.MergedTitles), "")
	return builtSlide{Shapes: shapes}
}

func insightSlides(view rollupView) []builtSlide {
	if len(view.Highlights) == 0 {
		return nil
	}
	const perSlide = 6
	pages := chunkHighlights(view.Highlights, perSlide)
	result := make([]builtSlide, 0, len(pages))
	for pageIndex, page := range pages {
		subtitle := ""
		if len(pages) > 1 {
			subtitle = fmt.Sprintf("%d / %d", pageIndex+1, len(pages))
		}
		shapes := rollupHeader(2, "경영 인사이트", subtitle)
		cardWidth := (rollupContentWidth - 200000) / 2
		cardHeight := 1560000
		id := 10
		for index, highlight := range page {
			column := index % 2
			row := index / 2
			x := rollupMargin + column*(cardWidth+200000)
			y := rollupContentTop + row*(cardHeight+140000)
			border, badge := highlightColors(highlight.Severity)
			shapes += textBox(id, "Insight", x, y, cardWidth, cardHeight,
				shapeStyle{Fill: "FFFFFF", Line: border, Radius: true}, []textRun{
					{Text: highlightSeverityLabel(highlight.Severity) + " · " + highlight.Title, Size: 1200, Bold: true, Color: badge},
					{Text: highlight.Detail, Size: 1000, Color: "475569", SpaceBefore: 300},
				})
			id += 2
		}
		shapes += rollupFooter(id, view.Label+" · "+view.ScopeLabel, "")
		result = append(result, builtSlide{Shapes: shapes})
	}
	return result
}

func highlightColors(severity string) (border, badge string) {
	switch severity {
	case "RISK":
		return "FCA5A5", "B91C1C"
	case "WATCH":
		return "FCD34D", "92400E"
	case "GOOD":
		return "86EFAC", "166534"
	default:
		return "BFDBFE", "1D4ED8"
	}
}

func highlightSeverityLabel(severity string) string {
	switch severity {
	case "RISK":
		return "위험"
	case "WATCH":
		return "주의"
	case "GOOD":
		return "양호"
	default:
		return "참고"
	}
}

func statusTableSlides(view rollupView) []builtSlide {
	if len(view.Items) == 0 {
		return nil
	}
	const rowsPerSlide = 11
	columns := []tableColumn{
		{Width: 1500000, Title: "구분"},
		{Width: 4700000, Title: "업무"},
		{Width: 1250000, Title: "진척도"},
		{Width: 1250000, Title: "수행 주차"},
		{Width: 1550000, Title: "상태"},
		{Width: 992000, Title: "담당"},
	}
	pages := (len(view.Items) + rowsPerSlide - 1) / rowsPerSlide
	result := make([]builtSlide, 0, pages)
	for page := 0; page < pages; page++ {
		start := page * rowsPerSlide
		end := start + rowsPerSlide
		if end > len(view.Items) {
			end = len(view.Items)
		}
		rows := make([][]tableCell, 0, end-start)
		for _, item := range view.Items[start:end] {
			state, color := rollupItemState(item)
			rows = append(rows, []tableCell{
				{Text: trimRunes(fallbackText(item.Category, "미분류"), 10)},
				{Text: trimRunes(item.Title, 34)},
				{Text: fmt.Sprintf("%d%%", item.Progress), Align: "ctr"},
				{Text: fmt.Sprintf("%d주", item.WeekCount), Align: "ctr"},
				{Text: state, Color: color, Bold: true, Align: "ctr"},
				{Text: trimRunes(strings.Join(item.Owners, ","), 8)},
			})
		}
		subtitle := ""
		if pages > 1 {
			subtitle = fmt.Sprintf("%d / %d", page+1, pages)
		}
		shapes := rollupHeader(2, "업무 현황", subtitle)
		shapes += textBox(4, "Note", rollupMargin, 940000, rollupContentWidth, 260000, shapeStyle{},
			[]textRun{{Text: fmt.Sprintf("주간보고 업무 %d건을 중복 제거해 %d건으로 정리했습니다.", view.Insights.SourceItems, view.Insights.TotalItems), Size: 950, Color: "64748B"}})
		shapes += tableShape(6, "StatusTable", rollupMargin, rollupContentTop+90000, rollupContentWidth, 400000, columns, rows)
		shapes += rollupFooter(8, view.Label+" · "+view.ScopeLabel, fmt.Sprintf("%d-%d / %d건", start+1, end, len(view.Items)))
		result = append(result, builtSlide{Shapes: shapes})
	}
	return result
}

func rollupItemState(item rollupItem) (string, string) {
	switch {
	case item.Completed:
		return "완료", "166534"
	case item.AtRisk:
		return "이슈 지속", "B91C1C"
	case item.Stalled:
		return "정체", "92400E"
	case item.Progress <= 0:
		return "미착수", "64748B"
	default:
		return "진행", "1D4ED8"
	}
}

// detailPage is one slide worth of items in the two column detail layout.
type detailPage struct {
	Items []rollupItem
}

// paginateDetail packs items into slides without ever exceeding the line budget
// of a single slide, keeping the report order intact.
func paginateDetail(items []rollupItem, linesPerItem, linesPerSlide int) []detailPage {
	pages := []detailPage{}
	current := detailPage{}
	used := 0
	for _, item := range items {
		cost := detailItemCost(item, linesPerItem)
		if used > 0 && used+cost > linesPerSlide {
			pages = append(pages, current)
			current = detailPage{}
			used = 0
		}
		current.Items = append(current.Items, item)
		used += cost
	}
	if len(current.Items) > 0 {
		pages = append(pages, current)
	}
	return pages
}

// Characters that fit on one rendered line of a detail column and of a heading.
// The columns are about 5.8 inches wide at 10pt, so a long bullet wraps and has
// to be counted as the several physical lines it actually occupies.
const (
	detailRunesPerLine  = 32
	headingRunesPerLine = 28
)

// detailItemCost counts the physical lines an item occupies. The two columns
// share a height, so the taller side decides.
func detailItemCost(item rollupItem, linesPerItem int) int {
	body := wrappedBlockLines(item.CurrentResult, linesPerItem)
	if right := wrappedBlockLines(item.NextPlan, linesPerItem); right > body {
		body = right
	}
	// The heading plus one line of separation before the next item.
	return body + 1 + wrappedLineCount(item.Title, headingRunesPerLine)
}

func wrappedBlockLines(value string, limit int) int {
	total := 0
	for _, line := range cappedLines(value, limit) {
		total += wrappedLineCount(line, detailRunesPerLine)
	}
	if total == 0 {
		return 1
	}
	return total
}

func wrappedLineCount(value string, perLine int) int {
	length := len([]rune(strings.TrimSpace(value)))
	if length == 0 || perLine <= 0 {
		return 1
	}
	return (length + perLine - 1) / perLine
}

// cappedLines returns at most limit lines and reports how many were dropped in
// a trailing marker, so a shortened slide never looks complete when it is not.
func cappedLines(value string, limit int) []string {
	lines := reportContentLines(value)
	if len(lines) <= limit {
		return lines
	}
	kept := append([]string{}, lines[:limit]...)
	return append(kept, fmt.Sprintf("외 %d건", len(lines)-limit))
}

func detailSlides(view rollupView) []builtSlide {
	if len(view.Items) == 0 {
		return nil
	}
	linesPerItem, linesPerSlide := rollupDetailBudget(view.Kind)
	pages := paginateDetail(view.Items, linesPerItem, linesPerSlide)
	columnWidth := (rollupContentWidth - 200000) / 2
	result := make([]builtSlide, 0, len(pages))
	for pageIndex, page := range pages {
		subtitle := ""
		if len(pages) > 1 {
			subtitle = fmt.Sprintf("%d / %d", pageIndex+1, len(pages))
		}
		shapes := rollupHeader(2, "업무 상세", subtitle)
		shapes += textBox(4, "LeftHead", rollupMargin, 900000, columnWidth, 380000,
			shapeStyle{Fill: "2563EB", Radius: true, AnchorMiddle: true},
			[]textRun{{Text: "기간 실적", Size: 1200, Bold: true, Color: "FFFFFF"}})
		shapes += textBox(5, "RightHead", rollupMargin+columnWidth+200000, 900000, columnWidth, 380000,
			shapeStyle{Fill: "0D9488", Radius: true, AnchorMiddle: true},
			[]textRun{{Text: "남은 계획", Size: 1200, Bold: true, Color: "FFFFFF"}})
		height := rollupContentBottom - (rollupContentTop + 180000)
		shapes += textBox(6, "LeftBody", rollupMargin, rollupContentTop+180000, columnWidth, height,
			shapeStyle{Fill: "FFFFFF", Line: "E2E8F0", Radius: true}, detailRuns(page.Items, linesPerItem, true))
		shapes += textBox(7, "RightBody", rollupMargin+columnWidth+200000, rollupContentTop+180000, columnWidth, height,
			shapeStyle{Fill: "FFFFFF", Line: "E2E8F0", Radius: true}, detailRuns(page.Items, linesPerItem, false))
		shapes += rollupFooter(8, view.Label+" · "+view.ScopeLabel, fmt.Sprintf("업무 %d건", len(page.Items)))
		result = append(result, builtSlide{Shapes: shapes})
	}
	return result
}

func detailRuns(items []rollupItem, linesPerItem int, current bool) []textRun {
	runs := []textRun{}
	lastCategory := ""
	for index, item := range items {
		category := strings.TrimSpace(item.Category)
		if category != "" && category != lastCategory {
			runs = append(runs, textRun{Text: category, Size: 1000, Bold: true, Color: "2563EB", SpaceBefore: spaceBeforeFor(index)})
			lastCategory = category
		}
		heading := item.Title
		if current {
			heading = fmt.Sprintf("%s (%d%%)", item.Title, item.Progress)
		}
		runs = append(runs, textRun{Text: heading, Size: 1050, Bold: true, Color: "0F172A", SpaceBefore: spaceBeforeFor(index)})
		source := item.NextPlan
		if current {
			source = item.CurrentResult
		}
		lines := cappedLines(source, linesPerItem)
		if len(lines) == 0 {
			runs = append(runs, textRun{Text: "-", Size: 1000, Color: "94A3B8", Indent: 1})
			continue
		}
		for _, line := range lines {
			runs = append(runs, textRun{Text: line, Size: 1000, Color: "334155", Indent: 1, Bullet: "•"})
		}
	}
	return runs
}

func spaceBeforeFor(index int) int {
	if index == 0 {
		return 0
	}
	return 500
}

func riskSlide(view rollupView) *builtSlide {
	rows := [][]tableCell{}
	for _, item := range view.Items {
		if !item.AtRisk && !item.Stalled {
			continue
		}
		reason := "이슈 " + fmt.Sprint(item.IssueWeeks) + "주 지속"
		if !item.AtRisk {
			reason = "진척 정체"
		}
		detail := firstLine(item.Issue)
		if detail == "" {
			detail = firstLine(item.NextPlan)
		}
		rows = append(rows, []tableCell{
			{Text: trimRunes(item.Title, 28)},
			{Text: reason, Color: "B91C1C", Bold: true, Align: "ctr"},
			{Text: fmt.Sprintf("%d%%", item.Progress), Align: "ctr"},
			{Text: trimRunes(detail, 46), Color: "475569"},
		})
		if len(rows) == 11 {
			break
		}
	}
	if len(rows) == 0 {
		return nil
	}
	columns := []tableColumn{
		{Width: 3200000, Title: "업무"},
		{Width: 1700000, Title: "사유"},
		{Width: 1100000, Title: "진척도"},
		{Width: 5232000, Title: "내용"},
	}
	shapes := rollupHeader(2, "이슈 · 리스크", "")
	shapes += textBox(4, "Note", rollupMargin, 940000, rollupContentWidth, 260000, shapeStyle{},
		[]textRun{{Text: "반복 보고된 이슈와 진척이 멈춘 업무입니다. 상위 의사결정이 필요한 항목을 먼저 확인하십시오.", Size: 950, Color: "64748B"}})
	shapes += tableShape(6, "RiskTable", rollupMargin, rollupContentTop+90000, rollupContentWidth, 400000, columns, rows)
	shapes += rollupFooter(8, view.Label+" · "+view.ScopeLabel, "")
	return &builtSlide{Shapes: shapes}
}

func firstLine(value string) string {
	lines := reportContentLines(value)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func rollupKindLabel(kind string) string {
	switch kind {
	case periodYear:
		return "연간"
	case periodHalf:
		return "반기"
	case periodQuarter:
		return "분기"
	default:
		return "월간"
	}
}

func chunkHighlights(items []rollupHighlight, size int) [][]rollupHighlight {
	result := [][]rollupHighlight{}
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		result = append(result, items[start:end])
	}
	return result
}

// decisionSlideRows is how many decisions fit on one slide without the table
// becoming unreadable, matched to the risk slide beside it.
const decisionSlideRows = 11

// decisionSlide is what the period decided.
//
// The screen has carried this since v0.37, but the screen is not what an
// executive receives — the deck is. A period report that lists the work and
// omits the decisions leaves its reader unable to ask why any of it went the
// way it did, and that gap does not close by being fixed only where the author
// happens to be looking.
//
// Outstanding decisions come first. A decision already carried out is history;
// one still owing something is the reason this slide is in a briefing.
func decisionSlide(view rollupView) *builtSlide {
	if len(view.Decisions) == 0 {
		return nil
	}
	ordered := append([]decisionView{}, view.Decisions...)
	sort.SliceStable(ordered, func(x, y int) bool {
		return decisionDeckRank(ordered[x].Status) < decisionDeckRank(ordered[y].Status)
	})

	rows := [][]tableCell{}
	for _, decision := range ordered {
		status, color := decisionDeckStatus(decision.Status)
		when := decision.DecidedOn
		if decision.DueDate != "" && decision.Status == decisionOpen {
			when += " · 기한 " + decision.DueDate
		}
		follow := firstLine(decision.FollowUp)
		if follow == "" {
			follow = firstLine(decision.Rationale)
		}
		rows = append(rows, []tableCell{
			{Text: trimRunes(decision.WorkTitle, 20)},
			{Text: trimRunes(decision.Title, 26)},
			// One line, not two: a table cell here is a single run, and a
			// newline inside <a:t> is not a line break in DrawingML — that
			// would need <a:br/>. Rather than rely on how a viewer treats
			// stray whitespace, the separator is explicit.
			{Text: trimRunes(decision.DecidedBy, 10) + " · " + when, Color: "475569"},
			{Text: status, Color: color, Bold: true, Align: "ctr"},
			{Text: trimRunes(follow, 34), Color: "475569"},
		})
		if len(rows) == decisionSlideRows {
			break
		}
	}
	columns := []tableColumn{
		{Width: 2300000, Title: "업무"},
		{Width: 2700000, Title: "결정"},
		{Width: 2100000, Title: "결정자 · 일자"},
		{Width: 1100000, Title: "상태"},
		{Width: 3032000, Title: "후속 조치"},
	}
	// Says what it is not showing, the same rule the screens follow.
	note := "이 기간에 기록된 결정입니다. 무엇을 했는지가 아니라 왜 그렇게 하기로 했는지입니다."
	if view.DecisionTotal > len(rows) {
		note = fmt.Sprintf("이 기간에 기록된 결정 %d건 중 %d건입니다. 후속 조치가 남은 것부터 싣습니다.",
			view.DecisionTotal, len(rows))
	}
	if view.OpenDecisions > 0 {
		note += fmt.Sprintf(" 후속 조치가 남은 결정이 %d건입니다.", view.OpenDecisions)
	}
	shapes := rollupHeader(2, "기간 내 결정", "")
	shapes += textBox(4, "Note", rollupMargin, 940000, rollupContentWidth, 260000, shapeStyle{},
		[]textRun{{Text: note, Size: 950, Color: "64748B"}})
	shapes += tableShape(6, "DecisionTable", rollupMargin, rollupContentTop+90000, rollupContentWidth, 400000, columns, rows)
	shapes += rollupFooter(8, view.Label+" · "+view.ScopeLabel, "")
	return &builtSlide{Shapes: shapes}
}

// decisionDeckRank puts what is still owed above what is settled.
func decisionDeckRank(status string) int {
	switch status {
	case decisionOpen:
		return 0
	case decisionDone:
		return 1
	default:
		return 2
	}
}

func decisionDeckStatus(status string) (string, string) {
	switch status {
	case decisionOpen:
		return "후속 조치", "1D4ED8"
	case decisionDone:
		return "완료", "166534"
	default:
		return "대체됨", "64748B"
	}
}

// askSlide lists what the authors said the reporting line has to decide or
// supply.
//
// ManagementAsk is deliberately separate from Issue — an issue states what is
// wrong, an ask states what somebody above has to do about it — and it reached
// the CSV, the meeting agenda and the handover note while appearing in neither
// deck. The decks are what management reads. This one's own assembly comment
// says a deck should end on the ask; until now the ask it ended on was a list
// this product inferred, not the sentence somebody wrote.
func askSlide(view rollupView) *builtSlide {
	rows := [][]tableCell{}
	remaining := 0
	for _, item := range view.Items {
		ask := firstLine(item.ManagementAsk)
		if ask == "" {
			continue
		}
		if len(rows) == askSlideRows {
			remaining++
			continue
		}
		owner := ""
		if len(item.Owners) > 0 {
			owner = item.Owners[0]
			if len(item.Owners) > 1 {
				owner += fmt.Sprintf(" 외 %d명", len(item.Owners)-1)
			}
		}
		rows = append(rows, []tableCell{
			{Text: trimRunes(item.Title, 26)},
			{Text: trimRunes(owner, 14), Align: "ctr", Color: "475569"},
			{Text: trimRunes(ask, 52), Color: "7C2D12"},
		})
	}
	if len(rows) == 0 {
		return nil
	}
	columns := []tableColumn{
		{Width: 3000000, Title: "업무"},
		{Width: 1600000, Title: "담당"},
		{Width: 6632000, Title: "요청·결정 필요 사항"},
	}
	note := "작성자가 상위 조직의 결정이나 지원이 필요하다고 적은 내용입니다."
	if remaining > 0 {
		// Saying how many did not fit beats letting them fall off the page.
		note += fmt.Sprintf(" 이 장에 담지 못한 요청이 %d건 더 있습니다.", remaining)
	}
	shapes := rollupHeader(2, "상위 조직 요청 · 결정 필요", "")
	shapes += textBox(4, "AskNote", rollupMargin, 940000, rollupContentWidth, 260000, shapeStyle{},
		[]textRun{{Text: note, Size: 950, Color: "64748B"}})
	shapes += tableShape(6, "AskTable", rollupMargin, rollupContentTop+90000, rollupContentWidth, 400000, columns, rows)
	shapes += rollupFooter(8, view.Label+" · "+view.ScopeLabel, "")
	return &builtSlide{Shapes: shapes}
}

const askSlideRows = 11
