package app

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type extractedPPTX struct {
	Slides          []extractedSlide
	Normalized      string
	SlideCount      int
	Truncated       bool
	TruncatedSlides []int
}

type extractedSlide struct {
	Number       int
	Order        int
	OutsideTexts []string
	Cells        [][]string
	Shapes       []extractedShape
	Tables       []extractedTable
}

type extractedShape struct {
	Name       string
	Paragraphs []extractedParagraph
}

type extractedTable struct {
	Rows []extractedRow
}

type extractedRow struct {
	Cells []extractedCell
}

type extractedCell struct {
	Paragraphs []extractedParagraph
}

type extractedParagraph struct {
	Text      string
	Bullet    string
	Level     int
	HasBullet bool
	HasLevel  bool
}

type detectedWeek struct {
	Start      time.Time
	End        time.Time
	Confidence float64
	Source     string
}

var importSlidePattern = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)
var importSlideRelationshipTypePattern = regexp.MustCompile(`/slide$`)
var fullNumericDatePattern = regexp.MustCompile(`(?i)(20\d{2})\s*[./-]\s*(\d{1,2})\s*[./-]\s*(\d{1,2})`)
var koreanDatePattern = regexp.MustCompile(`(20\d{2})\s*년\s*(\d{1,2})\s*월\s*(\d{1,2})\s*일?`)
var compactDatePattern = regexp.MustCompile(`(?:^|[^0-9])(20\d{2})(\d{2})(\d{2})(?:[^0-9]|$)`)
var shortRangePattern = regexp.MustCompile(`(\d{1,2})\s*[./-]\s*(\d{1,2})\s*(?:~|-|–|—)\s*(\d{1,2})\s*[./-]\s*(\d{1,2})`)
var filenameYearPattern = regexp.MustCompile(`20\d{2}`)

func extractPPTX(body []byte, maximumInput int) (extractedPPTX, error) {
	if len(body) == 0 || len(body) > 100<<20 {
		return extractedPPTX{}, errors.New("PPTX 크기가 올바르지 않습니다")
	}
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return extractedPPTX{}, errors.New("유효한 PPTX ZIP 구조가 아닙니다")
	}
	if len(reader.File) > 3000 {
		return extractedPPTX{}, errors.New("PPTX 구성 파일 수가 너무 많습니다")
	}
	var total uint64
	presentationFound := false
	var presentationXML, presentationRelationshipsXML []byte
	slidesByNumber := map[int]extractedSlide{}
	for _, file := range reader.File {
		total += file.UncompressedSize64
		if total > 120<<20 {
			return extractedPPTX{}, errors.New("PPTX 압축 해제 크기가 제한을 초과합니다")
		}
		if file.Name == "ppt/presentation.xml" {
			presentationFound = true
			presentationXML, err = readPPTXEntry(file, 10<<20)
			if err != nil {
				return extractedPPTX{}, errors.New("PPTX 프레젠테이션 순서를 읽을 수 없습니다")
			}
			continue
		}
		if file.Name == "ppt/_rels/presentation.xml.rels" {
			presentationRelationshipsXML, err = readPPTXEntry(file, 10<<20)
			if err != nil {
				return extractedPPTX{}, errors.New("PPTX 슬라이드 관계를 읽을 수 없습니다")
			}
			continue
		}
		match := importSlidePattern.FindStringSubmatch(file.Name)
		if len(match) != 2 {
			continue
		}
		if file.UncompressedSize64 > 10<<20 {
			return extractedPPTX{}, errors.New("PPTX 슬라이드 XML이 너무 큽니다")
		}
		number, _ := strconv.Atoi(match[1])
		stream, err := file.Open()
		if err != nil {
			return extractedPPTX{}, errors.New("PPTX 슬라이드를 열 수 없습니다")
		}
		slide, parseErr := parseImportSlideXML(stream, number)
		stream.Close()
		if parseErr != nil {
			return extractedPPTX{}, fmt.Errorf("슬라이드 %d XML 파싱 실패: %w", number, parseErr)
		}
		slidesByNumber[number] = slide
	}
	if !presentationFound || len(slidesByNumber) == 0 {
		return extractedPPTX{}, errors.New("PPTX 필수 프레젠테이션 또는 슬라이드가 없습니다")
	}
	slides := orderExtractedSlides(slidesByNumber, presentationXML, presentationRelationshipsXML)
	result, truncated, truncatedSlides := normalizeExtractedSlides(slides, maximumInput)
	if result == "" {
		return extractedPPTX{}, errors.New("PPTX에서 텍스트를 찾을 수 없습니다")
	}
	return extractedPPTX{
		Slides: slides, Normalized: result, SlideCount: len(slides),
		Truncated: truncated, TruncatedSlides: truncatedSlides,
	}, nil
}

