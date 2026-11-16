package app

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// finalizePPTX is the last thing every exported deck passes through, and it
// exists because PowerPoint's repair prompt — "읽을 수 없는 내용이 있습니다.
// 복구하시겠습니까?" followed by "읽을 수 없는 내용을 제거했습니다" — was
// what people saw when they opened a weekly report.
//
// The cause was not one bug but a shape of bug. The export edits somebody
// else's package: the house template is a PowerPoint file with a notes slide
// on three of its four pages, and the export drops the pages a short week does
// not need. It removed each dropped slide from the slide list, the content
// types and the presentation's relationships — and left its notes slide
// behind, still pointing at the slide that was gone. A relationship to a part
// that is not there is precisely what PowerPoint repairs. Office 2016 is the
// strictest reader this product meets, and it refuses the package outright.
//
// Fixing that one place would leave the next one: a corporate template with
// comments, sections, tags or charts on a dropped page breaks the same way. So
// every deck is closed under its own relationships here instead:
//
//   - parts nothing reaches any more are removed, with their relationships —
//     the orphaned notes slide, its comments, a chart only that page used
//   - content-type overrides for parts that are gone are removed, and every
//     part that remains has a content type
//   - sections name only slides that exist, and every slide is in one
//   - docProps/app.xml counts the slides and notes the deck really has
//   - [Content_Types].xml is written first, directory entries are dropped,
//     and each entry gets a clean header instead of the template's
//
// None of this changes what is on a slide. pptxProblems in the tests is the
// other half: the rules a finished deck has to satisfy, checked on every
// export path.
func finalizePPTX(deck []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(deck), int64(len(deck)))
	if err != nil {
		return nil, err
	}
	parts := map[string][]byte{}
	modified := map[string]time.Time{}
	order := []string{}
	for _, file := range reader.File {
		name := strings.TrimPrefix(strings.ReplaceAll(file.Name, `\`, "/"), "/")
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			return nil, err
		}
		// Two entries with one name is a package PowerPoint repairs, so only
		// one is written. The later one wins: every step that builds a deck
		// here writes a part by overwriting it, so the later entry is the one
		// that step meant.
		if _, seen := parts[name]; !seen {
			order = append(order, name)
		}
		parts[name] = data
		modified[name] = file.Modified
	}
	if _, ok := parts["[Content_Types].xml"]; !ok {
		return nil, fmt.Errorf("PPTX에 [Content_Types].xml이 없습니다")
	}

	// Close the package under its relationships, starting from the root.
	reachable := map[string]bool{}
	queue := []string{""}
	for len(queue) > 0 {
		source := queue[0]
		queue = queue[1:]
		rels, ok := parts[relationshipsPartFor(source)]
		if !ok {
			continue
		}
		for _, relation := range parseRelationships(rels) {
			if strings.EqualFold(relation.Mode, "External") {
				continue
			}
			target := resolvePartTarget(source, relation.Target)
			if _, exists := parts[target]; exists && !reachable[target] {
				reachable[target] = true
				queue = append(queue, target)
			}
		}
	}
	kept := order[:0:0]
	for _, name := range order {
		switch {
		case name == "[Content_Types].xml":
		case strings.HasSuffix(name, ".rels"):
			source := relationshipSource(name)
			if source != "" && !reachable[source] {
				delete(parts, name)
				continue
			}
		case !reachable[name]:
			delete(parts, name)
			continue
		}
		kept = append(kept, name)
	}
	order = kept

	parts["[Content_Types].xml"] = []byte(reconcileContentTypes(string(parts["[Content_Types].xml"]), order))
	if presentation, ok := parts["ppt/presentation.xml"]; ok {
		parts["ppt/presentation.xml"] = []byte(reconcileSections(string(presentation)))
	}
	if app, ok := parts["docProps/app.xml"]; ok {
		slides, notes := 0, 0
		for _, name := range order {
			switch {
			case slideNumberPattern.MatchString(name):
				slides++
			case notesSlidePartPattern.MatchString(name):
				notes++
			}
		}
		app = slideCountPattern.ReplaceAll(app, []byte(fmt.Sprintf(`<Slides>%d</Slides>`, slides)))
		app = notesCountPattern.ReplaceAll(app, []byte(fmt.Sprintf(`<Notes>%d</Notes>`, notes)))
		parts["docProps/app.xml"] = app
	}

	// [Content_Types].xml first and the root relationships second, which is
	// where PowerPoint writes them and where a streaming reader looks.
	sort.SliceStable(order, func(i, j int) bool { return packageRank(order[i]) < packageRank(order[j]) })
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range order {
		// A fresh header: the template's carried its own flags, extra fields
		// and sizes, none of which describe the entry being written now. A
		// generated part has no time of its own, and a zero time is written
		// as the MS-DOS date 0/0/1980, which is not a date; a fixed one keeps
		// two exports of one report byte-identical.
		stamp := modified[name]
		if stamp.IsZero() {
			stamp = packageEpoch
		}
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: stamp})
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(parts[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

var packageEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

func packageRank(name string) int {
	switch name {
	case "[Content_Types].xml":
		return 0
	case "_rels/.rels":
		return 1
	}
	return 2
}

var (
	notesSlidePartPattern  = regexp.MustCompile(`^ppt/notesSlides/notesSlide\d+\.xml$`)
	notesCountPattern      = regexp.MustCompile(`<Notes>\d+</Notes>`)
	contentOverridePattern = regexp.MustCompile(`<Override\s[^>]*?PartName="([^"]+)"[^>]*/>`)
	contentDefaultPattern  = regexp.MustCompile(`<Default\s[^>]*?Extension="([^"]+)"[^>]*/>`)
	sectionListPattern     = regexp.MustCompile(`(?s)<p14:sectionLst\b[^>]*>.*?</p14:sectionLst>`)
	sectionSlidesPattern   = regexp.MustCompile(`(?s)(<p14:sldIdLst>)(.*?)(</p14:sldIdLst>)|<p14:sldIdLst\s*/>`)
	sectionSlidePattern    = regexp.MustCompile(`<p14:sldId\s+id="(\d+)"\s*/>`)
	sectionPattern         = regexp.MustCompile(`(?s)<p14:section\b[^>]*?/>|<p14:section\b[^>]*>.*?</p14:section>`)
	presentationSlideList  = regexp.MustCompile(`(?s)<p:sldIdLst>(.*?)</p:sldIdLst>`)
)

