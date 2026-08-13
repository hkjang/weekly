package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var pptxPlaceholderPattern = regexp.MustCompile(`\{\{(?:WEEK_SCHEDULE|WEEK_START|WEEK_END|THIS_WEEK|NEXT_WEEK|ISSUES|AUTHOR|TEAM)\}\}`)
var pptxCellPattern = regexp.MustCompile(`(?s)<a:tc>.*?</a:tc>`)
var pptxTextPattern = regexp.MustCompile(`(?s)<a:t>.*?</a:t>`)
var pptxTextBodyPattern = regexp.MustCompile(`(?s)<a:txBody>.*?</a:txBody>`)

const customPPTXPath = stateDirectory + "/templates/weekly-report.pptx"

type pptxTemplateView struct {
	Source       string     `json:"source"`
	OriginalName string     `json:"originalName"`
	SizeBytes    int64      `json:"sizeBytes"`
	SHA256       string     `json:"sha256"`
	Placeholders []string   `json:"placeholders"`
	UploadedAt   *time.Time `json:"uploadedAt,omitempty"`
}

func (a *App) pptxTemplateInfo(w http.ResponseWriter, r *http.Request) {
	info, err := a.loadPPTXTemplateInfo(r.Context())
	if err != nil {
		writeError(w, 500, "QUERY_FAILED", "PPTX 템플릿 정보를 조회할 수 없습니다.")
		return
	}
	writeData(w, 200, info)
}

func (a *App) loadPPTXTemplateInfo(ctx context.Context) (pptxTemplateView, error) {
	var result pptxTemplateView
	result.Source = "custom"
	err := a.db.QueryRow(ctx, `SELECT original_name,size_bytes,sha256,placeholders,uploaded_at FROM pptx_templates WHERE id=1`).
		Scan(&result.OriginalName, &result.SizeBytes, &result.SHA256, &result.Placeholders, &result.UploadedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if len(a.defaultPPTX) > 0 {
			sum := sha256.Sum256(a.defaultPPTX)
			return pptxTemplateView{Source: "reference", OriginalName: a.defaultPPTXName, SizeBytes: int64(len(a.defaultPPTX)), SHA256: fmt.Sprintf("%x", sum), Placeholders: []string{"AUTO:TEAM", "AUTO:THIS_WEEK_DATES", "AUTO:NEXT_WEEK_DATES", "AUTO:THIS_WEEK_ITEMS", "AUTO:NEXT_WEEK_ITEMS"}}, nil
		}
		body, buildErr := defaultPPTX()
		if buildErr != nil {
			return result, buildErr
		}
		sum := sha256.Sum256(body)
		return pptxTemplateView{Source: "built-in", OriginalName: "Weekly-기본-주간보고.pptx", SizeBytes: int64(len(body)), SHA256: fmt.Sprintf("%x", sum), Placeholders: []string{"{{AUTHOR}}", "{{ISSUES}}", "{{NEXT_WEEK}}", "{{TEAM}}", "{{THIS_WEEK}}", "{{WEEK_SCHEDULE}}"}}, nil
	}
	return result, err
}

func (a *App) uploadPPTXTemplate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 26<<20)
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		writeError(w, 400, "INVALID_UPLOAD", "PPTX 파일은 최대 25MB까지 업로드할 수 있습니다.")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "FILE_REQUIRED", "PPTX 템플릿 파일을 선택하세요.")
		return
	}
	defer file.Close()
	if !strings.EqualFold(filepath.Ext(header.Filename), ".pptx") {
		writeError(w, 400, "PPTX_REQUIRED", ".pptx 형식만 등록할 수 있습니다.")
		return
	}
	body, err := io.ReadAll(file)
	if err != nil || len(body) == 0 {
		writeError(w, 400, "INVALID_UPLOAD", "PPTX 파일을 읽을 수 없습니다.")
		return
	}
	placeholders, err := analyzePPTX(body)
	if err != nil {
		writeError(w, 400, "INVALID_PPTX", err.Error())
		return
	}
	if !contains(placeholders, "{{THIS_WEEK}}") || !contains(placeholders, "{{NEXT_WEEK}}") {
		writeError(w, 422, "PPTX_PLACEHOLDERS_REQUIRED", "템플릿에는 독립된 텍스트 상자 {{THIS_WEEK}}와 {{NEXT_WEEK}}가 필요합니다.")
		return
	}
	if err := os.MkdirAll(filepath.Dir(customPPTXPath), 0o700); err != nil {
		writeError(w, 500, "STORAGE_ERROR", "템플릿 저장소를 만들 수 없습니다.")
		return
	}
	temporary, err := os.CreateTemp(filepath.Dir(customPPTXPath), "upload-*.pptx")
	if err != nil {
		writeError(w, 500, "STORAGE_ERROR", "템플릿을 저장할 수 없습니다.")
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(body)
	}
	closeErr := temporary.Close()
	if err != nil || closeErr != nil {
		writeError(w, 500, "STORAGE_ERROR", "템플릿을 저장할 수 없습니다.")
		return
	}
	if err := os.Rename(temporaryPath, customPPTXPath); err != nil {
		writeError(w, 500, "STORAGE_ERROR", "템플릿을 적용할 수 없습니다.")
		return
	}
	sum := sha256.Sum256(body)
	p := currentPrincipal(r.Context())
	_, err = a.db.Exec(r.Context(), `INSERT INTO pptx_templates(id,original_name,file_name,size_bytes,sha256,placeholders,uploaded_by,uploaded_at)
		VALUES(1,$1,$2,$3,$4,$5,$6,now()) ON CONFLICT(id) DO UPDATE SET original_name=EXCLUDED.original_name,file_name=EXCLUDED.file_name,
		size_bytes=EXCLUDED.size_bytes,sha256=EXCLUDED.sha256,placeholders=EXCLUDED.placeholders,uploaded_by=EXCLUDED.uploaded_by,uploaded_at=now()`,
		trimRunes(filepath.Base(header.Filename), 255), filepath.Base(customPPTXPath), len(body), fmt.Sprintf("%x", sum), placeholders, p.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "템플릿 메타데이터를 저장할 수 없습니다.")
		return
	}
	a.audit(r, p, "pptx_template.upload", "pptx_template", "1", map[string]any{"name": header.Filename, "placeholders": placeholders})
	writeData(w, 200, map[string]any{"uploaded": true, "placeholders": placeholders})
}