func parseImportSlideXML(source io.Reader, number int) (extractedSlide, error) {
	result := extractedSlide{
		Number: number, OutsideTexts: []string{}, Cells: [][]string{},
		Shapes: []extractedShape{}, Tables: []extractedTable{},
	}
	decoder := xml.NewDecoder(io.LimitReader(source, 10<<20))
	shapeIndex, tableIndex, rowIndex, cellIndex := -1, -1, -1, -1
	inParagraph := false
	paragraph := extractedParagraph{}
	var paragraphText strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch {
			case isPresentationElement(value.Name, "sp"):
				result.Shapes = append(result.Shapes, extractedShape{Paragraphs: []extractedParagraph{}})
				shapeIndex = len(result.Shapes) - 1
			case shapeIndex >= 0 && isPresentationElement(value.Name, "cNvPr"):
				if result.Shapes[shapeIndex].Name == "" {
					result.Shapes[shapeIndex].Name = trimRunes(strings.TrimSpace(xmlAttribute(value.Attr, "name")), 200)
				}
			case isDrawingElement(value.Name, "tbl"):
				result.Tables = append(result.Tables, extractedTable{Rows: []extractedRow{}})
				tableIndex = len(result.Tables) - 1
				rowIndex, cellIndex = -1, -1
			case tableIndex >= 0 && isDrawingElement(value.Name, "tr"):
				result.Tables[tableIndex].Rows = append(result.Tables[tableIndex].Rows, extractedRow{Cells: []extractedCell{}})
				rowIndex = len(result.Tables[tableIndex].Rows) - 1
				cellIndex = -1
			case tableIndex >= 0 && rowIndex >= 0 && isDrawingElement(value.Name, "tc"):
				row := &result.Tables[tableIndex].Rows[rowIndex]
				row.Cells = append(row.Cells, extractedCell{Paragraphs: []extractedParagraph{}})
				cellIndex = len(row.Cells) - 1
			case (cellIndex >= 0 || shapeIndex >= 0) && isDrawingElement(value.Name, "p"):
				inParagraph = true
				paragraph = extractedParagraph{}
				paragraphText.Reset()
			case inParagraph && isDrawingElement(value.Name, "pPr"):
				if level := xmlAttribute(value.Attr, "lvl"); level != "" {
					if parsed, parseErr := strconv.Atoi(level); parseErr == nil && parsed >= 0 {
						paragraph.Level = parsed
						paragraph.HasLevel = true
					}
				}
			case inParagraph && isDrawingElement(value.Name, "buChar"):
				paragraph.Bullet = normalizeImportedText(xmlAttribute(value.Attr, "char"))
				paragraph.HasBullet = paragraph.Bullet != ""
			case inParagraph && isDrawingElement(value.Name, "buAutoNum"):
				paragraph.Bullet = "AUTO_NUMBER"
				paragraph.HasBullet = true
			case inParagraph && isDrawingElement(value.Name, "buBlip"):
				paragraph.Bullet = "IMAGE"
				paragraph.HasBullet = true
			case inParagraph && isDrawingElement(value.Name, "buNone"):
				paragraph.Bullet = ""
				paragraph.HasBullet = false
			case inParagraph && isDrawingElement(value.Name, "br"):
				paragraphText.WriteString("\n")
			case inParagraph && isDrawingElement(value.Name, "t"):
				var text string
				if err := decoder.DecodeElement(&text, &value); err != nil {
					return result, err
				}
				paragraphText.WriteString(strings.ReplaceAll(text, "\x00", ""))
			}
		case xml.EndElement:
			switch {
			case inParagraph && isDrawingElement(value.Name, "p"):
				paragraph.Text = normalizeImportedText(paragraphText.String())
				if paragraph.Text != "" {
					if tableIndex >= 0 && rowIndex >= 0 && cellIndex >= 0 {
						cell := &result.Tables[tableIndex].Rows[rowIndex].Cells[cellIndex]
						cell.Paragraphs = append(cell.Paragraphs, paragraph)
					} else if shapeIndex >= 0 {
						result.Shapes[shapeIndex].Paragraphs = append(result.Shapes[shapeIndex].Paragraphs, paragraph)
					}
				}
				inParagraph = false
				paragraphText.Reset()
			case isDrawingElement(value.Name, "tc"):
				cellIndex = -1
			case isDrawingElement(value.Name, "tr"):
				rowIndex, cellIndex = -1, -1
			case isDrawingElement(value.Name, "tbl"):
				tableIndex, rowIndex, cellIndex = -1, -1, -1
			case isPresentationElement(value.Name, "sp"):
				shapeIndex = -1
			}
		}
	}
	populateLegacySlideFields(&result)
	return result, nil
}

