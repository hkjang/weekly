package app

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type extractedPPTX struct {
	Slides     []extractedSlide
	Normalized string
}

type extractedSlide struct {
	Number       int
	OutsideTexts []string
	Cells        [][]string
}

type detectedWeek struct {
	Start      time.Time
	End        time.Time
	Confidence float64
	Source     string
}

var importSlidePattern = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)
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
	slides := []extractedSlide{}
	for _, file := range reader.File {
		total += file.UncompressedSize64
		if total > 120<<20 {
			return extractedPPTX{}, errors.New("PPTX 압축 해제 크기가 제한을 초과합니다")
		}
		if file.Name == "ppt/presentation.xml" {
			presentationFound = true
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
		slides = append(slides, slide)
	}
	if !presentationFound || len(slides) == 0 {
		return extractedPPTX{}, errors.New("PPTX 필수 프레젠테이션 또는 슬라이드가 없습니다")
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].Number < slides[j].Number })
	var normalized strings.Builder
	for _, slide := range slides {
		fmt.Fprintf(&normalized, "\n=== SLIDE %d ===\n", slide.Number)
		if len(slide.OutsideTexts) > 0 {
			normalized.WriteString("[NON_TABLE_TEXT]\n")
			for _, value := range slide.OutsideTexts {
				normalized.WriteString(value + "\n")
			}
		}
		if len(slide.Cells) > 0 {
			normalized.WriteString("[TABLE_CELLS_IN_ORDER]\n")
			for index, cell := range slide.Cells {
				fmt.Fprintf(&normalized, "CELL %d:\n", index+1)
				for _, value := range cell {
					normalized.WriteString(value + "\n")
				}
			}
		}
	}
	result := strings.TrimSpace(normalized.String())
	if result == "" {
		return extractedPPTX{}, errors.New("PPTX에서 텍스트를 찾을 수 없습니다")
	}
	if maximumInput > 0 && runeLength(result) > maximumInput {
		result = trimRunes(result, maximumInput)
		result += "\n[입력 길이 제한으로 이후 텍스트가 생략됨]"
	}
	return extractedPPTX{Slides: slides, Normalized: result}, nil
}

func parseImportSlideXML(source io.Reader, number int) (extractedSlide, error) {
	result := extractedSlide{Number: number, OutsideTexts: []string{}, Cells: [][]string{}}
	decoder := xml.NewDecoder(io.LimitReader(source, 10<<20))
	inCell := false
	cellIndex := -1
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
			if value.Name.Local == "tc" {
				inCell = true
				result.Cells = append(result.Cells, []string{})
				cellIndex = len(result.Cells) - 1
				continue
			}
			if value.Name.Local != "t" {
				continue
			}
			var text string
			if err := decoder.DecodeElement(&text, &value); err != nil {
				return result, err
			}
			text = normalizeImportedText(text)
			if text == "" {
				continue
			}
			if inCell && cellIndex >= 0 {
				result.Cells[cellIndex] = append(result.Cells[cellIndex], text)
			} else {
				result.OutsideTexts = append(result.OutsideTexts, text)
			}
		case xml.EndElement:
			if value.Name.Local == "tc" {
				inCell = false
				cellIndex = -1
			}
		}
	}
	filtered := result.Cells[:0]
	for _, cell := range result.Cells {
		if len(cell) > 0 {
			filtered = append(filtered, cell)
		}
	}
	result.Cells = filtered
	return result, nil
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
		end := dates[0].AddDate(0, 0, 4)
		confidence := 0.92
		if len(dates) > 1 && !dates[1].Before(dates[0]) && dates[1].Sub(dates[0]) <= 7*24*time.Hour {
			end = dates[1]
			confidence = 0.98
		}
		return detectedWeek{Start: dates[0], End: end, Confidence: confidence, Source: "slide_text"}
	}
	if yearMatch := filenameYearPattern.FindString(filename); yearMatch != "" {
		year, _ := strconv.Atoi(yearMatch)
		if match := shortRangePattern.FindStringSubmatch(normalized); len(match) == 5 {
			start, startOK := makeDate(year, match[1], match[2], location)
			end, endOK := makeDate(year, match[3], match[4], location)
			if startOK && endOK && !end.Before(start) && end.Sub(start) <= 7*24*time.Hour {
				return detectedWeek{Start: start, End: end, Confidence: 0.9, Source: "slide_text_with_filename_year"}
			}
		}
	}
	if match := compactDatePattern.FindStringSubmatch(filepath.Base(filename)); len(match) == 4 {
		date, ok := makeDate(match[1], match[2], match[3], location)
		if ok {
			start := currentWeekStart(date, weekStartSetting)
			return detectedWeek{Start: start, End: start.AddDate(0, 0, 4), Confidence: 0.82, Source: "filename"}
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
