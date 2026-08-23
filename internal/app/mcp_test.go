package app

import "testing"

// An omitted argument must read as omitted. fmt.Sprint turns a missing map key
// into "<nil>", which every `if value == ""` guard in the tool dispatcher then
// waves through as a real value. weekly_reports_search with no arguments sent
// those four characters to PostgreSQL as a date, so the first question an agent
// asks — what reports are there? — always failed.
func TestOmittedMCPArgumentsReadAsEmptyNotNilText(t *testing.T) {
	arguments := map[string]any{"status": "APPROVED", "empty": "", "blank": nil, "number": float64(7)}
	cases := []struct{ name, want string }{
		{"weekStart", ""}, // absent
		{"blank", ""},     // present but null
		{"empty", ""},     // present and empty
		{"status", "APPROVED"},
		{"number", "7"},
	}
	for _, testCase := range cases {
		if got := mcpArgumentString(arguments, testCase.name); got != testCase.want {
			t.Errorf("mcpArgumentString(%q) = %q, want %q", testCase.name, got, testCase.want)
		}
	}
	if got := asString(arguments["weekStart"]); got != "<nil>" {
		t.Fatalf("asString on a missing key returned %q; this test exists because it returns \"<nil>\"", got)
	}
}

// Tool arguments arrive as JSON, so a number is a float64 — and an agent may
// well send it as text instead.
func TestMCPArgumentIntAcceptsWhatJSONActuallyDelivers(t *testing.T) {
	cases := []struct {
		arguments map[string]any
		want      int
	}{
		{map[string]any{}, 100},                        // absent
		{map[string]any{"limit": nil}, 100},            // null
		{map[string]any{"limit": float64(30)}, 30},     // JSON number
		{map[string]any{"limit": "30"}, 30},            // text
		{map[string]any{"limit": float64(9999)}, 100},  // above the ceiling
		{map[string]any{"limit": float64(0)}, 1},       // below the floor
		{map[string]any{"limit": "not a number"}, 100}, // unreadable
	}
	for _, testCase := range cases {
		if got := mcpArgumentInt(testCase.arguments, "limit", 100, 1, 100); got != testCase.want {
			t.Errorf("mcpArgumentInt(%v) = %d, want %d", testCase.arguments, got, testCase.want)
		}
	}
}