const (
	drawingMLNamespace    = "http://schemas.openxmlformats.org/drawingml/2006/main"
	presentationNamespace = "http://schemas.openxmlformats.org/presentationml/2006/main"
	relationshipNamespace = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

func isDrawingElement(name xml.Name, local string) bool {
	return name.Local == local && (name.Space == drawingMLNamespace || name.Space == "")
}

func isPresentationElement(name xml.Name, local string) bool {
	return name.Local == local && (name.Space == presentationNamespace || name.Space == "")
}

func xmlAttribute(attributes []xml.Attr, local string) string {
	for _, attribute := range attributes {
		if attribute.Name.Local == local {
			return attribute.Value
		}
	}
	return ""
}

func populateLegacySlideFields(slide *extractedSlide) {
	for _, shape := range slide.Shapes {
		for _, paragraph := range shape.Paragraphs {
			slide.OutsideTexts = append(slide.OutsideTexts, paragraph.Text)
		}
	}
	for _, table := range slide.Tables {
		for _, row := range table.Rows {
			for _, cell := range row.Cells {
				texts := make([]string, 0, len(cell.Paragraphs))
				for _, paragraph := range cell.Paragraphs {
					texts = append(texts, paragraph.Text)
				}
				// Empty cells are intentionally retained. Their position is required to
				// distinguish, for example, an empty actual column from a populated plan.
				slide.Cells = append(slide.Cells, texts)
			}
		}
	}
}

func readPPTXEntry(file *zip.File, limit int64) ([]byte, error) {
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return io.ReadAll(io.LimitReader(stream, limit))
}

func orderExtractedSlides(slidesByNumber map[int]extractedSlide, presentationXML, relationshipsXML []byte) []extractedSlide {
	orderedNumbers := presentationSlideNumbers(presentationXML, relationshipsXML)
	seen := make(map[int]bool, len(slidesByNumber))
	result := make([]extractedSlide, 0, len(slidesByNumber))
	for _, number := range orderedNumbers {
		slide, exists := slidesByNumber[number]
		if !exists || seen[number] {
			continue
		}
		seen[number] = true
		result = append(result, slide)
	}
	remaining := make([]int, 0, len(slidesByNumber)-len(result))
	for number := range slidesByNumber {
		if !seen[number] {
			remaining = append(remaining, number)
		}
	}
	sort.Ints(remaining)
	for _, number := range remaining {
		result = append(result, slidesByNumber[number])
	}
	for index := range result {
		result[index].Order = index + 1
	}
	return result
}

func presentationSlideNumbers(presentationXML, relationshipsXML []byte) []int {
	relationshipIDs := parsePresentationSlideIDs(presentationXML)
	targets := parsePresentationSlideRelationships(relationshipsXML)
	result := make([]int, 0, len(relationshipIDs))
	for _, relationshipID := range relationshipIDs {
		target, exists := targets[relationshipID]
		if !exists {
			continue
		}
		target = strings.TrimPrefix(target, "/")
		if !strings.HasPrefix(target, "ppt/") {
			target = path.Join("ppt", target)
		} else {
			target = path.Clean(target)
		}
		match := importSlidePattern.FindStringSubmatch(target)
		if len(match) == 2 {
			number, _ := strconv.Atoi(match[1])
			result = append(result, number)
		}
	}
	return result
}

func parsePresentationSlideIDs(document []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(document))
	result := []string{}
	for {
		token, err := decoder.Token()
		if err != nil {
			return result
		}
		start, ok := token.(xml.StartElement)
		if !ok || !isPresentationElement(start.Name, "sldId") {
			continue
		}
		for _, attribute := range start.Attr {
			if attribute.Name.Local == "id" && (attribute.Name.Space == relationshipNamespace || strings.HasPrefix(attribute.Value, "rId")) {
				result = append(result, attribute.Value)
				break
			}
		}
	}
}

