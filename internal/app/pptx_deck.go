package app

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Slide assembly shared by the period rollup export, which needs as many
// slides as the content requires, and by image capture slides, which are
// grafted onto an administrator supplied template without disturbing it.

const (
	emuPerInch     = 914400
	defaultSlideCX = 12192000
	defaultSlideCY = 6858000
)

type slideMedia struct {
	// Name is the part name inside ppt/media, for example image3.png.
	Name        string
	ContentType string
	Extension   string
	Bytes       []byte
	// RelID is the relationship id the owning slide uses to reach this part.
	RelID string
}

// builtSlide carries the shapes of one slide plus any media it references.
type builtSlide struct {
	Shapes string
	Media  []slideMedia
}

var (
	slideNumberPattern = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)
	relationIDPattern  = regexp.MustCompile(`Id="rId(\d+)"`)
	slideIDPattern     = regexp.MustCompile(`<p:sldId\s+id="(\d+)"`)
	slideSizePattern   = regexp.MustCompile(`<p:sldSz\s+cx="(\d+)"\s+cy="(\d+)"`)
	slideListPattern   = regexp.MustCompile(`(?s)<p:sldIdLst>(.*?)</p:sldIdLst>`)
	emptySlideListTag  = regexp.MustCompile(`<p:sldIdLst\s*/>`)
	layoutRelPattern   = regexp.MustCompile(`<Relationship[^>]*slideLayout[^>]*/>`)
	slideCountPattern  = regexp.MustCompile(`<Slides>\d+</Slides>`)
)

// buildPPTX assembles a self-contained deck from generated slides. It owns
// every relationship id, so the rollup layout can emit any number of slides.
func buildPPTX(widthEMU, heightEMU int, title string, slides []builtSlide) ([]byte, error) {
	if len(slides) == 0 {
		return nil, errors.New("a deck needs at least one slide")
	}
	files := map[string]string{
		"_rels/.rels":                                  defaultRootRels,
		"docProps/core.xml":                            strings.ReplaceAll(defaultCoreProps, "Weekly 주간업무보고", escapeXML(title)),
		"ppt/presProps.xml":                            defaultPresProps,
		"ppt/viewProps.xml":                            defaultViewProps,
		"ppt/tableStyles.xml":                          defaultTableStyles,
		"ppt/slideLayouts/slideLayout1.xml":            defaultSlideLayout,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": defaultSlideLayoutRels,
		"ppt/slideMasters/slideMaster1.xml":            defaultSlideMaster,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": defaultSlideMasterRels,
		"ppt/theme/theme1.xml":                         defaultTheme,
	}
	media := map[string][]byte{}

	// rId1 is the slide master; slides follow, then the fixed presentation parts.
	var slideIDs, slideRels, overrides strings.Builder
	extensions := map[string]string{}
	for index, slide := range slides {
		number := index + 1
		relID := index + 2
		slideName := fmt.Sprintf("ppt/slides/slide%d.xml", number)
		files[slideName] = slideDocument(slide.Shapes)
		relations := `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>`
		for _, item := range slide.Media {
			relations += fmt.Sprintf(`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/%s"/>`, item.RelID, item.Name)
			media["ppt/media/"+item.Name] = item.Bytes
			extensions[item.Extension] = item.ContentType
		}
		files[fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", number)] =
			`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + relations + `</Relationships>`
		fmt.Fprintf(&slideIDs, `<p:sldId id="%d" r:id="rId%d"/>`, 255+number, relID)
		fmt.Fprintf(&slideRels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, relID, number)
		fmt.Fprintf(&overrides, `<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, number)
	}
	fixedRelID := len(slides) + 2
	presentationRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>` +
		slideRels.String() +
		fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/presProps" Target="presProps.xml"/>`, fixedRelID) +
		fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/viewProps" Target="viewProps.xml"/>`, fixedRelID+1) +
		fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/tableStyles" Target="tableStyles.xml"/>`, fixedRelID+2) +
		`</Relationships>`
	files["ppt/_rels/presentation.xml.rels"] = presentationRels
	files["ppt/presentation.xml"] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>` +
		`<p:sldIdLst>` + slideIDs.String() + `</p:sldIdLst>` +
		fmt.Sprintf(`<p:sldSz cx="%d" cy="%d"/>`, widthEMU, heightEMU) +
		`<p:notesSz cx="6858000" cy="9144000"/><p:defaultTextStyle/></p:presentation>`

	defaults := `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>`
	for _, extension := range sortedKeys(extensions) {
		defaults += fmt.Sprintf(`<Default Extension="%s" ContentType="%s"/>`, extension, extensions[extension])
	}
	files["[Content_Types].xml"] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		defaults +
		`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
		overrides.String() +
		`<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>` +
		`<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>` +
		`<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>` +
		`<Override PartName="/ppt/presProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presProps+xml"/>` +
		`<Override PartName="/ppt/viewProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.viewProps+xml"/>` +
		`<Override PartName="/ppt/tableStyles.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.tableStyles+xml"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
		`</Types>`
	files["docProps/app.xml"] = strings.Replace(defaultAppProps, `<Slides>1</Slides>`, fmt.Sprintf(`<Slides>%d</Slides>`, len(slides)), 1)

	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range sortedKeys(files) {
		entry, err := writer.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(entry, files[name]); err != nil {
			return nil, err
		}
	}
	for _, name := range sortedByteKeys(media) {
		entry, err := writer.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(media[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func slideDocument(shapes string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:effectLst/></p:bgPr></p:bg><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>` +
		shapes + `</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`
}

