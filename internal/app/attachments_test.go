package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func samplePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func captureAttachment(id int64, name, caption, placement string, width, height int) storedAttachment {
	item := storedAttachment{ContentType: "image/png", Extension: "png", StoredPath: name}
	item.ID = id
	item.Filename = name
	item.Caption = caption
	item.Placement = placement
	item.Width = width
	item.Height = height
	return item
}

func TestCaptureSlidesAppendToGeneratedTemplate(t *testing.T) {
	template, err := referenceStylePPTX()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := readDeck(t, template)
	width, height := presentationSlideSize(template)

	image1 := samplePNG(t, 800, 400)
	image2 := samplePNG(t, 300, 900)
	items := []storedAttachment{
		captureAttachment(1, "capture-a.png", "배포 화면 캡처", placementAfter, 800, 400),
		captureAttachment(2, "capture-b.png", "", placementAfter, 300, 900),
	}
	bodies := map[string][]byte{"capture-a.png": image1, "capture-b.png": image2}
	slides := attachmentSlides(items, width, height, openBodies(bodies))
	if len(slides) != 2 {
		t.Fatalf("built %d capture slides, want 2", len(slides))
	}
	result, err := appendSlidesToPPTX(template, nil, slides)
	if err != nil {
		t.Fatal(err)
	}
	validatePPTXXML(t, result)
	after, names := readDeck(t, result)
	if len(after) != len(before)+2 {
		t.Fatalf("slide count = %d, want %d", len(after), len(before)+2)
	}
	// The original slides must be untouched.
	for index := range before {
		if before[index] != after[index] {
			t.Errorf("template slide %d was modified", index+1)
		}
	}
	if !names["ppt/media/weekly-capture-1.png"] || !names["ppt/media/weekly-capture-2.png"] {
		t.Error("image parts were not added to ppt/media")
	}
	// The caption becomes the slide heading; a missing caption falls back to the
	// file name so the slide is never blank.
	if !strings.Contains(after[len(before)], "배포 화면 캡처") {
		t.Error("caption is missing from the capture slide")
	}
	if !strings.Contains(after[len(before)+1], "capture-b.png") {
		t.Error("a caption-less capture must fall back to its file name")
	}
	for _, slide := range after[len(before):] {
		if !strings.Contains(slide, `r:embed="rId2"`) {
			t.Error("capture slide does not reference its image relationship")
		}
	}
	if !names["ppt/slides/_rels/slide5.xml.rels"] || !names["ppt/slides/_rels/slide6.xml.rels"] {
		t.Error("capture slides are missing relationship parts")
	}
}

func TestCaptureSlidesCanGoFirst(t *testing.T) {
	template, err := referenceStylePPTX()
	if err != nil {
		t.Fatal(err)
	}
	width, height := presentationSlideSize(template)
	body := samplePNG(t, 640, 480)
	slides := attachmentSlides(
		[]storedAttachment{captureAttachment(9, "cover.png", "표지 캡처", placementBefore, 640, 480)},
		width, height, openBodies(map[string][]byte{"cover.png": body}))
	result, err := appendSlidesToPPTX(template, slides, nil)
	if err != nil {
		t.Fatal(err)
	}
	validatePPTXXML(t, result)
	// The new slide id must be listed before the template's own slides.
	reader := mustReadPart(t, result, "ppt/presentation.xml")
	list := slideListPattern.FindStringSubmatch(reader)
	if list == nil {
		t.Fatal("presentation.xml has no slide list")
	}
	ids := slideIDPattern.FindAllStringSubmatch(list[1], -1)
	if len(ids) != 5 {
		t.Fatalf("slide list has %d entries, want 5", len(ids))
	}
	if ids[0][1] != "260" {
		t.Errorf("first slide id = %s, want the appended capture (260)", ids[0][1])
	}
}

func TestAppendSlidesRegistersImageContentType(t *testing.T) {
	template, err := defaultPPTX()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustReadPart(t, template, "[Content_Types].xml"), `Extension="png"`) {
		t.Skip("template already declares png")
	}
	body := samplePNG(t, 200, 100)
	slides := attachmentSlides(
		[]storedAttachment{captureAttachment(3, "x.png", "캡처", placementAfter, 200, 100)},
		defaultSlideCX, defaultSlideCY, openBodies(map[string][]byte{"x.png": body}))
	result, err := appendSlidesToPPTX(template, nil, slides)
	if err != nil {
		t.Fatal(err)
	}
	validatePPTXXML(t, result)
	types := mustReadPart(t, result, "[Content_Types].xml")
	if !strings.Contains(types, `<Default Extension="png" ContentType="image/png"/>`) {
		t.Error("png default content type was not registered")
	}
	// Defaults have to precede overrides for the package to be valid.
	if strings.Index(types, `Extension="png"`) > strings.Index(types, "<Override") {
		t.Error("the png default must appear before the first override")
	}
}

