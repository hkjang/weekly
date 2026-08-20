package app

import (
	"strings"
	"unicode"
)

// Spreadsheet applications treat a cell that begins with =, +, - or @ as a
// formula, so an exported CSV carries whatever the report author typed straight
// into a formula engine on the reader's machine. The reader here is usually a
// manager opening their team's export, and the author is any employee, which is
// exactly the direction that makes this worth guarding.
//
// The guard is a leading apostrophe: spreadsheets read the rest of the cell as
// text and the apostrophe itself is not part of the value.
//
// A leading hyphen followed by whitespace is left alone. It is how people write
// bullet lines — the same "- ", "* ", "• " markers the rollup already strips —
// and no formula or DDE payload can start that way, since every one of them
// needs a function name or a command right after the operator. Escaping those
// would put an apostrophe in front of a large share of ordinary report text.
func csvSafe(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '@', '\t', '\r':
		return "'" + value
	case '-':
		rest := strings.TrimPrefix(value, "-")
		if rest == "" {
			return value
		}
		if first := []rune(rest)[0]; unicode.IsSpace(first) {
			return value
		}
		return "'" + value
	}
	return value
}

func csvSafeRow(values []string) []string {
	row := make([]string, len(values))
	for index, value := range values {
		row[index] = csvSafe(value)
	}
	return row
}