func (a *App) resetPPTXTemplate(w http.ResponseWriter, r *http.Request) {
	_, err := a.db.Exec(r.Context(), `DELETE FROM pptx_templates WHERE id=1`)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "템플릿을 초기화할 수 없습니다.")
		return
	}
	if err := os.Remove(customPPTXPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, 500, "STORAGE_ERROR", "템플릿 파일을 제거할 수 없습니다.")
		return
	}
	a.audit(r, currentPrincipal(r.Context()), "pptx_template.reset", "pptx_template", "1", nil)
	writeData(w, 200, map[string]bool{"reset": true})
}

func (a *App) exportReportPPTX(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p := currentPrincipal(r.Context())
	if !a.canViewReport(r.Context(), p, id) {
		writeError(w, 403, "FORBIDDEN", "이 보고서를 내보낼 권한이 없습니다.")
		return
	}
	report, err := a.loadReport(r.Context(), id)
	if err != nil {
		writeError(w, 404, "REPORT_NOT_FOUND", "보고서를 찾을 수 없습니다.")
		return
	}
	template, err := os.ReadFile(customPPTXPath)
	customTemplate := err == nil
	if errors.Is(err, os.ErrNotExist) && len(a.defaultPPTX) > 0 {
		template = a.defaultPPTX
		err = nil
	} else if errors.Is(err, os.ErrNotExist) {
		template, err = defaultPPTX()
	}
	if err != nil {
		writeError(w, 500, "PPTX_TEMPLATE_ERROR", "PPTX 템플릿을 읽을 수 없습니다.")
		return
	}
	weekStart, _ := time.Parse("2006-01-02", report.WeekStart)
	weekEnd := weekStart.AddDate(0, 0, 6)
	team := a.userOrganizationName(r.Context(), report.UserID)
	values := map[string]string{
		"{{WEEK_START}}":    weekStart.Format("2006.01.02"),
		"{{WEEK_END}}":      weekEnd.Format("2006.01.02"),
		"{{WEEK_SCHEDULE}}": fmt.Sprintf("%s ~ %s", weekStart.Format("2006.01.02"), weekEnd.Format("01.08")),
		"{{AUTHOR}}":        report.DisplayName,
		"{{TEAM}}":          team,
		"{{THIS_WEEK}}":     reportItemLines(report.Items, "current"),
		"{{NEXT_WEEK}}":     reportItemLines(report.Items, "next"),
		"{{ISSUES}}":        reportItemLines(report.Items, "issue"),
	}
	var result []byte
	if !customTemplate && len(a.defaultPPTX) > 0 {
		result, err = renderReferencePPTX(template, report, team, weekStart)
	} else {
		result, err = renderPPTX(template, values)
	}
	if err != nil {
		a.logger.Error("render PPTX", "error", err)
		writeError(w, 500, "PPTX_RENDER_ERROR", "PPTX를 생성할 수 없습니다.")
		return
	}
	filename := fmt.Sprintf("%s_%s_주간업무보고.pptx", strings.ReplaceAll(report.WeekStart, "-", ""), report.DisplayName)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	w.Header().Set("Content-Disposition", "attachment; filename=weekly-report.pptx; filename*=UTF-8''"+url.PathEscape(filename))
	w.Header().Set("Content-Length", fmt.Sprint(len(result)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(result)
	a.audit(r, p, "report.export_pptx", "report", fmt.Sprint(id), map[string]any{"filename": filename})
}

func renderReferencePPTX(template []byte, report *reportView, team string, weekStart time.Time) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(template), int64(len(template)))
	if err != nil {
		return nil, err
	}
	slideCount := 0
	for _, file := range reader.File {
		if isSlideXML(file.Name) {
			slideCount++
		}
	}
	groups := distributeReferenceItems(report.Items, slideCount)
	currentHeader := fmt.Sprintf("추진실적 (%s ~ %s)", formatReferenceDate(weekStart), formatReferenceDate(weekStart.AddDate(0, 0, 6)))
	nextStart := weekStart.AddDate(0, 0, 7)
	nextHeader := fmt.Sprintf("추진계획 (%s ~ %s)", formatReferenceDate(nextStart), formatReferenceDate(nextStart.AddDate(0, 0, 6)))
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	slideIndex := 0
	for _, file := range reader.File {
		source, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(source)
		source.Close()
		if err != nil {
			return nil, err
		}
		if isSlideXML(file.Name) {
			items := []reportItem{}
			if slideIndex < len(groups) {
				items = groups[slideIndex]
			}
			data = []byte(renderReferenceSlide(string(data), team, currentHeader, nextHeader, items))
			slideIndex++
		}
		header := file.FileHeader
		header.Method = zip.Deflate
		destination, err := writer.CreateHeader(&header)
		if err != nil {
			return nil, err
		}
		if _, err := destination.Write(data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func isSlideXML(name string) bool {
	return strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") && !strings.Contains(name, "_rels")
}

func renderReferenceSlide(document, team, currentHeader, nextHeader string, items []reportItem) string {
	// The reference deck has four table cells on each slide: two headers followed by two content columns.
	cellIndex := 0
	document = pptxCellPattern.ReplaceAllStringFunc(document, func(cell string) string {
		index := cellIndex
		cellIndex++
		switch index {
		case 0:
			return replaceReferenceCellHeader(cell, currentHeader)
		case 1:
			return replaceReferenceCellHeader(cell, nextHeader)
		case 2:
			return pptxTextBodyPattern.ReplaceAllString(cell, referenceTextBody(items, "current"))
		case 3:
			return pptxTextBodyPattern.ReplaceAllString(cell, referenceTextBody(items, "next"))
		default:
			return cell
		}
	})
	// The supplied reference uses this team label on every slide.
	if strings.TrimSpace(team) != "" {
		document = strings.ReplaceAll(document, "<a:t>AI엔지니어링 파트</a:t>", "<a:t>"+escapeXML(team)+"</a:t>")
	}
	return document
}

func replaceReferenceCellHeader(cell, value string) string {
	replaced := false
	return pptxTextPattern.ReplaceAllStringFunc(cell, func(_ string) string {
		if replaced {
			return "<a:t></a:t>"
		}
		replaced = true
		return "<a:t>" + escapeXML(value) + "</a:t>"
	})
}

func referenceTextBody(items []reportItem, field string) string {
	var body strings.Builder
	body.WriteString(`<a:txBody><a:bodyPr/><a:lstStyle/>`)
	if len(items) == 0 {
		body.WriteString(referenceParagraph("-", "detail"))
	} else {
		lastCategory := ""
		for _, item := range items {
			category := strings.TrimSpace(item.Category)
			if category != "" && category != lastCategory {
				body.WriteString(referenceParagraph(category, "category"))
				lastCategory = category
			}
			body.WriteString(referenceParagraph(strings.TrimSpace(item.Title), "title"))
			var content string
			if field == "current" {
				content = item.CurrentResult
			} else {
				content = item.NextPlan
			}
			for _, line := range reportContentLines(content) {
				if line != "" {
					body.WriteString(referenceParagraph(line, "detail"))
				}
			}
		}
	}
	body.WriteString(`</a:txBody>`)
	return body.String()
}

func referenceParagraph(text, kind string) string {
	pPr := `<a:pPr indent="-142875" lvl="1" marL="449263" marR="0" rtl="0" algn="l"><a:lnSpc><a:spcPct val="120000"/></a:lnSpc><a:spcBef><a:spcPts val="500"/></a:spcBef><a:spcAft><a:spcPts val="0"/></a:spcAft><a:buClr><a:schemeClr val="dk1"/></a:buClr><a:buSzPts val="1300"/><a:buFont typeface="Arial"/><a:buChar char="*"/></a:pPr>`
	bold := "0"
	if kind == "category" {
		pPr = `<a:pPr indent="-82550" lvl="0" marL="88900" marR="0" rtl="0" algn="l"><a:lnSpc><a:spcPct val="150000"/></a:lnSpc><a:spcBef><a:spcPts val="0"/></a:spcBef><a:spcAft><a:spcPts val="0"/></a:spcAft><a:buClr><a:schemeClr val="dk1"/></a:buClr><a:buSzPts val="1300"/><a:buFont typeface="Arial"/><a:buChar char="•"/></a:pPr>`
		bold = "1"
	} else if kind == "title" {
		pPr = `<a:pPr indent="-142875" lvl="0" marL="338138" marR="0" rtl="0" algn="l"><a:lnSpc><a:spcPct val="120000"/></a:lnSpc><a:spcBef><a:spcPts val="600"/></a:spcBef><a:spcAft><a:spcPts val="0"/></a:spcAft><a:buClr><a:schemeClr val="dk1"/></a:buClr><a:buSzPts val="1300"/><a:buFont typeface="Arial"/><a:buChar char="-"/></a:pPr>`
	}
	return `<a:p>` + pPr + `<a:r><a:rPr b="` + bold + `" i="0" lang="ko-KR" sz="1300" u="none" cap="none" strike="noStrike"><a:solidFill><a:schemeClr val="dk1"/></a:solidFill><a:latin typeface="Arial"/><a:ea typeface="Arial"/><a:cs typeface="Arial"/><a:sym typeface="Arial"/></a:rPr><a:t>` + escapeXML(text) + `</a:t></a:r><a:endParaRPr b="` + bold + `" i="0" sz="1300" u="none" cap="none" strike="noStrike"><a:solidFill><a:schemeClr val="dk1"/></a:solidFill><a:latin typeface="Arial"/><a:ea typeface="Arial"/><a:cs typeface="Arial"/><a:sym typeface="Arial"/></a:endParaRPr></a:p>`
}

func distributeReferenceItems(items []reportItem, slideCount int) [][]reportItem {
	if slideCount < 1 {
		return nil
	}
	result := make([][]reportItem, slideCount)
	if len(items) == 0 {
		return result
	}

	// Keep report order while minimizing the tallest slide. A slide's two table
	// columns share the same height, so each item's cost is based on the longer
	// of the current-result and next-plan sides. The secondary squared-load score
	// makes otherwise equivalent partitions distribute short items evenly.
	partitionCount := slideCount
	if len(items) < partitionCount {
		partitionCount = len(items)
	}
	costs := referenceSegmentCosts(items)
	const unreachable = int(^uint(0) >> 1)
	maximumLoads := make([][]int, partitionCount+1)
	squaredLoads := make([][]int64, partitionCount+1)
	previousBreak := make([][]int, partitionCount+1)
	for partitions := 0; partitions <= partitionCount; partitions++ {
		maximumLoads[partitions] = make([]int, len(items)+1)
		squaredLoads[partitions] = make([]int64, len(items)+1)
		previousBreak[partitions] = make([]int, len(items)+1)
		for end := range maximumLoads[partitions] {
			maximumLoads[partitions][end] = unreachable
			previousBreak[partitions][end] = -1
		}
	}
	maximumLoads[0][0] = 0
	for partitions := 1; partitions <= partitionCount; partitions++ {
		for end := partitions; end <= len(items); end++ {
			for start := partitions - 1; start < end; start++ {
				if maximumLoads[partitions-1][start] == unreachable {
					continue
				}
				load := costs[start][end]
				candidateMaximum := maximumReferenceWeight(maximumLoads[partitions-1][start], load)
				candidateSquared := squaredLoads[partitions-1][start] + int64(load)*int64(load)
				if candidateMaximum < maximumLoads[partitions][end] ||
					(candidateMaximum == maximumLoads[partitions][end] && candidateSquared < squaredLoads[partitions][end]) {
					maximumLoads[partitions][end] = candidateMaximum
					squaredLoads[partitions][end] = candidateSquared
					previousBreak[partitions][end] = start
				}
			}
		}
	}

	end := len(items)
	for partitions := partitionCount; partitions > 0; partitions-- {
		start := previousBreak[partitions][end]
		if start < 0 {
			// Every positive item/slide combination is reachable. Keep a defensive
			// fallback so a future cost-model change can never drop report items.
			result[0] = append(result[0], items...)
			return result
		}
		result[partitions-1] = append(result[partitions-1], items[start:end]...)
		end = start
	}
	return result
}

// referenceSegmentCosts estimates the vertical space used by every contiguous
// item range. Category headings are counted once per slide segment, matching
// referenceTextBody; when a category crosses a slide boundary the heading cost
// (and the rendered heading itself) is repeated on the next slide.
func referenceSegmentCosts(items []reportItem) [][]int {
	itemLoads := make([]int, len(items))
	categories := make([]string, len(items))
	for index, item := range items {
		categories[index] = strings.TrimSpace(item.Category)
		itemLoads[index] = 3*referenceWrappedLines(strings.TrimSpace(item.Title)) +
			maximumReferenceWeight(referenceDetailWeight(item.CurrentResult), referenceDetailWeight(item.NextPlan))
	}
	costs := make([][]int, len(items))
	for start := range items {
		costs[start] = make([]int, len(items)+1)
		lastCategory := ""
		load := 0
		for end := start; end < len(items); end++ {
			category := categories[end]
			if category != "" && category != lastCategory {
				load += 3 * referenceWrappedLines(category)
				lastCategory = category
			}
			load += itemLoads[end]
			costs[start][end+1] = load
		}
	}
	return costs
}

func referenceDetailWeight(value string) int {
	weight := 0
	for _, line := range reportContentLines(value) {
		weight += 2 * referenceWrappedLines(line)
	}
	return weight
}

func referenceWrappedLines(value string) int {
	const estimatedRunesPerLine = 40
	length := len([]rune(strings.TrimSpace(value)))
	if length == 0 {
		return 1
	}
	return (length + estimatedRunesPerLine - 1) / estimatedRunesPerLine
}

func maximumReferenceWeight(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func formatReferenceDate(value time.Time) string {
	return fmt.Sprintf("%d.%d.%d", value.Year(), int(value.Month()), value.Day())
}

func escapeXML(value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

func (a *App) userOrganizationName(ctx context.Context, userID int64) string {
	var name string
	_ = a.db.QueryRow(ctx, `SELECT coalesce(o.name,'') FROM users u LEFT JOIN organizations o ON o.id=u.organization_id WHERE u.id=$1`, userID).Scan(&name)
	return name
}

func reportItemLines(items []reportItem, field string) string {
	lines := []string{}
	for _, item := range items {
		var content string
		switch field {
		case "current":
			content = strings.TrimSpace(item.CurrentResult)
		case "next":
			content = strings.TrimSpace(item.NextPlan)
		case "issue":
			content = strings.TrimSpace(item.Issue)
		}
		if content == "" {
			continue
		}
		prefix := "• "
		if item.Category != "" {
			prefix += "[" + item.Category + "] "
		}
		line := prefix + item.Title
		if content != item.Title {
			for _, detail := range reportContentLines(content) {
				line += "\n  - " + detail
			}
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "-"
	}
	return strings.Join(lines, "\n")
}

func reportContentLines(value string) []string {
	value = formatAIListText(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	result := make([]string, 0)
	for _, line := range strings.Split(value, "\n") {
		line = stripListMarker(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func analyzePPTX(body []byte) ([]string, error) {
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, errors.New("유효한 PPTX 압축 구조가 아닙니다.")
	}
	required := map[string]bool{"[Content_Types].xml": false, "ppt/presentation.xml": false}
	set := map[string]bool{}
	for _, file := range reader.File {
		if _, ok := required[file.Name]; ok {
			required[file.Name] = true
		}
		if !strings.HasPrefix(file.Name, "ppt/slides/slide") || !strings.HasSuffix(file.Name, ".xml") {
			continue
		}
		stream, openErr := file.Open()
		if openErr != nil {
			return nil, errors.New("PPTX 슬라이드를 읽을 수 없습니다.")
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, 10<<20))
		stream.Close()
		if readErr != nil {
			return nil, errors.New("PPTX 슬라이드를 읽을 수 없습니다.")
		}
		for _, token := range pptxPlaceholderPattern.FindAllString(string(data), -1) {
			set[token] = true
		}
	}
	for name, exists := range required {
		if !exists {
			return nil, fmt.Errorf("PPTX 필수 구성요소가 없습니다: %s", name)
		}
	}
	result := make([]string, 0, len(set))
	for token := range set {
		result = append(result, token)
	}
	sort.Strings(result)
	return result, nil
}

func renderPPTX(template []byte, values map[string]string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(template), int64(len(template)))
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range reader.File {
		source, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(source)
		source.Close()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(file.Name, "ppt/slides/slide") && strings.HasSuffix(file.Name, ".xml") {
			text := string(data)
			for token, value := range values {
				text = replacePPTXToken(text, token, value)
			}
			data = []byte(text)
		}
		header := file.FileHeader
		header.Method = zip.Deflate
		destination, err := writer.CreateHeader(&header)
		if err != nil {
			return nil, err
		}
		if _, err := destination.Write(data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func replacePPTXToken(document, token, value string) string {
	needle := "<a:t>" + token + "</a:t>"
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	parts := make([]string, 0, len(lines))
	for index, line := range lines {
		var escaped bytes.Buffer
		_ = xml.EscapeText(&escaped, []byte(line))
		if index > 0 {
			parts = append(parts, "</a:r><a:br/><a:r>")
		}
		parts = append(parts, "<a:t>"+escaped.String()+"</a:t>")
	}
	return strings.ReplaceAll(document, needle, strings.Join(parts, ""))
}

func defaultPPTX() ([]byte, error) {
	files := map[string]string{
		"[Content_Types].xml":                          defaultContentTypes,
		"_rels/.rels":                                  defaultRootRels,
		"docProps/app.xml":                             defaultAppProps,
		"docProps/core.xml":                            defaultCoreProps,
		"ppt/presentation.xml":                         defaultPresentation,
		"ppt/_rels/presentation.xml.rels":              defaultPresentationRels,
		"ppt/presProps.xml":                            defaultPresProps,
		"ppt/viewProps.xml":                            defaultViewProps,
		"ppt/tableStyles.xml":                          defaultTableStyles,
		"ppt/slides/slide1.xml":                        defaultSlide,
		"ppt/slides/_rels/slide1.xml.rels":             defaultSlideRels,
		"ppt/slideLayouts/slideLayout1.xml":            defaultSlideLayout,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": defaultSlideLayoutRels,
		"ppt/slideMasters/slideMaster1.xml":            defaultSlideMaster,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": defaultSlideMasterRels,
		"ppt/theme/theme1.xml":                         defaultTheme,
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(entry, files[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// referenceStylePPTX recreates the supplied AI Engineering weekly report format without
// requiring the source binary in a Git checkout. It uses the same 9,906,000 x 6,858,000
// canvas, four slides, team label, and two-column actual/plan table geometry.
func referenceStylePPTX() ([]byte, error) {
	contentTypes := strings.Replace(defaultContentTypes, `</Types>`, `<Override PartName="/ppt/slides/slide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slides/slide3.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slides/slide4.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/></Types>`, 1)
	presentation := strings.Replace(defaultPresentation, `<p:sldIdLst><p:sldId id="256" r:id="rId2"/></p:sldIdLst>`, `<p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId6"/><p:sldId id="258" r:id="rId7"/><p:sldId id="259" r:id="rId8"/></p:sldIdLst>`, 1)
	presentation = strings.Replace(presentation, `<p:sldSz cx="12192000" cy="6858000" type="screen16x9"/>`, `<p:sldSz cx="9906000" cy="6858000"/>`, 1)
	presentationRels := strings.Replace(defaultPresentationRels, `</Relationships>`, `<Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/><Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide3.xml"/><Relationship Id="rId8" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide4.xml"/></Relationships>`, 1)
	appProps := strings.Replace(defaultAppProps, `<Slides>1</Slides>`, `<Slides>4</Slides>`, 1)
	files := map[string]string{
		"[Content_Types].xml":                          contentTypes,
		"_rels/.rels":                                  defaultRootRels,
		"docProps/app.xml":                             appProps,
		"docProps/core.xml":                            defaultCoreProps,
		"ppt/presentation.xml":                         presentation,
		"ppt/_rels/presentation.xml.rels":              presentationRels,
		"ppt/presProps.xml":                            defaultPresProps,
		"ppt/viewProps.xml":                            defaultViewProps,
		"ppt/tableStyles.xml":                          defaultTableStyles,
		"ppt/slideLayouts/slideLayout1.xml":            defaultSlideLayout,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": defaultSlideLayoutRels,
		"ppt/slideMasters/slideMaster1.xml":            defaultSlideMaster,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": defaultSlideMasterRels,
		"ppt/theme/theme1.xml":                         defaultTheme,
	}
	for index := 1; index <= 4; index++ {
		files[fmt.Sprintf("ppt/slides/slide%d.xml", index)] = generatedReferenceSlide(index)
		files[fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", index)] = defaultSlideRels
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(entry, files[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func generatedReferenceSlide(_ int) string {
	headerActual := "추진실적 (2026.1.26 ~ 2026.1.30)"
	headerPlan := "추진계획 (2026.2.2 ~ 2026.2.6)"
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>` +
		generatedReferenceLabel(2, "Team", 8171160, 304608, 1534394, 292388, "AI엔지니어링 파트", 1300, true, "r") +
		`<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="3" name="Weekly actual and plan table"/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="272480" y="764704"/><a:ext cx="9433050" cy="5616625"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table"><a:tbl><a:tblPr><a:noFill/><a:tableStyleId>{9B8FA427-CB96-4E00-B161-538A03E3D982}</a:tableStyleId></a:tblPr><a:tblGrid><a:gridCol w="4716525"/><a:gridCol w="4716525"/></a:tblGrid><a:tr h="267825">` +
		generatedReferenceHeaderCell(headerActual) + generatedReferenceHeaderCell(headerPlan) +
		`</a:tr><a:tr h="5348800">` + generatedReferenceContentCell() + generatedReferenceContentCell() + `</a:tr></a:tbl></a:graphicData></a:graphic></p:graphicFrame>` +
		generatedReferenceLabel(4, "Footer", 272480, 6470000, 3500000, 260000, "주간업무 추진실적 및 계획", 1100, true, "l") +
		`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`
}

func generatedReferenceLabel(id int, name string, x, y, cx, cy int, text string, size int, bold bool, align string) string {
	boldValue := "0"
	if bold {
		boldValue = "1"
	}
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/><a:ln><a:noFill/></a:ln></p:spPr><p:txBody><a:bodyPr anchor="t"><a:spAutoFit/></a:bodyPr><a:lstStyle/><a:p><a:pPr algn="%s"><a:buNone/></a:pPr><a:r><a:rPr lang="ko-KR" sz="%d" b="%s"><a:solidFill><a:schemeClr val="dk1"/></a:solidFill><a:latin typeface="Arial"/><a:ea typeface="Arial"/></a:rPr><a:t>%s</a:t></a:r><a:endParaRPr lang="ko-KR" sz="%d" b="%s"/></a:p></p:txBody></p:sp>`, id, name, x, y, cx, cy, align, size, boldValue, escapeXML(text), size, boldValue)
}

func generatedReferenceHeaderCell(text string) string {
	return `<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:pPr algn="ctr"><a:buNone/></a:pPr><a:r><a:rPr b="1" lang="ko-KR" sz="1300"><a:solidFill><a:schemeClr val="dk1"/></a:solidFill><a:latin typeface="Arial"/><a:ea typeface="Arial"/></a:rPr><a:t>` + escapeXML(text) + `</a:t></a:r><a:endParaRPr b="1" sz="1300"/></a:p></a:txBody>` + generatedReferenceCellProperties(true) + `</a:tc>`
}

func generatedReferenceContentCell() string {
	return `<a:tc>` + referenceTextBody(nil, "current") + generatedReferenceCellProperties(false) + `</a:tc>`
}

func generatedReferenceCellProperties(header bool) string {
	fill := `<a:noFill/>`
	anchor := "t"
	if header {
		fill = `<a:solidFill><a:srgbClr val="D8D8D8"><a:alpha val="50196"/></a:srgbClr></a:solidFill>`
		anchor = "ctr"
	}
	line := `<a:solidFill><a:srgbClr val="7F7F7F"/></a:solidFill><a:prstDash val="solid"/>`
	return `<a:tcPr marT="46800" marB="46800" marR="43200" marL="43200" anchor="` + anchor + `"><a:lnL w="12700">` + line + `</a:lnL><a:lnR w="12700">` + line + `</a:lnR><a:lnT w="12700">` + line + `</a:lnT><a:lnB w="12700">` + line + `</a:lnB>` + fill + `</a:tcPr>`
}

const defaultContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/><Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/><Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/><Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/><Override PartName="/ppt/presProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presProps+xml"/><Override PartName="/ppt/viewProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.viewProps+xml"/><Override PartName="/ppt/tableStyles.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.tableStyles+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`
const defaultRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`
const defaultAppProps = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>Weekly</Application><PresentationFormat>On-screen Show (16:9)</PresentationFormat><Slides>1</Slides><Notes>0</Notes><HiddenSlides>0</HiddenSlides><Company>Weekly</Company><AppVersion>1.0</AppVersion></Properties>`
const defaultCoreProps = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:title>Weekly 주간업무보고</dc:title><dc:creator>Weekly</dc:creator><cp:lastModifiedBy>Weekly</cp:lastModifiedBy><dcterms:created xsi:type="dcterms:W3CDTF">2026-01-01T00:00:00Z</dcterms:created><dcterms:modified xsi:type="dcterms:W3CDTF">2026-01-01T00:00:00Z</dcterms:modified></cp:coreProperties>`
const defaultPresentation = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst><p:sldIdLst><p:sldId id="256" r:id="rId2"/></p:sldIdLst><p:sldSz cx="12192000" cy="6858000" type="screen16x9"/><p:notesSz cx="6858000" cy="9144000"/><p:defaultTextStyle/></p:presentation>`
const defaultPresentationRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/presProps" Target="presProps.xml"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/viewProps" Target="viewProps.xml"/><Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/tableStyles" Target="tableStyles.xml"/></Relationships>`
const defaultPresProps = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:presentationPr xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`
const defaultViewProps = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:viewPr xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:normalViewPr/><p:slideViewPr/><p:notesTextViewPr/><p:gridSpacing cx="78028800" cy="78028800"/></p:viewPr>`
const defaultTableStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><a:tblStyleLst xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" def="{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}"/>`
const defaultSlideRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/></Relationships>`
const defaultSlideLayout = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="blank" preserve="1"><p:cSld name="Blank"><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`
const defaultSlideLayoutRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`
const defaultSlideMaster = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="F8FAFC"/></a:solidFill><a:effectLst/></p:bgPr></p:bg><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr></p:spTree></p:cSld><p:clrMap accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" bg1="lt1" bg2="lt2" folHlink="folHlink" hlink="hlink" tx1="dk1" tx2="dk2"/><p:sldLayoutIdLst><p:sldLayoutId id="1" r:id="rId1"/></p:sldLayoutIdLst><p:txStyles><p:titleStyle/><p:bodyStyle/><p:otherStyle/></p:txStyles></p:sldMaster>`
const defaultSlideMasterRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/></Relationships>`
const defaultTheme = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Weekly"><a:themeElements><a:clrScheme name="Weekly"><a:dk1><a:srgbClr val="0F172A"/></a:dk1><a:lt1><a:srgbClr val="FFFFFF"/></a:lt1><a:dk2><a:srgbClr val="172554"/></a:dk2><a:lt2><a:srgbClr val="F8FAFC"/></a:lt2><a:accent1><a:srgbClr val="2563EB"/></a:accent1><a:accent2><a:srgbClr val="0D9488"/></a:accent2><a:accent3><a:srgbClr val="F59E0B"/></a:accent3><a:accent4><a:srgbClr val="7C3AED"/></a:accent4><a:accent5><a:srgbClr val="EC4899"/></a:accent5><a:accent6><a:srgbClr val="64748B"/></a:accent6><a:hlink><a:srgbClr val="0563C1"/></a:hlink><a:folHlink><a:srgbClr val="954F72"/></a:folHlink></a:clrScheme><a:fontScheme name="Weekly"><a:majorFont><a:latin typeface="Aptos Display"/><a:ea typeface="Noto Sans KR"/><a:cs typeface="Arial"/></a:majorFont><a:minorFont><a:latin typeface="Aptos"/><a:ea typeface="Noto Sans KR"/><a:cs typeface="Arial"/></a:minorFont></a:fontScheme><a:fmtScheme name="Weekly"><a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst><a:lnStyleLst><a:ln w="9525"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst><a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst><a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst></a:fmtScheme></a:themeElements></a:theme>`

var defaultSlide = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="F8FAFC"/></a:solidFill><a:effectLst/></p:bgPr></p:bg><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>` +
	shapeXML(2, "Title", 500000, 280000, 7600000, 650000, "주간업무보고", "172554", "FFFFFF", 2600, true) +
	shapeXML(3, "Schedule", 8300000, 280000, 3380000, 650000, "{{WEEK_SCHEDULE}}", "DBEAFE", "1E3A8A", 1500, true) +
	shapeXML(4, "Author", 500000, 1030000, 11200000, 360000, "{{TEAM}}  ·  {{AUTHOR}}", "F8FAFC", "475569", 1100, false) +
	shapeXML(5, "ThisWeekHeader", 500000, 1550000, 5400000, 500000, "이번 주 — 한 일", "2563EB", "FFFFFF", 1800, true) +
	shapeXML(6, "NextWeekHeader", 6290000, 1550000, 5400000, 500000, "다음 주 — 할 일", "0D9488", "FFFFFF", 1800, true) +
	shapeXML(7, "ThisWeek", 500000, 2050000, 5400000, 3200000, "{{THIS_WEEK}}", "FFFFFF", "1E293B", 1250, false) +
	shapeXML(8, "NextWeek", 6290000, 2050000, 5400000, 3200000, "{{NEXT_WEEK}}", "FFFFFF", "1E293B", 1250, false) +
	shapeXML(9, "IssuesHeader", 500000, 5480000, 1800000, 420000, "이슈 / 지원 요청", "FEF3C7", "92400E", 1200, true) +
	shapeXML(10, "Issues", 2380000, 5480000, 9310000, 800000, "{{ISSUES}}", "FFFFFF", "475569", 1100, false) +
	`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`

func shapeXML(id int, name string, x, y, cx, cy int, text, fill, color string, fontSize int, bold bool) string {
	boldValue := "0"
	if bold {
		boldValue = "1"
	}
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="roundRect"><a:avLst/></a:prstGeom><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:ln><a:solidFill><a:srgbClr val="E2E8F0"/></a:solidFill></a:ln></p:spPr><p:txBody><a:bodyPr wrap="square" lIns="180000" rIns="180000" tIns="110000" bIns="90000" anchor="t"/><a:lstStyle/><a:p><a:r><a:rPr lang="ko-KR" sz="%d" b="%s" dirty="0"><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:latin typeface="Aptos"/><a:ea typeface="Noto Sans KR"/></a:rPr><a:t>%s</a:t></a:r><a:endParaRPr lang="ko-KR" sz="%d"/></a:p></p:txBody></p:sp>`, id, name, x, y, cx, cy, fill, fontSize, boldValue, color, text, fontSize)
}
