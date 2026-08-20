package app

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// The colours the presentation screen borrows from the organisation's own PPTX
// template.
//
// Until now the exported file used the corporate template while the screen used
// a fixed blue and teal gradient, so the same report looked like two different
// companies depending on which medium someone saw it in. The template is
// already uploaded by an administrator and already carries a colour scheme, so
// the accent is there to be read rather than asked for a second time.

// Only the two colours a slide actually needs. A full theme import would bring
// fonts and six accents that have nowhere to go on a screen deck built around
// one highlight colour.
type presentTheme struct {
	// Accent tints the eyebrow, the progress bar and the section rule.
	Accent string `json:"accent"`
	// Source says where the colour came from, so the screen can explain itself
	// instead of appearing to have picked a colour at random.
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
}

var (
	colorSchemePattern = regexp.MustCompile(`(?s)<a:clrScheme.*?</a:clrScheme>`)
	accentPattern      = regexp.MustCompile(`<a:accent1>\s*<a:(?:srgbClr val="([0-9A-Fa-f]{6})"|sysClr[^>]*lastClr="([0-9A-Fa-f]{6})")`)
)

// themeAccentFromPPTX reads accent1 out of a package's theme part.
//
// accent1 is the colour PowerPoint uses for the first highlight in every
// built-in layout, which makes it the closest thing a deck has to "the company
// colour". Anything unreadable returns empty rather than a guess.
func themeAccentFromPPTX(deck []byte) string {
	reader, err := zip.NewReader(bytes.NewReader(deck), int64(len(deck)))
	if err != nil {
		return ""
	}
	for _, file := range reader.File {
		if !strings.HasPrefix(file.Name, "ppt/theme/") || !strings.HasSuffix(file.Name, ".xml") {
			continue
		}
		stream, openErr := file.Open()
		if openErr != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(stream, 2<<20))
		stream.Close()
		if readErr != nil {
			continue
		}
		scheme := colorSchemePattern.Find(body)
		if scheme == nil {
			continue
		}
		match := accentPattern.FindSubmatch(scheme)
		if match == nil {
			continue
		}
		value := string(match[1])
		if value == "" {
			value = string(match[2])
		}
		if value != "" {
			return "#" + strings.ToUpper(value)
		}
	}
	return ""
}

// presentThemeFor resolves the accent for the deck the caller would export, so
// the screen and the file agree.
func (a *App) presentThemeFor(ctx context.Context) presentTheme {
	if body, err := os.ReadFile(customPPTXPath); err == nil {
		if accent := themeAccentFromPPTX(body); accent != "" {
			theme := presentTheme{Accent: accent, Source: "TEMPLATE"}
			if info, infoErr := a.loadPPTXTemplateInfo(ctx); infoErr == nil {
				theme.Name = info.OriginalName
			}
			return theme
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		a.logger.Warn("read pptx template for theme", "error", err)
	}
	if len(a.defaultPPTX) > 0 {
		if accent := themeAccentFromPPTX(a.defaultPPTX); accent != "" {
			return presentTheme{Accent: accent, Source: "REFERENCE", Name: a.defaultPPTXName}
		}
	}
	return presentTheme{Source: "DEFAULT"}
}

// presentThemeInfo serves the accent to the presentation screen. Readable by any
// signed-in user because everyone who can present needs it.
func (a *App) presentThemeInfo(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, a.presentThemeFor(r.Context()))
}
