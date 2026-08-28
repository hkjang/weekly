package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Columns the product writes but never reads.
//
// TestEveryTableTheProductWritesIsAlsoRead asks this of tables, and it was
// built after report_status_history turned out to be written by six paths and
// read by none. The same shape then appeared one layer finer, where a table
// check cannot see it: weekly_reports.reviewed_by was set on every approve and
// reject, cleared on every resubmit, and read by nothing — while the editor
// told a writer whose reason had gone missing to "검토자에게 직접 확인해
// 주세요" without ever naming them, and an approved report showed a timestamp
// with nobody's name on it. weekly_reports is obviously read, so the table
// check passed the whole time.
//
// The scan is by column name across the product, not per table: a name read
// anywhere counts as read everywhere. That under-reports — a column read for
// one table and write-only for another goes unnoticed — and never invents a
// finding, which is the trade a check nobody wants to argue with has to make.
var (
	createTableBody = regexp.MustCompile(`(?is)CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+[a-z_][a-z0-9_]*\s*\((.*?)\n\s*\)`)
	addColumn       = regexp.MustCompile(`(?i)ADD\s+COLUMN(?:\s+IF\s+NOT\s+EXISTS)?\s+([a-z_][a-z0-9_]*)`)
	columnHead      = regexp.MustCompile(`^\s*([a-z_][a-z0-9_]*)\s+[a-z]`)
	// Where a column name means "I am writing this", and nothing else: the
	// column list of an INSERT, and the left side of an assignment inside a SET
	// clause. The SET clause has to be cut out first — `WHERE token_hash=$1`
	// has the same shape as `SET token_hash=$1` and is the opposite thing, and
	// reading every `x=$1` as a write reported the session and OIDC lookups as
	// columns nobody reads.
	insertColumns = regexp.MustCompile(`(?is)INSERT\s+INTO\s+[a-z_][a-z0-9_]*\s*\(([^)]*)\)`)
	setClause     = regexp.MustCompile("(?is)\\bSET\\s+(.*?)(?:\\bWHERE\\b|\\bRETURNING\\b|`|;)")
	setAssignment = regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*)\s*=`)
)

func TestEveryColumnTheProductWritesIsAlsoRead(t *testing.T) {
	// Written and never read, and why that is correct.
	allowed := map[string]string{
		"body_hash": "Confluence 본문의 해시. 바뀌지 않은 문서를 다시 분석하지 않으려던 값인데, " +
			"그 판단은 Confluence 자신이 매기는 page_version 이 이미 더 정확하게 하고 있다 " +
			"(previousVersion != page.Version). 남은 잔재이며 사용자에게 아무것도 약속하지 않는다.",
		"from_status": "report_status_history 의 내용. 그 표가 왜 읽히지 않아도 되는지는 " +
			"TestEveryTableTheProductWritesIsAlsoRead 의 목록에 적혀 있다.",
		"to_status": "위와 같다.",
		"raw_text": "Import 가 PPTX 에서 뽑아낸 원문. 어떤 파일이 왜 그렇게 해석됐는지를 " +
			"나중에 따지기 위한 재료이고, import.retention_days 가 보관 기간을 정한다. " +
			"운영자가 데이터베이스에서 읽는다.",
		"ai_response": "같은 재료의 다른 쪽. 모델이 실제로 무엇을 돌려줬는지이며, " +
			"해석이 이상할 때 원문과 나란히 놓고 보는 값이다.",
		"last_error": "Confluence 페이지 단위의 마지막 오류. 실제로 들어가는 값은 'HTTP 404' " +
			"하나뿐이고, 그때 같은 문장이 status='DELETED' 로도 기록된다. status 는 읽힌다 — " +
			"이 열은 이미 전달되고 있는 사실의 사본이다.",
		"last_synced_at": "페이지 하나가 마지막으로 수집된 시각. 화면은 수집 전체의 마지막 " +
			"성공 시각을 보여 주고, 페이지별 시각은 한 문서가 왜 안 보이는지 따질 때 " +
			"운영자가 직접 읽는 값이다.",
		"source_ref": "이 보고서를 만든 구체적인 출처. 복제면 원본 보고서, Import 면 파일, " +
			"Confluence 면 초안이다. 화면은 sourceType 으로 '무엇에서 왔는지'를 말하고, " +
			"'그중 어느 것인지'는 운영자가 추적할 때 쓴다.",
		"created_by": "업무 의존 관계를 선언한 사람. 목록은 관계와 메모를 보여 주고, " +
			"누가 적었는지는 조회 범위가 이미 그 사람의 조직으로 좁혀 준다.",
		"user_agent": "로그인한 브라우저. 세션 목록 화면이 없으므로 지금은 " +
			"'무엇으로 접속했는가'를 뒤에서 따질 때만 쓰인다.",
		// confluence_pages 는 Confluence 의 지역 사본이다. 수집은 API 응답을
		// 그대로 들고 일하고(page.CreatorUsername 을 직접 읽는다), 이 표는
		// 무엇을 언제 봤는지의 기록이다. 사본을 도로 읽는 경로가 없는 것이
		// 이 설계의 결과다.
		"creator_username":       "Confluence 지역 사본. 수집은 API 응답의 값을 직접 쓴다.",
		"last_modifier_username": "위와 같다.",
		"created_at_source":      "위와 같다. 문서가 Confluence 에서 만들어진 시각.",
		"title_hash":             "제목이 바뀌었는지 보려던 값. 그 판단도 page_version 이 한다.",
		"file_name":              "PPTX 템플릿의 원래 파일 이름. 화면은 조직과 기본 여부로 고른다.",
		"uploaded_by":            "그 템플릿을 올린 사람. 관리 화면은 조직 단위로만 다룬다.",
		"updated_by":             "결정 기록을 마지막으로 고친 사람. 화면은 기록한 사람과 결정한 사람을 보여 준다.",
		"issue_text":             "장애물이 끝났다고 적힌 그 주의 이슈 문장. 화면은 업무 제목으로 그것을 가리킨다.",
	}

	// Column names the migrations actually create. Without this the scan reads
	// Go identifiers and prose as SQL.
	real := map[string]bool{}
	migrations, err := os.ReadDir("migrations")
	if err != nil {
		t.Skipf("마이그레이션을 읽을 수 없습니다: %v", err)
	}
	for _, entry := range migrations {
		body, readErr := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if readErr != nil {
			continue
		}
		text := string(body)
		for _, table := range createTableBody.FindAllStringSubmatch(text, -1) {
			for _, line := range strings.Split(table[1], "\n") {
				if match := columnHead.FindStringSubmatch(line); match != nil {
					name := strings.ToLower(match[1])
					switch name {
					case "primary", "unique", "foreign", "check", "constraint", "references":
					default:
						real[name] = true
					}
				}
			}
		}
		for _, match := range addColumn.FindAllStringSubmatch(text, -1) {
			real[strings.ToLower(match[1])] = true
		}
	}
	if len(real) < 60 {
		t.Fatalf("마이그레이션에서 열을 %d개만 찾았습니다. 훑기가 깨졌습니다", len(real))
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Skipf("이 디렉터리를 읽을 수 없습니다: %v", err)
	}
	written, elsewhere := map[string]bool{}, map[string]bool{}
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
		// Blank the two places a name can only mean a write, then anything left
		// is the column being used for something — projected, filtered, joined,
		// ordered by, returned.
		remaining := text
		for _, match := range insertColumns.FindAllStringSubmatch(text, -1) {
			for _, column := range strings.Split(match[1], ",") {
				column = strings.ToLower(strings.TrimSpace(column))
				if real[column] {
					written[column] = true
				}
			}
			remaining = strings.Replace(remaining, match[0], "INSERT INTO x ()", 1)
		}
		for _, clause := range setClause.FindAllStringSubmatch(remaining, -1) {
			for _, match := range setAssignment.FindAllStringSubmatch(clause[1], -1) {
				if column := strings.ToLower(match[1]); real[column] {
					written[column] = true
				}
			}
			// A column that appears on both sides of its own assignment —
			// `reviewed_by = CASE WHEN $4 THEN NULL ELSE reviewed_by END`,
			// `version = version + 1` — is not being read by anybody. The value
			// is only surviving the write. Blanking just the left side let that
			// count as a reader, and the check then passed on the very column
			// it was written for. Every occurrence of an assigned name goes.
			blanked := clause[1]
			for _, match := range setAssignment.FindAllStringSubmatch(clause[1], -1) {
				column := strings.ToLower(match[1])
				if !real[column] {
					continue
				}
				blanked = regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(column)+`\b`).ReplaceAllString(blanked, " ")
			}
			remaining = strings.Replace(remaining, clause[1], blanked, 1)
		}
		for column := range real {
			if elsewhere[column] {
				continue
			}
			// Word bounded: user_id must not be found inside report_user_id.
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(column) + `\b`).MatchString(remaining) {
				elsewhere[column] = true
			}
		}
	}
	if len(written) < 40 {
		t.Fatalf("SQL 에서 쓰이는 열을 %d개만 찾았습니다. 훑기가 깨졌습니다", len(written))
	}

	orphans := []string{}
	for column := range written {
		switch column {
		case "id", "created_at", "updated_at", "version":
			// Bookkeeping every table carries; read through row scans that name
			// the table, not the column.
			continue
		}
		if elsewhere[column] {
			continue
		}
		orphans = append(orphans, column)
	}
	sort.Strings(orphans)
	for _, column := range orphans {
		if _, ok := allowed[column]; !ok {
			t.Errorf("%s 열에 쓰기만 하고 어디서도 읽지 않습니다.\n"+
				"  화면이나 API 가 그것을 보여 주게 하거나, 왜 읽지 않아도 되는지 이유와 함께 목록에 적으십시오.", column)
		}
	}
	for column := range allowed {
		if !written[column] {
			t.Errorf("%s 열은 이제 쓰이지도 않습니다. 목록에서 지우십시오", column)
		}
		if elsewhere[column] {
			t.Errorf("%s 열을 이제 읽습니다. 목록에서 지우십시오", column)
		}
	}
}