// mediaContentTypes are the parts a deck can carry without an override, by
// extension. Only formats PowerPoint 2016 reads.
var mediaContentTypes = map[string]string{
	"png": "image/png", "jpeg": "image/jpeg", "jpg": "image/jpeg", "jpe": "image/jpeg", "jfif": "image/jpeg",
	"gif": "image/gif", "bmp": "image/bmp", "tif": "image/tiff", "tiff": "image/tiff",
	"emf": "image/x-emf", "wmf": "image/x-wmf", "svg": "image/svg+xml", "wdp": "image/vnd.ms-photo",
	"xml": "application/xml", "rels": "application/vnd.openxmlformats-package.relationships+xml",
	"bin":     "application/vnd.openxmlformats-officedocument.oleObject",
	"xlsx":    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"fntdata": "application/x-fontdata", "odttf": "application/vnd.openxmlformats-officedocument.obfuscatedFont",
	"mp4": "video/mp4", "m4a": "audio/mp4", "wav": "audio/wav", "mp3": "audio/mpeg",
}

// reconcileContentTypes removes overrides for parts that are gone and adds a
// default for any extension a remaining part needs and the list lacks.
func reconcileContentTypes(types string, parts []string) string {
	present := map[string]bool{}
	for _, name := range parts {
		present[strings.ToLower(name)] = true
	}
	overridden := map[string]bool{}
	types = contentOverridePattern.ReplaceAllStringFunc(types, func(element string) string {
		name := strings.ToLower(strings.TrimPrefix(contentOverridePattern.FindStringSubmatch(element)[1], "/"))
		if !present[name] || overridden[name] {
			return ""
		}
		overridden[name] = true
		return element
	})
	defaults := map[string]bool{}
	types = contentDefaultPattern.ReplaceAllStringFunc(types, func(element string) string {
		extension := strings.ToLower(contentDefaultPattern.FindStringSubmatch(element)[1])
		if defaults[extension] {
			return ""
		}
		defaults[extension] = true
		return element
	})
	missing := map[string]string{}
	for _, name := range parts {
		if name == "[Content_Types].xml" || overridden[strings.ToLower(name)] {
			continue
		}
		extension := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
		if defaults[extension] {
			continue
		}
		if contentType, known := mediaContentTypes[extension]; known {
			missing[extension] = contentType
		}
	}
	if len(missing) == 0 {
		return types
	}
	inserted := ""
	for _, extension := range sortedKeys(missing) {
		inserted += fmt.Sprintf(`<Default Extension="%s" ContentType="%s"/>`, extension, missing[extension])
	}
	// Defaults come before overrides in the part PowerPoint writes.
	if index := strings.Index(types, "<Override"); index >= 0 {
		return types[:index] + inserted + types[index:]
	}
	return strings.Replace(types, "</Types>", inserted+"</Types>", 1)
}

