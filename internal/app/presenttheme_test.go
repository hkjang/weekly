package app

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

func themedPPTX(t *testing.T, scheme string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("ppt/theme/theme1.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(scheme)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestThemeAccentFromPPTX(t *testing.T) {
	cases := []struct {
		name string
		xml  string
		want string
	}{
		{
			name: "explicit rgb accent",
			xml: `<a:theme><a:themeElements><a:clrScheme name="Office">
				<a:dk1><a:sysClr val="windowText" lastClr="000000"/></a:dk1>
				<a:accent1><a:srgbClr val="c0392b"/></a:accent1>
				<a:accent2><a:srgbClr val="2980b9"/></a:accent2>
				</a:clrScheme></a:themeElements></a:theme>`,
			want: "#C0392B",
		},
		{
			// Some templates express the accent as a system colour with the
			// resolved value alongside it.
			name: "system colour with a resolved value",
			xml:  `<a:clrScheme name="X"><a:accent1><a:sysClr val="windowText" lastClr="1F4E79"/></a:accent1></a:clrScheme>`,
			want: "#1F4E79",
		},
		{
			// A guess here would put an arbitrary colour on every slide in the
			// company, so nothing is better than something.
			name: "no colour scheme at all",
			xml:  `<a:theme><a:themeElements/></a:theme>`,
			want: "",
		},
		{
			name: "scheme without accent1",
			xml:  `<a:clrScheme name="X"><a:accent2><a:srgbClr val="123456"/></a:accent2></a:clrScheme>`,
			want: "",
		},
	}
	for _, item := range cases {
		if got := themeAccentFromPPTX(themedPPTX(t, item.xml)); got != item.want {
			t.Errorf("%s: got=%q want=%q", item.name, got, item.want)
		}
	}
}

func TestThemeAccentIgnoresBrokenPackages(t *testing.T) {
	if got := themeAccentFromPPTX([]byte("not a zip at all")); got != "" {
		t.Errorf("a broken package produced an accent: %q", got)
	}
	if got := themeAccentFromPPTX(nil); got != "" {
		t.Errorf("an empty package produced an accent: %q", got)
	}
}

// The bundled template is what most deployments present against on day one, so
// whatever it carries has to survive the same parser.
func TestThemeAccentReadsTheBundledTemplate(t *testing.T) {
	deck, err := defaultPPTX()
	if err != nil {
		t.Fatal(err)
	}
	accent := themeAccentFromPPTX(deck)
	if accent != "" && (len(accent) != 7 || accent[0] != '#') {
		t.Fatalf("unexpected accent format: %q", accent)
	}
	t.Logf("bundled template accent: %q", accent)
}

// The cache is keyed by the template file, so an upload has to invalidate it.
// Serving the previous organisation's colour after a template change would be a
// worse failure than reading the file every time.
func TestPresentThemeCacheFollowsTheTemplateFile(t *testing.T) {
	original := customPPTXPath
	directory := t.TempDir()
	customPPTXPath = directory + "/weekly-report.pptx"
	t.Cleanup(func() { customPPTXPath = original; themeCache.key, themeCache.value = "", presentTheme{} })
	themeCache.key, themeCache.value = "", presentTheme{}

	first := templateFingerprint()
	if first != "builtin" {
		t.Fatalf("no template should fingerprint as builtin, got %q", first)
	}
	if err := os.WriteFile(customPPTXPath, themedPPTX(t,
		`<a:clrScheme name="X"><a:accent1><a:srgbClr val="AA1111"/></a:accent1></a:clrScheme>`), 0o600); err != nil {
		t.Fatal(err)
	}
	second := templateFingerprint()
	if second == first {
		t.Fatal("uploading a template did not change the fingerprint")
	}
	if err := os.WriteFile(customPPTXPath, themedPPTX(t,
		`<a:clrScheme name="X"><a:accent1><a:srgbClr val="22BB22"/></a:accent1></a:clrScheme>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if third := templateFingerprint(); third == second {
		t.Fatal("replacing the template did not change the fingerprint")
	}
}
