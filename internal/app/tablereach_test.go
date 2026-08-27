package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Tables the product writes but never reads.
//
// TestEveryRouteIsReachable asks this of routes because three releases in a row
// found a feature built completely, tested, documented — and structurally
// unreachable. The same shape lives one layer down: report_status_history is
// written by six code paths, carries its own index on (report_id, id), and is
// named in the README as the record of every status transition. Nothing selects
// from it. In Go, in the OpenAPI contract, and in every screen: nothing.
//
// A store nobody reads is either an operator's trail — in which case saying so
// once here is cheap — or a write that has quietly lost its reader. This makes
// the project choose, and catches the next one.

var (
	sqlWrite = regexp.MustCompile(`(?i)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+([a-z_][a-z0-9_]*)`)
	sqlRead  = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([a-z_][a-z0-9_]*)`)
	// DELETE FROM x is a write, and its FROM must not be counted as a read.
	deleteFrom = regexp.MustCompile(`(?i)\bDELETE\s+FROM\b`)
	// Except when it returns the row. `DELETE FROM x WHERE … RETURNING …` is how
	// a single-use record is consumed: it reads and invalidates in one
	// statement, and the OIDC login state is stored exactly that way. Counting
	// it as write-only would report the check on the sign-in flow as missing.
	deleteReturning = regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+([a-z_][a-z0-9_]*)\b[^;` + "`" + `]*?\bRETURNING\b`)
)

func TestEveryTableTheProductWritesIsAlsoRead(t *testing.T) {
	// Written and never read, and why that is correct.
	allowed := map[string]string{
		"report_status_history": "상태 전이의 기록. 화면은 최신 검토자와 반려 사유만 쓰고, 지나온 순서는 " +
			"운영자가 데이터베이스에서 직접 읽는 감사용 흔적이다. README 가 그렇게 적고 있고 " +
			"(report_id, id) 색인이 그 조회를 위해 있다.",
	}

	// Only names the migrations actually create. Without this the scan reads
	// English prose in comments as SQL — "UPDATE that", "DELETE FROM skip".
	real := map[string]bool{}
	migrations, err := os.ReadDir("migrations")
	if err != nil {
		t.Skipf("마이그레이션을 읽을 수 없습니다: %v", err)
	}
	createTable := regexp.MustCompile(`(?i)CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+([a-z_][a-z0-9_]*)`)
	for _, entry := range migrations {
		body, readErr := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if readErr != nil {
			continue
		}
		for _, match := range createTable.FindAllStringSubmatch(string(body), -1) {
			real[strings.ToLower(match[1])] = true
		}
	}
	if len(real) < 15 {
		t.Fatalf("마이그레이션에서 표를 %d개만 찾았습니다. 훑기가 깨졌습니다", len(real))
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Skipf("이 디렉터리를 읽을 수 없습니다: %v", err)
	}
	written, read := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(".", name))
		if readErr != nil {
			continue
		}
		text := string(body)
		for _, match := range sqlWrite.FindAllStringSubmatch(text, -1) {
			if name := strings.ToLower(match[1]); real[name] {
				written[name] = true
			}
		}
		// Blank the DELETE FROM keyword so its table is not read as a select.
		for _, match := range sqlRead.FindAllStringSubmatch(deleteFrom.ReplaceAllString(text, "DELETE  "), -1) {
			if name := strings.ToLower(match[1]); real[name] {
				read[name] = true
			}
		}
		for _, match := range deleteReturning.FindAllStringSubmatch(text, -1) {
			if name := strings.ToLower(match[1]); real[name] {
				read[name] = true
			}
		}
	}
	if len(written) < 10 {
		t.Fatalf("SQL 을 %d개 표에서만 찾았습니다. 훑기가 깨졌습니다", len(written))
	}

	// Migrations create tables; the schema itself is not a reader.
	orphans := []string{}
	for table := range written {
		if read[table] || table == "schema_migrations" {
			continue
		}
		orphans = append(orphans, table)
	}
	sort.Strings(orphans)
	for _, table := range orphans {
		if _, ok := allowed[table]; !ok {
			t.Errorf("%s 에 쓰기만 하고 읽는 곳이 없습니다.\n"+
				"  화면이나 API 가 그것을 보여 주게 하거나, 왜 읽지 않아도 되는지 이유와 함께 목록에 적으십시오.", table)
		}
	}
	// A reason that outlived its table is noise the next reader has to check.
	for table := range allowed {
		if !written[table] {
			t.Errorf("%s 는 이제 쓰이지도 않습니다. 목록에서 지우십시오", table)
		}
		if read[table] {
			t.Errorf("%s 를 이제 읽습니다. 목록에서 지우십시오", table)
		}
	}
}