func parsePresentationSlideRelationships(document []byte) map[string]string {
	decoder := xml.NewDecoder(bytes.NewReader(document))
	result := map[string]string{}
	for {
		token, err := decoder.Token()
		if err != nil {
			return result
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		identifier := xmlAttribute(start.Attr, "Id")
		target := xmlAttribute(start.Attr, "Target")
		typeName := xmlAttribute(start.Attr, "Type")
		if identifier != "" && target != "" && importSlideRelationshipTypePattern.MatchString(typeName) {
			result[identifier] = target
		}
	}
}

func normalizeExtractedSlides(slides []extractedSlide, maximumInput int) (string, bool, []int) {
	segments := make([]string, 0, len(slides))
	lengths := make([]int, 0, len(slides))
	for _, slide := range slides {
		segment := formatExtractedSlide(slide)
		segments = append(segments, segment)
		lengths = append(lengths, runeLength(segment))
	}
	full := strings.TrimSpace(strings.Join(segments, "\n\n"))
	if maximumInput <= 0 || runeLength(full) <= maximumInput {
		return full, false, nil
	}

	separatorCost := maximum(0, (len(segments)-1)*2)
	budgets := fairTextBudgets(lengths, maximum(0, maximumInput-separatorCost))
	truncatedSlides := make([]int, 0, len(slides))
	for index := range segments {
		if budgets[index] >= lengths[index] {
			continue
		}
		truncatedSlides = append(truncatedSlides, slides[index].Order)
		segments[index] = truncateSlideSegment(segments[index], budgets[index], slides[index].Order)
	}
	return strings.TrimSpace(strings.Join(segments, "\n\n")), true, truncatedSlides
}

func formatExtractedSlide(slide extractedSlide) string {
	var normalized strings.Builder
	fmt.Fprintf(&normalized, "=== SLIDE %d (SOURCE slide%d.xml) ===\n", slide.Order, slide.Number)
	for shapeIndex, shape := range slide.Shapes {
		if len(shape.Paragraphs) == 0 {
			continue
		}
		fmt.Fprintf(&normalized, "[SHAPE %d", shapeIndex+1)
		if shape.Name != "" {
			fmt.Fprintf(&normalized, " name=%q", shape.Name)
		}
		normalized.WriteString("]\n")
		writeExtractedParagraphs(&normalized, shape.Paragraphs)
	}
	for tableIndex, table := range slide.Tables {
		fmt.Fprintf(&normalized, "[TABLE %d]\n", tableIndex+1)
		for rowIndex, row := range table.Rows {
			fmt.Fprintf(&normalized, "[ROW %d]\n", rowIndex+1)
			for columnIndex, cell := range row.Cells {
				fmt.Fprintf(&normalized, "[COL %d]\n", columnIndex+1)
				if len(cell.Paragraphs) == 0 {
					normalized.WriteString("[EMPTY]\n")
					continue
				}
				writeExtractedParagraphs(&normalized, cell.Paragraphs)
			}
		}
	}
	return strings.TrimSpace(normalized.String())
}

func writeExtractedParagraphs(destination *strings.Builder, paragraphs []extractedParagraph) {
	for index, paragraph := range paragraphs {
		fmt.Fprintf(destination, "PARAGRAPH %d", index+1)
		if paragraph.HasBullet {
			fmt.Fprintf(destination, " bullet=%q", paragraph.Bullet)
		}
		if paragraph.HasLevel {
			fmt.Fprintf(destination, " level=%d", paragraph.Level)
		}
		fmt.Fprintf(destination, ": %s\n", paragraph.Text)
	}
}

func fairTextBudgets(lengths []int, total int) []int {
	budgets := make([]int, len(lengths))
	unresolved := make([]int, 0, len(lengths))
	for index := range lengths {
		unresolved = append(unresolved, index)
	}
	for len(unresolved) > 0 && total > 0 {
		share := total / len(unresolved)
		if share == 0 {
			for _, index := range unresolved {
				if total == 0 {
					break
				}
				budgets[index]++
				total--
			}
			break
		}
		next := make([]int, 0, len(unresolved))
		resolvedAny := false
		for _, index := range unresolved {
			needed := lengths[index] - budgets[index]
			if needed <= share {
				budgets[index] += needed
				total -= needed
				resolvedAny = true
			} else {
				next = append(next, index)
			}
		}
		if resolvedAny {
			unresolved = next
			continue
		}
		for _, index := range unresolved {
			budgets[index] += share
			total -= share
		}
		for _, index := range unresolved {
			if total == 0 {
				break
			}
			budgets[index]++
			total--
		}
		break
	}
	return budgets
}

func truncateSlideSegment(segment string, budget, slideOrder int) string {
	if budget <= 0 {
		return ""
	}
	lines := strings.Split(segment, "\n")
	header := lines[0]
	notice := fmt.Sprintf("[SLIDE %d CONTENT TRUNCATED]", slideOrder)
	if budget <= runeLength(header) {
		return trimRunes(header, budget)
	}
	if budget <= runeLength(header)+1+runeLength(notice) {
		return trimRunes(header+"\n"+notice, budget)
	}
	remaining := budget - runeLength(header) - 1 - runeLength(notice) - 1
	kept := []string{header}
	for _, line := range lines[1:] {
		cost := runeLength(line) + 1
		if cost <= remaining {
			kept = append(kept, line)
			remaining -= cost
			continue
		}
		if remaining > 1 {
			kept = append(kept, trimRunes(line, remaining-1))
		}
		break
	}
	kept = append(kept, notice)
	return strings.Join(kept, "\n")
}

func normalizeImportedText(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	value = strings.Join(strings.Fields(value), " ")
	return trimRunes(strings.TrimSpace(value), 5000)
}

func detectPPTXWeek(filename, normalized string, weekStartSetting string, location *time.Location) detectedWeek {
	if location == nil {
		location = time.UTC
	}
	if dates := extractFullDates(normalized, location); len(dates) > 0 {
		start := currentWeekStart(dates[0], weekStartSetting)
		end := start.AddDate(0, 0, 6)
		confidence := 0.78
		if len(dates) > 1 && !dates[1].Before(dates[0]) && dates[1].Sub(dates[0]) <= 6*24*time.Hour {
			start = dates[0]
			end = dates[1]
			confidence = 0.98
		}
		return detectedWeek{Start: start, End: end, Confidence: confidence, Source: "slide_text"}
	}
	if yearMatch := filenameYearPattern.FindString(filename); yearMatch != "" {
		year, _ := strconv.Atoi(yearMatch)
		if match := shortRangePattern.FindStringSubmatch(normalized); len(match) == 5 {
			start, startOK := makeDate(year, match[1], match[2], location)
			end, endOK := makeDate(year, match[3], match[4], location)
			if startOK && endOK && !end.Before(start) && end.Sub(start) <= 6*24*time.Hour {
				return detectedWeek{Start: start, End: end, Confidence: 0.9, Source: "slide_text_with_filename_year"}
			}
		}
	}
	if match := compactDatePattern.FindStringSubmatch(filepath.Base(filename)); len(match) == 4 {
		date, ok := makeDate(match[1], match[2], match[3], location)
		if ok {
			start := currentWeekStart(date, weekStartSetting)
			return detectedWeek{Start: start, End: start.AddDate(0, 0, 6), Confidence: 0.82, Source: "filename"}
		}
	}
	return detectedWeek{}
}

func extractFullDates(value string, location *time.Location) []time.Time {
	type candidate struct {
		position int
		date     time.Time
	}
	candidates := []candidate{}
	for _, pattern := range []*regexp.Regexp{fullNumericDatePattern, koreanDatePattern} {
		for _, indices := range pattern.FindAllStringSubmatchIndex(value, -1) {
			if len(indices) < 8 {
				continue
			}
			date, ok := makeDate(value[indices[2]:indices[3]], value[indices[4]:indices[5]], value[indices[6]:indices[7]], location)
			if ok {
				candidates = append(candidates, candidate{position: indices[0], date: date})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].position < candidates[j].position })
	result := make([]time.Time, 0, len(candidates))
	for _, item := range candidates {
		if len(result) == 0 || !result[len(result)-1].Equal(item.date) {
			result = append(result, item.date)
		}
	}
	return result
}

func makeDate(yearValue, monthValue, dayValue any, location *time.Location) (time.Time, bool) {
	year, yearOK := integerValue(yearValue)
	month, monthOK := integerValue(monthValue)
	day, dayOK := integerValue(dayValue)
	if !yearOK || !monthOK || !dayOK || year < 2000 || year > 2100 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	result := time.Date(year, time.Month(month), day, 0, 0, 0, 0, location)
	if result.Year() != year || int(result.Month()) != month || result.Day() != day {
		return time.Time{}, false
	}
	return result, true
}

func integerValue(value any) (int, bool) {
	switch item := value.(type) {
	case string:
		result, err := strconv.Atoi(item)
		return result, err == nil
	case int:
		return item, true
	default:
		return 0, false
	}
}
