package app

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// PowerPoint's "읽을 수 없는 내용이 있습니다 — 복구하시겠습니까?" is not one
// fault but a family of them, and PowerPoint never says which. Every member of
// the family that this product has produced, or could produce, is listed here
// as a rule over the package, so a deck that would raise the prompt fails a
// test instead of a meeting.
//
// The rules are the ones PowerPoint 2016 actually enforces when it opens a
// file: the package has to be closed under its own relationships, every part
// needs a content type, and a handful of schema constraints — identifier
// ranges, the three-entry theme style lists, the required children of the view
// properties — are checked rather than tolerated. Running the file through a
// schema validator was not available offline; these are the constraints that
// validator reports for decks built by code like this.

type packagePart struct {
	name string
	data []byte
}

type packageRelationship struct {
	ID, Type, Target, Mode string
}

// pptxProblems returns every rule the package breaks, or nothing.
func pptxProblems(body []byte) []string {
	problems := []string{}
	report := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return []string{"not a zip: " + err.Error()}
	}
	parts := map[string][]byte{}
	order := []string{}
	for _, file := range reader.File {
		name := file.Name
		switch {
		case strings.HasSuffix(name, "/"):
			report("zip: directory entry %q", name)
			continue
		case strings.Contains(name, `\`), strings.HasPrefix(name, "/"):
			report("zip: malformed part name %q", name)
		}
		if _, seen := parts[name]; seen {
			report("zip: duplicate entry %q", name)
		}
		stream, err := file.Open()
		if err != nil {
			report("zip: %s cannot be opened: %v", name, err)
			continue
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			report("zip: %s cannot be read: %v", name, err)
			continue
		}
		parts[name] = data
		order = append(order, name)
	}
	if len(order) == 0 || order[0] != "[Content_Types].xml" {
		report("zip: [Content_Types].xml is not the first entry")
	}

	// Every XML part well formed, which also refuses characters XML 1.0 forbids.
	for _, name := range order {
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".rels") {
			continue
		}
		decoder := xml.NewDecoder(bytes.NewReader(parts[name]))
		for {
			if _, err := decoder.Token(); err == io.EOF {
				break
			} else if err != nil {
				report("xml: %s is not well formed: %v", name, err)
				break
			}
		}
	}

	// Content types: every part has one, and every override names a part.
	types := parts["[Content_Types].xml"]
	if types == nil {
		report("content types: [Content_Types].xml is missing")
	}
	var typesDocument struct {
		Defaults []struct {
			Extension   string `xml:"Extension,attr"`
			ContentType string `xml:"ContentType,attr"`
		} `xml:"Default"`
		Overrides []struct {
			PartName    string `xml:"PartName,attr"`
			ContentType string `xml:"ContentType,attr"`
		} `xml:"Override"`
	}
	_ = xml.Unmarshal(types, &typesDocument)
	defaults := map[string]bool{}
	for _, item := range typesDocument.Defaults {
		if defaults[strings.ToLower(item.Extension)] {
			report("content types: Default %q twice", item.Extension)
		}
		defaults[strings.ToLower(item.Extension)] = true
	}
	overrides := map[string]bool{}
	for _, item := range typesDocument.Overrides {
		name := strings.TrimPrefix(item.PartName, "/")
		if overrides[strings.ToLower(name)] {
			report("content types: Override %q twice", item.PartName)
		}
		overrides[strings.ToLower(name)] = true
		if _, exists := parts[name]; !exists {
			report("content types: Override %q names a part that is not in the package", item.PartName)
		}
	}
	for _, name := range order {
		if name == "[Content_Types].xml" {
			continue
		}
		extension := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
		if !overrides[strings.ToLower(name)] && !defaults[extension] {
			report("content types: %s has no content type", name)
		}
	}

	// Relationships: a rels part belongs to a part that exists, its ids are
	// unique, and every internal target exists.
	relationships := map[string][]packageRelationship{}
	for _, name := range order {
		if !strings.HasSuffix(name, ".rels") {
			continue
		}
		source := relationshipSource(name)
		if source != "" {
			if _, exists := parts[source]; !exists {
				report("rels: %s belongs to %s, which is not in the package", name, source)
			}
		}
		var document struct {
			Relationships []struct {
				ID     string `xml:"Id,attr"`
				Type   string `xml:"Type,attr"`
				Target string `xml:"Target,attr"`
				Mode   string `xml:"TargetMode,attr"`
			} `xml:"Relationship"`
		}
		if err := xml.Unmarshal(parts[name], &document); err != nil {
			continue
		}
		ids := map[string]bool{}
		for _, item := range document.Relationships {
			if ids[item.ID] {
				report("rels: %s uses id %s twice", name, item.ID)
			}
			ids[item.ID] = true
			relationships[source] = append(relationships[source], packageRelationship{item.ID, item.Type, item.Target, item.Mode})
			if strings.EqualFold(item.Mode, "External") {
				continue
			}
			target := resolvePartTarget(source, item.Target)
			if _, exists := parts[target]; !exists {
				report("rels: %s → %s (%s) does not exist", name, item.Target, lastSegment(item.Type))
			}
		}
	}

	// Reachability: a part nothing points at is harmless on its own, but a
	// notes slide left behind by a removed slide points back at nothing, and
	// that is the commonest repair this product caused.
	reachable := map[string]bool{"": true}
	queue := []string{""}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, item := range relationships[current] {
			if strings.EqualFold(item.Mode, "External") {
				continue
			}
			target := resolvePartTarget(current, item.Target)
			if !reachable[target] {
				reachable[target] = true
				queue = append(queue, target)
			}
		}
	}
	for _, name := range order {
		if name == "[Content_Types].xml" || strings.HasSuffix(name, ".rels") {
			continue
		}
		if !reachable[name] {
			report("package: %s is not reachable from the package root", name)
		}
	}

	// Every r:id, r:embed, r:link … in a part names one of that part's
	// relationships.
	for _, name := range order {
		if !strings.HasSuffix(name, ".xml") || name == "[Content_Types].xml" {
			continue
		}
		known := map[string]bool{}
		for _, item := range relationships[name] {
			known[item.ID] = true
		}
		decoder := xml.NewDecoder(bytes.NewReader(parts[name]))
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			for _, attribute := range start.Attr {
				if attribute.Name.Space == relationshipNamespace && !known[attribute.Value] {
					report("rels: %s <%s r:%s=%q> names no relationship", name, start.Name.Local, attribute.Name.Local, attribute.Value)
				}
			}
		}
	}

	problems = append(problems, presentationProblems(parts, relationships)...)
	for _, name := range order {
		switch {
		case strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml"),
			strings.HasPrefix(name, "ppt/slideLayouts/slideLayout") && strings.HasSuffix(name, ".xml"),
			strings.HasPrefix(name, "ppt/slideMasters/slideMaster") && strings.HasSuffix(name, ".xml"),
			strings.HasPrefix(name, "ppt/notesSlides/notesSlide") && strings.HasSuffix(name, ".xml"):
			problems = append(problems, drawingProblems(name, parts[name])...)
		case strings.HasPrefix(name, "ppt/theme/theme") && strings.HasSuffix(name, ".xml"):
			problems = append(problems, themeProblems(name, parts[name])...)
		case name == "ppt/viewProps.xml":
			problems = append(problems, viewPropertiesProblems(parts[name])...)
		}
	}
	// Notes slides and the slides they annotate point at each other.
	for _, name := range order {
		if !strings.HasPrefix(name, "ppt/notesSlides/notesSlide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		for _, item := range relationships[name] {
			if lastSegment(item.Type) != "slide" {
				continue
			}
			slide := resolvePartTarget(name, item.Target)
			back := false
			for _, other := range relationships[slide] {
				if lastSegment(other.Type) == "notesSlide" && resolvePartTarget(slide, other.Target) == name {
					back = true
				}
			}
			if !back {
				report("notes: %s annotates %s, which does not point back at it", name, slide)
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func presentationProblems(parts map[string][]byte, relationships map[string][]packageRelationship) []string {
	problems := []string{}
	report := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	presentation, ok := parts["ppt/presentation.xml"]
	if !ok {
		return []string{"presentation: ppt/presentation.xml is missing"}
	}
	byID := map[string]packageRelationship{}
	for _, item := range relationships["ppt/presentation.xml"] {
		byID[item.ID] = item
	}
	slideIDs := map[uint64]bool{}
	listedTargets := map[string]bool{}
	ids := map[uint64]string{}
	claim := func(id uint64, what string) {
		if previous, taken := ids[id]; taken {
			report("presentation: id %d is used by %s and %s", id, previous, what)
		}
		ids[id] = what
	}
	decoder := xml.NewDecoder(bytes.NewReader(presentation))
	sectionIDs := []uint64{}
	slideOrder := []uint64{}
	inSections, inSectionList := false, false
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch element := token.(type) {
		case xml.StartElement:
			attribute := func(local string) string {
				for _, item := range element.Attr {
					if item.Name.Local == local {
						return item.Value
					}
				}
				return ""
			}
			switch {
			case element.Name.Local == "sectionLst":
				inSections, inSectionList = true, true
			case element.Name.Local == "sldId" && inSections:
				id, _ := strconv.ParseUint(attribute("id"), 10, 64)
				sectionIDs = append(sectionIDs, id)
			case element.Name.Local == "sldId":
				id, err := strconv.ParseUint(attribute("id"), 10, 64)
				if err != nil || id < 256 || id > 2147483647 {
					report("presentation: slide id %q is outside 256..2147483647", attribute("id"))
				}
				if slideIDs[id] {
					report("presentation: slide id %d is listed twice", id)
				}
				slideIDs[id] = true
				slideOrder = append(slideOrder, id)
				rid := ""
				for _, item := range element.Attr {
					if item.Name.Space == relationshipNamespace && item.Name.Local == "id" {
						rid = item.Value
					}
				}
				target, found := byID[rid]
				if !found || lastSegment(target.Type) != "slide" {
					report("presentation: slide id %d reaches %q, which is not a slide relationship", id, rid)
				} else {
					listedTargets[resolvePartTarget("ppt/presentation.xml", target.Target)] = true
				}
			case element.Name.Local == "sldMasterId":
				id, err := strconv.ParseUint(attribute("id"), 10, 64)
				if err != nil || id < 2147483648 {
					report("presentation: slide master id %q is below 2147483648", attribute("id"))
				}
				claim(id, "a slide master")
			}
		case xml.EndElement:
			if element.Name.Local == "sectionLst" {
				inSections = false
			}
		}
	}
	for _, item := range relationships["ppt/presentation.xml"] {
		if lastSegment(item.Type) == "slide" && !listedTargets[resolvePartTarget("ppt/presentation.xml", item.Target)] {
			report("presentation: %s is related but not in the slide list", item.Target)
		}
	}
	if inSectionList && len(sectionIDs) != len(slideOrder) {
		report("presentation: the sections name %d slides, the slide list has %d", len(sectionIDs), len(slideOrder))
	} else if inSectionList {
		for index := range sectionIDs {
			if sectionIDs[index] != slideOrder[index] {
				report("presentation: the sections list slide %d where the slide order has %d", sectionIDs[index], slideOrder[index])
				break
			}
		}
	}
	if len(sectionIDs) > 0 {
		inSection := map[uint64]bool{}
		for _, id := range sectionIDs {
			if !slideIDs[id] {
				report("presentation: a section names slide id %d, which is not in the slide list", id)
			}
			inSection[id] = true
		}
		for id := range slideIDs {
			if !inSection[id] {
				report("presentation: slide id %d is in no section", id)
			}
		}
	}
	// Layout ids share the master's number space and its lower bound.
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasPrefix(name, "ppt/slideMasters/slideMaster") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		decoder := xml.NewDecoder(bytes.NewReader(parts[name]))
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			if element, ok := token.(xml.StartElement); ok && element.Name.Local == "sldLayoutId" {
				for _, item := range element.Attr {
					if item.Name.Local != "id" || item.Name.Space == relationshipNamespace {
						continue
					}
					id, err := strconv.ParseUint(item.Value, 10, 64)
					if err != nil || id < 2147483648 {
						report("%s: slide layout id %q is below 2147483648", name, item.Value)
					}
					claim(id, name+" layout")
				}
			}
		}
	}
	// docProps/app.xml says how many slides there are; keep it honest.
	if app, ok := parts["docProps/app.xml"]; ok {
		var properties struct {
			Slides *int `xml:"Slides"`
		}
		if xml.Unmarshal(app, &properties) == nil && properties.Slides != nil && *properties.Slides != len(slideIDs) {
			report("app.xml: says %d slides, the deck has %d", *properties.Slides, len(slideIDs))
		}
	}
	return problems
}

// drawingProblems checks a slide-like part: unique shape ids, tables whose rows
// match their grid, text bodies with a paragraph, and font sizes in range.
func drawingProblems(name string, data []byte) []string {
	problems := []string{}
	report := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	decoder := xml.NewDecoder(bytes.NewReader(data))
	shapeIDs := map[string]bool{}
	type table struct{ columns, cellsInRow, row int }
	tables := []*table{}
	txBodyParagraphs := []int{}
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch element := token.(type) {
		case xml.StartElement:
			attribute := func(local string) (string, bool) {
				for _, item := range element.Attr {
					if item.Name.Local == local && item.Name.Space != relationshipNamespace {
						return item.Value, true
					}
				}
				return "", false
			}
			switch element.Name.Local {
			case "cNvPr":
				id, _ := attribute("id")
				if shapeIDs[id] {
					report("%s: shape id %s is used twice", name, id)
				}
				shapeIDs[id] = true
			case "tbl":
				tables = append(tables, &table{})
			case "gridCol":
				if len(tables) > 0 {
					tables[len(tables)-1].columns++
				}
			case "tr":
				if len(tables) > 0 {
					tables[len(tables)-1].cellsInRow = 0
					tables[len(tables)-1].row++
				}
			case "tc":
				if len(tables) > 0 {
					tables[len(tables)-1].cellsInRow++
				}
			case "txBody":
				txBodyParagraphs = append(txBodyParagraphs, 0)
			case "p":
				if element.Name.Space == "http://schemas.openxmlformats.org/drawingml/2006/main" && len(txBodyParagraphs) > 0 {
					txBodyParagraphs[len(txBodyParagraphs)-1]++
				}
			case "rPr", "endParaRPr", "defRPr":
				if size, ok := attribute("sz"); ok {
					if value, err := strconv.Atoi(size); err != nil || value < 100 || value > 400000 {
						report("%s: font size %q is outside 100..400000", name, size)
					}
				}
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "tr":
				if len(tables) > 0 {
					current := tables[len(tables)-1]
					if current.cellsInRow != current.columns {
						report("%s: table row %d has %d cells for %d columns", name, current.row, current.cellsInRow, current.columns)
					}
				}
			case "tbl":
				tables = tables[:len(tables)-1]
			case "txBody":
				if txBodyParagraphs[len(txBodyParagraphs)-1] == 0 {
					report("%s: a text body has no paragraph", name)
				}
				txBodyParagraphs = txBodyParagraphs[:len(txBodyParagraphs)-1]
			}
		}
	}
	return problems
}

// themeProblems: the format scheme's four lists each need three entries — the
// subtle, moderate and intense styles shapes refer to by index.
func themeProblems(name string, data []byte) []string {
	var theme struct {
		Format struct {
			Fills struct {
				Items []struct{} `xml:",any"`
			} `xml:"fillStyleLst"`
			Lines struct {
				Items []struct{} `xml:",any"`
			} `xml:"lnStyleLst"`
			Effects struct {
				Items []struct{} `xml:",any"`
			} `xml:"effectStyleLst"`
			Backgrounds struct {
				Items []struct{} `xml:",any"`
			} `xml:"bgFillStyleLst"`
		} `xml:"themeElements>fmtScheme"`
	}
	if err := xml.Unmarshal(data, &theme); err != nil {
		return []string{name + ": unreadable theme"}
	}
	problems := []string{}
	for list, count := range map[string]int{
		"fillStyleLst": len(theme.Format.Fills.Items), "lnStyleLst": len(theme.Format.Lines.Items),
		"effectStyleLst": len(theme.Format.Effects.Items), "bgFillStyleLst": len(theme.Format.Backgrounds.Items),
	} {
		if count < 3 {
			problems = append(problems, fmt.Sprintf("%s: %s has %d entries, the schema requires 3", name, list, count))
		}
	}
	sort.Strings(problems)
	return problems
}

// viewPropertiesProblems: each view element that is present must carry the
// children the schema makes mandatory.
func viewPropertiesProblems(data []byte) []string {
	var view struct {
		Normal *struct {
			Left *struct{} `xml:"restoredLeft"`
			Top  *struct{} `xml:"restoredTop"`
		} `xml:"normalViewPr"`
		Slide *struct {
			Common *struct{} `xml:"cSldViewPr"`
		} `xml:"slideViewPr"`
		Notes *struct {
			Common *struct{} `xml:"cViewPr"`
		} `xml:"notesTextViewPr"`
	}
	if err := xml.Unmarshal(data, &view); err != nil {
		return []string{"viewProps: unreadable"}
	}
	problems := []string{}
	if view.Normal != nil && (view.Normal.Left == nil || view.Normal.Top == nil) {
		problems = append(problems, "viewProps: normalViewPr lacks restoredLeft/restoredTop")
	}
	if view.Slide != nil && view.Slide.Common == nil {
		problems = append(problems, "viewProps: slideViewPr lacks cSldViewPr")
	}
	if view.Notes != nil && view.Notes.Common == nil {
		problems = append(problems, "viewProps: notesTextViewPr lacks cViewPr")
	}
	return problems
}

func lastSegment(value string) string {
	return value[strings.LastIndex(value, "/")+1:]
}