// reconcileSections keeps PowerPoint 2010+ sections in step with the slide
// list. The sections, read in order, have to name exactly the slides of the
// slide list in the slide list's order: a section naming a slide that was
// removed is a repair, and so is a slide no section names.
//
// Each section is rebuilt from the slide order rather than patched. A slide
// keeps the section it was in; a slide that was in none — a capture page or the
// issue page added to a template — joins the section of the slide before it,
// or the first section when it opens the deck. A section whose slides were all
// dropped stays, empty, which PowerPoint allows and keeps its name visible.
func reconcileSections(presentation string) string {
	sections := sectionListPattern.FindString(presentation)
	if sections == "" {
		return presentation
	}
	listed := presentationSlideList.FindStringSubmatch(presentation)
	order := []string{}
	if listed != nil {
		for _, match := range slideIDPattern.FindAllStringSubmatch(listed[1], -1) {
			order = append(order, match[1])
		}
	}
	locations := sectionPattern.FindAllStringIndex(sections, -1)
	if len(locations) == 0 {
		return presentation
	}
	owner := map[string]int{}
	for index, location := range locations {
		for _, match := range sectionSlidePattern.FindAllStringSubmatch(sections[location[0]:location[1]], -1) {
			if _, taken := owner[match[1]]; !taken {
				owner[match[1]] = index
			}
		}
	}
	assigned := make([]string, len(locations))
	current := 0
	for _, id := range order {
		// Never backwards: a section is a contiguous run of the slide order.
		if section, ok := owner[id]; ok && section > current {
			current = section
		}
		assigned[current] += `<p14:sldId id="` + id + `"/>`
	}
	rebuilt := sections
	for index := len(locations) - 1; index >= 0; index-- {
		location := locations[index]
		section := rebuilt[location[0]:location[1]]
		list := `<p14:sldIdLst>` + assigned[index] + `</p14:sldIdLst>`
		switch {
		case sectionSlidesPattern.MatchString(section):
			replaced := false
			section = sectionSlidesPattern.ReplaceAllStringFunc(section, func(string) string {
				if replaced {
					return ""
				}
				replaced = true
				return list
			})
		case strings.HasSuffix(section, "/>"):
			section = strings.TrimSuffix(strings.TrimSuffix(section, "/>"), " ") + ">" + list + `</p14:section>`
		default:
			section = strings.TrimSuffix(section, `</p14:section>`) + list + `</p14:section>`
		}
		rebuilt = rebuilt[:location[0]] + section + rebuilt[location[1]:]
	}
	return strings.Replace(presentation, sections, rebuilt, 1)
}

type partRelationship struct {
	ID, Type, Target, Mode string
}

func parseRelationships(data []byte) []partRelationship {
	var document struct {
		Relationships []struct {
			ID     string `xml:"Id,attr"`
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &document); err != nil {
		return nil
	}
	result := make([]partRelationship, 0, len(document.Relationships))
	for _, item := range document.Relationships {
		result = append(result, partRelationship{item.ID, item.Type, item.Target, item.Mode})
	}
	return result
}

// relationshipsPartFor is where a part's relationships live: "" (the package)
// keeps them in _rels/.rels, ppt/slides/slide1.xml in
// ppt/slides/_rels/slide1.xml.rels.
func relationshipsPartFor(source string) string {
	if source == "" {
		return "_rels/.rels"
	}
	directory, base := path.Split(source)
	return directory + "_rels/" + base + ".rels"
}

// relationshipSource is the inverse: the part a .rels part belongs to, or ""
// for the package's own.
func relationshipSource(rels string) string {
	directory, base := path.Split(rels)
	directory = strings.TrimSuffix(directory, "/")
	if path.Base(directory) != "_rels" {
		return ""
	}
	parent := path.Dir(directory)
	base = strings.TrimSuffix(base, ".rels")
	if base == "" {
		return ""
	}
	if parent == "." {
		return base
	}
	return parent + "/" + base
}

// resolvePartTarget turns a relationship target into a part name.
func resolvePartTarget(source, target string) string {
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	directory := path.Dir(source)
	if source == "" {
		directory = ""
	}
	return strings.TrimPrefix(path.Clean(path.Join(directory, target)), "/")
}
