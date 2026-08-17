package app

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
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
	slides := attachmentSlides(items, width, height, func(item storedAttachment) ([]byte, error) {
		return bodies[item.StoredPath], nil
	})
	if len(slides) != 2 {
		t.Fatalf("built %d capture slides, want 2", len(slides))
	}
	result, err := appendSlidesToPPTX(template, slides, false)
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
		width, height, func(storedAttachment) ([]byte, error) { return body, nil })
	result, err := appendSlidesToPPTX(template, slides, true)
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
		defaultSlideCX, defaultSlideCY, func(storedAttachment) ([]byte, error) { return body, nil })
	result, err := appendSlidesToPPTX(template, slides, false)
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
		defaultSlideCX, defaultSlideCY,
		func(storedAttachment) ([]byte, error) { return nil, errNotFound })
	if len(slides) != 0 {
		t.Error("an unreadable capture must be skipped rather than produce a broken slide")
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
