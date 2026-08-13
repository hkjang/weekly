package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Column widths are counted in characters by PostgreSQL. Validation that counted
// bytes both rejected valid Korean input and, where a field had no check at all,
// let an oversized value reach the database and fail the request with a 500.

func TestValidateItemsUsesCharacterLimits(t *testing.T) {
	ok := reportItem{Title: strings.Repeat("업", 240), Category: strings.Repeat("구", 80)}
	if err := validateItems([]reportItem{ok}); err != nil {
		t.Errorf("a 240 character title with an 80 character category was rejected: %v", err)
	}
	longTitle := reportItem{Title: strings.Repeat("업", 241)}
	if err := validateItems([]reportItem{longTitle}); err == nil {
		t.Error("a 241 character title must be rejected before it reaches varchar(240)")
	}
	longCategory := reportItem{Title: "제목", Category: strings.Repeat("구", 81)}
	if err := validateItems([]reportItem{longCategory}); err == nil {
		t.Error("an 81 character category must be rejected before it reaches varchar(80)")
	}
	if err := validateItems([]reportItem{{Title: "  "}}); err == nil {
		t.Error("a blank title must be rejected")
	}
	if err := validateItems([]reportItem{{Title: "제목", Progress: 101}}); err == nil {
		t.Error("progress above 100 must be rejected")
	}
	body := reportItem{Title: "제목", CurrentResult: strings.Repeat("가", 20001)}
	if err := validateItems([]reportItem{body}); err == nil {
		t.Error("an oversized body field must be rejected")
	}
}

func TestTrimRunesNeverSplitsACharacter(t *testing.T) {
	// Truncating by bytes used to cut a multi-byte character in half, and
	// PostgreSQL then rejected the value as invalid UTF-8.
	for _, limit := range []int{0, 1, 5, 99, 10000} {
		got := trimRunes(strings.Repeat("가", 12000), limit)
		if !utf8.ValidString(got) {
			t.Fatalf("trimRunes(limit=%d) produced invalid UTF-8", limit)
		}
		if runeLength(got) > limit {
			t.Fatalf("trimRunes(limit=%d) returned %d characters", limit, runeLength(got))
		}
	}
	if got := trimRunes("짧은 값", 100); got != "짧은 값" {
		t.Errorf("a short value must pass through unchanged, got %q", got)
	}
}

func TestBoundedCountsCharacters(t *testing.T) {
	check := bounded(1, 80)
	if !check(strings.Repeat("가", 80)) {
		t.Error("80 Korean characters must pass an 80 character bound")
	}
	if check(strings.Repeat("가", 81)) {
		t.Error("81 characters must fail an 80 character bound")
	}
	if check("") {
		t.Error("an empty value must fail a minimum of 1")
	}
}

func TestValidateProfileLengths(t *testing.T) {
	if err := validateProfileLengths(strings.Repeat("이", 120), strings.Repeat("a", 255)+"@x.io"); err == nil {
		t.Error("an email longer than varchar(255) must be rejected")
	}
	if err := validateProfileLengths(strings.Repeat("이", 121), ""); err == nil {
		t.Error("a display name longer than varchar(120) must be rejected")
	}
	if err := validateProfileLengths(strings.Repeat("이", 120), "user@example.com"); err != nil {
		t.Errorf("a 120 character Korean display name must be accepted: %v", err)
	}
}

func TestOrganizationCodeStaysInsideItsColumn(t *testing.T) {
	if organizationCodePattern.MatchString(strings.Repeat("A", 61)) {
		t.Error("a 61 character code must be rejected before it reaches varchar(60)")
	}
	if !organizationCodePattern.MatchString(strings.Repeat("A", 60)) {
		t.Error("a 60 character code must be accepted")
	}
	if organizationCodePattern.MatchString("한글코드") {
		t.Error("non-ASCII codes must be rejected")
	}
}