func TestAttachmentSlidesSkipUnreadableFiles(t *testing.T) {
	slides := attachmentSlides(
		[]storedAttachment{captureAttachment(1, "missing.png", "", placementAfter, 100, 100)},
		defaultSlideCX, defaultSlideCY, openBodies(nil))
	if len(slides) != 0 {
		t.Error("an unreadable capture must be skipped rather than produce a broken slide")
	}
}

// openBodies adapts an in-memory fixture to the lazy reader attachmentSlides
// expects. Missing entries report "not there" the same way a missing file does.
func openBodies(bodies map[string][]byte) func(storedAttachment) (func() ([]byte, error), bool) {
	return func(item storedAttachment) (func() ([]byte, error), bool) {
		body, ok := bodies[item.StoredPath]
		if !ok || len(body) == 0 {
			return nil, false
		}
		return func() ([]byte, error) { return body, nil }, true
	}
}

func TestSafeAttachmentPathStaysInsideTheDirectory(t *testing.T) {
	original := stateDirectoryAttachments
	stateDirectoryAttachments = "/var/lib/weekly/attachments"
	defer func() { stateDirectoryAttachments = original }()
	for _, stored := range []string{"../../etc/passwd", "/etc/passwd", "12/../../../etc/passwd"} {
		got := safeAttachmentPath(stored)
		if !strings.HasPrefix(got, "/var/lib/weekly/attachments/") {
			t.Errorf("safeAttachmentPath(%q) = %q, escaped the attachment directory", stored, got)
		}
	}
	if got := safeAttachmentPath("12/abc.png"); got != "/var/lib/weekly/attachments/12/abc.png" {
		t.Errorf("a normal path was rewritten: %q", got)
	}
}

func mustReadPart(t *testing.T, archive []byte, name string) string {
	t.Helper()
	_, names := readDeck(t, archive)
	if !names[name] {
		t.Fatalf("archive has no part %s", name)
	}
	reader := zipPart(t, archive, name)
	return reader
}

func zipPart(t *testing.T, archive []byte, name string) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		stream, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer stream.Close()
		var buffer bytes.Buffer
		if _, copyErr := buffer.ReadFrom(stream); copyErr != nil {
			t.Fatal(copyErr)
		}
		return buffer.String()
	}
	t.Fatalf("part %s not found", name)
	return ""
}

// A row can outlive its file when the state volume is not carried across an
// upgrade. The list has to say so, or the screen shows a broken image and the
// export silently drops a slide with no explanation anywhere.
func TestAttachmentAvailabilityReflectsTheStoredFile(t *testing.T) {
	root := t.TempDir()
	original := stateDirectoryAttachments
	stateDirectoryAttachments = root
	defer func() { stateDirectoryAttachments = original }()

	if err := os.MkdirAll(filepath.Join(root, "7"), 0o700); err != nil {
		t.Fatal(err)
	}
	present := filepath.Join("7", "present.png")
	if err := os.WriteFile(filepath.Join(root, present), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name, stored string
		want         bool
	}{
		{"file on disk", present, true},
		{"file removed", filepath.Join("7", "gone.png"), false},
	} {
		if _, err := os.Stat(safeAttachmentPath(testCase.stored)); (err == nil) != testCase.want {
			t.Errorf("%s: availability = %v, want %v", testCase.name, err == nil, testCase.want)
		}
	}
}

