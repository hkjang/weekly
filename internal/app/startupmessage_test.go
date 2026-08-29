package app

import (
	"strings"
	"testing"
)

// The only errors in this package a person reads directly. Everything else
// becomes a Korean sentence in an HTTP response; these two land on a console,
// during an installation that will not start, in front of the operator.

// guards: loadEnvironment, bootstrapAdmin
func TestARefusedStartUpNamesWhatIsMissingAndWhatToDo(t *testing.T) {
	// The address is demanded before anything is opened, because nothing can
	// stand in for it.
	t.Setenv("WEEKLY_POSTGRES_DSN", "")
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN", "admin")
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD", "WeeklyVerify1234")
	_, err := loadEnvironment()
	if err == nil {
		t.Fatal("DSN 이 없는데 기동이 거부되지 않았습니다")
	}
	assertNamesOnly(t, "DSN", err, "WEEKLY_POSTGRES_DSN")

	// The bootstrap pair is demanded only where it is actually needed: a
	// database with no administrator in it.
	server := newTestServer(t)
	if _, err := server.app.db.Exec(server.ctx(), `DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []struct {
		name     string
		username string
		password string
		expect   string
	}{
		{"관리자 아이디", "", "WeeklyVerify1234", "WEEKLY_BOOTSTRAP_ADMIN"},
		{"관리자 비밀번호", "admin", "", "WEEKLY_BOOTSTRAP_ADMIN_PASSWORD"},
		{"둘 다", "", "", "WEEKLY_BOOTSTRAP_ADMIN, WEEKLY_BOOTSTRAP_ADMIN_PASSWORD"},
	} {
		err := server.app.bootstrapAdmin(server.ctx(), missing.username, missing.password)
		if err == nil {
			t.Fatalf("%s 가 없는데 기동이 거부되지 않았습니다", missing.name)
		}
		assertNamesOnly(t, missing.name, err, missing.expect)
		if !strings.Contains(err.Error(), "아무도 로그인할 수 없습니다") {
			t.Errorf("%s: 왜 필요한지 말하지 않습니다: %v", missing.name, err)
		}
	}
}

// It has to name the ones that are missing and no others. The names overlap —
// WEEKLY_BOOTSTRAP_ADMIN is a prefix of the password one — so the list is
// compared as a list rather than searched for.
func assertNamesOnly(t *testing.T, label string, err error, expect string) {
	t.Helper()
	named, _, found := strings.Cut(err.Error(), " 환경변수가")
	if !found {
		t.Fatalf("%s: 예상한 문장 모양이 아닙니다: %v", label, err)
	}
	if sentence := strings.LastIndex(named, ". "); sentence >= 0 {
		named = named[sentence+2:]
	}
	if named != expect {
		t.Errorf("%s: 없는 것은 %s 뿐인데 %q 라고 말합니다", label, expect, named)
	}
	// And what to do about it.
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("%s: 다음에 무엇을 할지 말하지 않습니다: %v", label, err)
	}
}

// guards: loadEnvironment
func TestAShortBootstrapPasswordSaysWhyAndThatItCanBeChanged(t *testing.T) {
	t.Setenv("WEEKLY_POSTGRES_DSN", "postgres://x/y")
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN", "admin")
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD", "short")

	_, err := loadEnvironment()
	if err == nil {
		t.Fatal("11자 이하 비밀번호로 기동이 허용됐습니다")
	}
	if !strings.Contains(err.Error(), "12") {
		t.Errorf("몇 자가 필요한지 말하지 않습니다: %v", err)
	}
	if !strings.Contains(err.Error(), "바꿀 수 있습니다") {
		t.Errorf("이 값이 영구적이지 않다는 것을 말하지 않습니다: %v", err)
	}

	// Exactly twelve is enough — the boundary the message names.
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD", "123456789012")
	if _, err := loadEnvironment(); err != nil {
		t.Errorf("정확히 12자가 거부됐습니다: %v", err)
	}

	// And an absent password is not a short one: it means this deployment
	// already has an administrator and has taken the secret back out.
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD", "")
	if _, err := loadEnvironment(); err != nil {
		t.Errorf("비밀번호를 지운 배포가 거부됐습니다: %v", err)
	}
}
