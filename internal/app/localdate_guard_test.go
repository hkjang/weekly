package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every date this product writes is a plain calendar day — a week starts on a
// Monday, a decision was made on a day — and Date.toISOString() answers in UTC.
// East of Greenwich a local midnight is the previous day, so the conversion
// silently moves the date back one.
//
// It was in four places at once, all shipped:
//
//   - the meeting agenda's week arrows stepped 2026-08-24 to 2026-08-30 rather
//     than 2026-08-31, landing on a date that starts no week and an agenda that
//     finds nothing;
//   - the decision form offered yesterday to anybody recording before 09:00;
//   - the weekly period picker labelled the previous week "이번 주";
//   - the timeline chart's week keys.
//
// One helper now formats dates from the fields the browser already holds in
// local time. This keeps the old way from coming back, because the mistake
// looks completely reasonable at the call site and only shows up in a timezone
// nobody is testing in.
func TestFrontendNeverFormatsACalendarDateThroughUTC(t *testing.T) {
	root := "../../frontend/src"
	offenders := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if extension := filepath.Ext(path); extension != ".ts" && extension != ".tsx" {
			return nil
		}
		// The helper itself names the mistake in order to explain it.
		if strings.HasSuffix(path, "localdate.ts") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for index, line := range strings.Split(string(body), "\n") {
			collapsed := strings.ReplaceAll(line, " ", "")
			if strings.Contains(collapsed, "toISOString().slice(0,10)") {
				offenders = append(offenders, filepath.Base(path)+":"+itoaLine(index+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Skipf("frontend sources are not readable from here: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("이 자리들이 달력 날짜를 UTC로 변환합니다: %s\n"+
			"localdate.ts 의 localDate/todayLocal/shiftWeeks 를 쓰십시오.",
			strings.Join(offenders, ", "))
	}
}

func itoaLine(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
