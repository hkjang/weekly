package app

import (
	"strings"
	"testing"
)

// The only errors in this package a person reads directly. Everything else
// becomes a Korean sentence in an HTTP response; these two land on a console,
// during an installation that will not start, in front of the operator.

// guards: loadEnvironment
func TestARefusedStartUpNamesWhatIsMissingAndWhatToDo(t *testing.T) {
	for _, missing := range []struct {
		name  string
		unset string
		set   map[string]string
	}{
		{"DSN", "WEEKLY_POSTGRES_DSN", map[string]string{
			"WEEKLY_BOOTSTRAP_ADMIN": "admin", "WEEKLY_BOOTSTRAP_ADMIN_PASSWORD": "WeeklyVerify1234"}},
		{"관리자 아이디", "WEEKLY_BOOTSTRAP_ADMIN", map[string]string{
			"WEEKLY_POSTGRES_DSN": "postgres://x/y", "WEEKLY_BOOTSTRAP_ADMIN_PASSWORD": "WeeklyVerify1234"}},
		{"관리자 비밀번호", "WEEKLY_BOOTSTRAP_ADMIN_PASSWORD", map[string]string{
			"WEEKLY_POSTGRES_DSN": "postgres://x/y", "WEEKLY_BOOTSTRAP_ADMIN": "admin"}},
	} {
		t.Setenv(missing.unset, "")
		for key, value := range missing.set {
			t.Setenv(key, value)
		}
		_, err := loadEnvironment()
		if err == nil {
			t.Fatalf("%s 가 없는데 기동이 거부되지 않았습니다", missing.name)
		}
		// It has to name the one that is missing and no others. The names
		// overlap — WEEKLY_BOOTSTRAP_ADMIN is a prefix of the password one —
		// so the list is compared as a list rather than searched for.
		named, _, found := strings.Cut(err.Error(), " 환경변수가")
		if !found {
			t.Fatalf("%s: 예상한 문장 모양이 아닙니다: %v", missing.name, err)
		}
		if named != missing.unset {
			t.Errorf("%s: 없는 것은 %s 뿐인데 %q 라고 말합니다", missing.name, missing.unset, named)
		}
		// And what to do about it.
		if !strings.Contains(err.Error(), ".env") {
			t.Errorf("%s: 다음에 무엇을 할지 말하지 않습니다: %v", missing.name, err)
		}
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
}