// Both ends in one call: the package is read and rewritten once, and the two
// groups still land on the correct sides of the template's own slides.
func TestAppendSlidesPlacesBothGroupsInOnePass(t *testing.T) {
	template, err := defaultPPTX()
	if err != nil {
		t.Fatal(err)
	}
	original, _ := readDeck(t, template)
	width, height := presentationSlideSize(template)
	bodies := map[string][]byte{"cover.png": samplePNG(t, 640, 480), "tail.png": samplePNG(t, 400, 300)}
	reads := map[string]int{}
	open := func(item storedAttachment) (func() ([]byte, error), bool) {
		body, ok := bodies[item.StoredPath]
		if !ok {
			return nil, false
		}
		return func() ([]byte, error) { reads[item.StoredPath]++; return body, nil }, true
	}
	before := attachmentSlides([]storedAttachment{captureAttachment(1, "cover.png", "표지", placementBefore, 640, 480)}, width, height, open)
	after := attachmentSlides([]storedAttachment{captureAttachment(2, "tail.png", "마무리", placementAfter, 400, 300)}, width, height, open)

	// Nothing has been read yet: building the slides only needs the stored size.
	if len(reads) != 0 {
		t.Fatalf("images were read while building slides: %v", reads)
	}

	result, err := appendSlidesToPPTX(template, before, after)
	if err != nil {
		t.Fatal(err)
	}
	validatePPTXXML(t, result)
	if reads["cover.png"] != 1 || reads["tail.png"] != 1 {
		t.Fatalf("each image should be read exactly once at write time: %v", reads)
	}
	slides, _ := readDeck(t, result)
	if len(slides) != len(original)+2 {
		t.Fatalf("slide count = %d, want %d", len(slides), len(original)+2)
	}
	list := slideListPattern.FindStringSubmatch(mustReadPart(t, result, "ppt/presentation.xml"))
	if list == nil {
		t.Fatal("presentation.xml has no slide list")
	}
	ids := slideIDPattern.FindAllStringSubmatch(list[1], -1)
	if len(ids) != len(original)+2 {
		t.Fatalf("slide list has %d entries, want %d", len(ids), len(original)+2)
	}
	// The template's own ids must all sit between the two new ones, whatever
	// numbers the template happens to use.
	templateList := slideListPattern.FindStringSubmatch(mustReadPart(t, template, "ppt/presentation.xml"))
	existing := map[string]bool{}
	for _, match := range slideIDPattern.FindAllStringSubmatch(templateList[1], -1) {
		existing[match[1]] = true
	}
	if existing[ids[0][1]] {
		t.Errorf("first slide id = %s, which is one of the template's own", ids[0][1])
	}
	if existing[ids[len(ids)-1][1]] {
		t.Errorf("last slide id = %s, which is one of the template's own", ids[len(ids)-1][1])
	}
	for _, match := range ids[1 : len(ids)-1] {
		if !existing[match[1]] {
			t.Errorf("slide id %s ended up in the middle instead of at an end", match[1])
		}
	}
}

// Twenty captures at ten megabytes each is what the administrator screen lets a
// deployment configure, so an export can be asked to carry two hundred
// megabytes of images. Reading them all up front to plan the slides would hold
// every one of them at once, and the geometry the planning needs is already in
// the stored width and height — so the bytes must not be touched until the
// moment each image is written into the package.
//
// Measured on a seeded deployment: twenty captures totalling 44.9 MB took the
// container from 78 MiB to 235 MiB, about 3.5× the payload. That is with the
// reads deferred. Holding them eagerly adds the whole payload again.

// guards: attachmentSlides, appendSlidesToPPTX
func TestCaptureBytesAreNotReadUntilTheyAreWritten(t *testing.T) {
	template, err := referenceStylePPTX()
	if err != nil {
		t.Fatal(err)
	}
	width, height := presentationSlideSize(template)

	const captures = 6
	items := []storedAttachment{}
	bodies := map[string][]byte{}
	reads := map[string]int{}
	for index := 0; index < captures; index++ {
		name := fmt.Sprintf("capture-%d.png", index)
		items = append(items, captureAttachment(int64(index+1), name, "", placementAfter, 640, 480))
		bodies[name] = samplePNG(t, 640, 480)
	}
	open := func(item storedAttachment) (func() ([]byte, error), bool) {
		body, ok := bodies[item.StoredPath]
		if !ok {
			return nil, false
		}
		return func() ([]byte, error) { reads[item.StoredPath]++; return body, nil }, true
	}

	slides := attachmentSlides(items, width, height, open)
	if len(slides) != captures {
		t.Fatalf("built %d slides, want %d", len(slides), captures)
	}
	// Planning is geometry. Nothing here needs a pixel.
	if total := countReads(reads); total != 0 {
		t.Fatalf("%d capture(s) were read while the slides were only being planned — a full export's images would be resident at once", total)
	}

	if _, err := appendSlidesToPPTX(template, nil, slides); err != nil {
		t.Fatal(err)
	}
	// And once each when they are written: reading an image twice doubles the
	// disk traffic of every export and is the kind of thing a refactor adds
	// without anybody noticing.
	for name, count := range reads {
		if count != 1 {
			t.Errorf("%s was read %d times, want 1", name, count)
		}
	}
	if total := countReads(reads); total != captures {
		t.Errorf("%d of %d captures reached the package", total, captures)
	}
}

func countReads(reads map[string]int) int {
	total := 0
	for _, count := range reads {
		total += count
	}
	return total
}