// appendSlidesToPPTX grafts generated slides onto an existing deck without
// touching its master, layouts, theme or original slides, so an administrator
// supplied corporate template keeps its identity.
//
// atStart places the new slides before the template's own slides.
func appendSlidesToPPTX(template []byte, slides []builtSlide, atStart bool) ([]byte, error) {
	if len(slides) == 0 {
		return template, nil
	}
	reader, err := zip.NewReader(bytes.NewReader(template), int64(len(template)))
	if err != nil {
		return nil, err
	}
	parts := map[string][]byte{}
	order := []string{}
	highestSlide := 0
	for _, file := range reader.File {
		stream, openErr := file.Open()
		if openErr != nil {
			return nil, openErr
		}
		data, readErr := io.ReadAll(stream)
		stream.Close()
		if readErr != nil {
			return nil, readErr
		}
		parts[file.Name] = data
		order = append(order, file.Name)
		if match := slideNumberPattern.FindStringSubmatch(file.Name); match != nil {
			if number, convErr := strconv.Atoi(match[1]); convErr == nil && number > highestSlide {
				highestSlide = number
			}
		}
	}
	contentTypes, ok := parts["[Content_Types].xml"]
	if !ok {
		return nil, errors.New("PPTX에 [Content_Types].xml이 없습니다")
	}
	presentation, ok := parts["ppt/presentation.xml"]
	if !ok {
		return nil, errors.New("PPTX에 ppt/presentation.xml이 없습니다")
	}
	presentationRels, ok := parts["ppt/_rels/presentation.xml.rels"]
	if !ok {
		return nil, errors.New("PPTX에 presentation 관계 파일이 없습니다")
	}

	nextRelID := highestRelationID(string(presentationRels)) + 1
	nextSlideID := highestSlideID(string(presentation)) + 1
	if nextSlideID < 256 {
		nextSlideID = 256
	}
	// The template's own slides point at their own layout. Reusing that exact
	// relationship keeps generated slides on the corporate master.
	layoutRelationship := templateLayoutRelationship(parts, highestSlide)

	addedIDs := ""
	addedRels := ""
	addedOverrides := ""
	newDefaults := map[string]string{}
	for _, slide := range slides {
		highestSlide++
		number := highestSlide
		slideName := fmt.Sprintf("ppt/slides/slide%d.xml", number)
		relations := layoutRelationship
		for _, item := range slide.Media {
			relations += fmt.Sprintf(`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/%s"/>`, item.RelID, item.Name)
			mediaName := "ppt/media/" + item.Name
			if _, exists := parts[mediaName]; !exists {
				parts[mediaName] = item.Bytes
				order = append(order, mediaName)
			}
			if !strings.Contains(string(contentTypes), `Extension="`+item.Extension+`"`) {
				newDefaults[item.Extension] = item.ContentType
			}
		}
		parts[slideName] = []byte(slideDocument(slide.Shapes))
		order = append(order, slideName)
		relsName := fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", number)
		parts[relsName] = []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + relations + `</Relationships>`)
		order = append(order, relsName)
		addedIDs += fmt.Sprintf(`<p:sldId id="%d" r:id="rId%d"/>`, nextSlideID, nextRelID)
		addedRels += fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, nextRelID, number)
		addedOverrides += fmt.Sprintf(`<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, number)
		nextSlideID++
		nextRelID++
	}

	updatedPresentation, err := insertSlideIDs(string(presentation), addedIDs, atStart)
	if err != nil {
		return nil, err
	}
	parts["ppt/presentation.xml"] = []byte(updatedPresentation)
	parts["ppt/_rels/presentation.xml.rels"] = []byte(strings.Replace(string(presentationRels), `</Relationships>`, addedRels+`</Relationships>`, 1))

	types := string(contentTypes)
	inserted := ""
	for _, extension := range sortedKeys(newDefaults) {
		inserted += fmt.Sprintf(`<Default Extension="%s" ContentType="%s"/>`, extension, newDefaults[extension])
	}
	if inserted != "" {
		// Defaults must precede overrides in the package content types part.
		if index := strings.Index(types, "<Override"); index >= 0 {
			types = types[:index] + inserted + types[index:]
		} else {
			types = strings.Replace(types, `</Types>`, inserted+`</Types>`, 1)
		}
	}
	types = strings.Replace(types, `</Types>`, addedOverrides+`</Types>`, 1)
	parts["[Content_Types].xml"] = []byte(types)

	if appProps, exists := parts["docProps/app.xml"]; exists {
		total := len(slideNames(parts))
		parts["docProps/app.xml"] = slideCountPattern.ReplaceAll(appProps, []byte(fmt.Sprintf(`<Slides>%d</Slides>`, total)))
	}

	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range order {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			return nil, createErr
		}
		if _, writeErr := entry.Write(parts[name]); writeErr != nil {
			return nil, writeErr
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func insertSlideIDs(presentation, addedIDs string, atStart bool) (string, error) {
	if emptySlideListTag.MatchString(presentation) {
		return emptySlideListTag.ReplaceAllString(presentation, `<p:sldIdLst>`+addedIDs+`</p:sldIdLst>`), nil
	}
	if !slideListPattern.MatchString(presentation) {
		return "", errors.New("PPTX presentation.xml에 슬라이드 목록이 없습니다")
	}
	return slideListPattern.ReplaceAllStringFunc(presentation, func(section string) string {
		existing := slideListPattern.FindStringSubmatch(section)[1]
		if atStart {
			return `<p:sldIdLst>` + addedIDs + existing + `</p:sldIdLst>`
		}
		return `<p:sldIdLst>` + existing + addedIDs + `</p:sldIdLst>`
	}), nil
}

func templateLayoutRelationship(parts map[string][]byte, highestSlide int) string {
	fallback := `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>`
	for number := 1; number <= highestSlide; number++ {
		rels, ok := parts[fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", number)]
		if !ok {
			continue
		}
		if match := layoutRelPattern.Find(rels); match != nil {
			// Force the layout onto rId1 so generated media ids never collide.
			return relationIDPattern.ReplaceAllString(string(match), `Id="rId1"`)
		}
	}
	return fallback
}

func slideNames(parts map[string][]byte) []string {
	result := []string{}
	for name := range parts {
		if slideNumberPattern.MatchString(name) {
			result = append(result, name)
		}
	}
	return result
}

func highestRelationID(rels string) int {
	highest := 0
	for _, match := range relationIDPattern.FindAllStringSubmatch(rels, -1) {
		if value, err := strconv.Atoi(match[1]); err == nil && value > highest {
			highest = value
		}
	}
	return highest
}

func highestSlideID(presentation string) int {
	highest := 255
	for _, match := range slideIDPattern.FindAllStringSubmatch(presentation, -1) {
		if value, err := strconv.Atoi(match[1]); err == nil && value > highest {
			highest = value
		}
	}
	return highest
}

// presentationSlideSize reads the canvas of an existing deck so generated
// slides match it exactly.
func presentationSlideSize(template []byte) (int, int) {
	reader, err := zip.NewReader(bytes.NewReader(template), int64(len(template)))
	if err != nil {
		return defaultSlideCX, defaultSlideCY
	}
	for _, file := range reader.File {
		if file.Name != "ppt/presentation.xml" {
			continue
		}
		stream, openErr := file.Open()
		if openErr != nil {
			return defaultSlideCX, defaultSlideCY
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, 1<<20))
		stream.Close()
		if readErr != nil {
			return defaultSlideCX, defaultSlideCY
		}
		if match := slideSizePattern.FindStringSubmatch(string(data)); match != nil {
			width, widthErr := strconv.Atoi(match[1])
			height, heightErr := strconv.Atoi(match[2])
			if widthErr == nil && heightErr == nil && width > 0 && height > 0 {
				return width, height
			}
		}
	}
	return defaultSlideCX, defaultSlideCY
}

func sortedKeys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func sortedByteKeys(values map[string][]byte) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
