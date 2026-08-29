package app

import (
	"strings"
	"testing"
)

// The first administrator's password should not have to live in the
// environment forever.
//
// loadEnvironment demanded WEEKLY_BOOTSTRAP_ADMIN and its password on every
// boot, and bootstrapAdmin already does nothing once an administrator exists.
// So on every deployment past its first day the two were read, validated and
// ignored — while the operator kept the password in the Compose file, the
// Kubernetes manifest and whatever CI writes them. Measured: a container given
// only the DSN against a populated database refused to start.
//
// guards: loadEnvironment, bootstrapAdmin
func TestOnlyTheDatabaseAddressIsNeededToStart(t *testing.T) {
	t.Setenv("WEEKLY_POSTGRES_DSN", "postgres://user:pass@host:5432/weekly?sslmode=disable")
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN", "")
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD", "")
	t.Setenv("WEEKLY_ENCRYPTION_KEY", "")
	t.Setenv("WEEKLY_ALLOW_SECRET_RESET", "")

	env, err := loadEnvironment()
	if err != nil {
		t.Fatalf("주소만 주었는데 기동 설정을 거절합니다: %v", err)
	}
	if env.PostgresDSN == "" {
		t.Error("주소가 실리지 않았습니다")
	}

	// The address itself is still required: without it there is nothing to
	// connect to and no later check can stand in for it.
	t.Setenv("WEEKLY_POSTGRES_DSN", "")
	if _, err := loadEnvironment(); err == nil {
		t.Error("데이터베이스 주소가 없는데 기동 설정을 받아들입니다")
	} else if !strings.Contains(err.Error(), "WEEKLY_POSTGRES_DSN") {
		t.Errorf("어느 변수가 없는지 말하지 않습니다: %v", err)
	}

	// A password that is present but too short is still refused up front, so
	// the operator hears it before the database is even opened.
	t.Setenv("WEEKLY_POSTGRES_DSN", "postgres://user:pass@host:5432/weekly?sslmode=disable")
	t.Setenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD", "short")
	if _, err := loadEnvironment(); err == nil {
		t.Error("12자 미만 비밀번호를 받아들입니다")
	}
}

// And an empty database still refuses, because otherwise nobody can log in.
//
// guards: bootstrapAdmin
func TestAnEmptyDatabaseStillDemandsAFirstAdministrator(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.app.db.Exec(server.ctx(), `DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	err := server.app.bootstrapAdmin(server.ctx(), "", "")
	if err == nil {
		t.Fatal("관리자가 하나도 없는데 빈 이름과 빈 비밀번호를 받아들입니다")
	}
	for _, needed := range []string{"WEEKLY_BOOTSTRAP_ADMIN", "아무도 로그인할 수 없습니다"} {
		if !strings.Contains(err.Error(), needed) {
			t.Errorf("거절 문장이 %q 를 말하지 않습니다: %v", needed, err)
		}
	}
	// Given the pair, it creates the account rather than refusing.
	if err := server.app.bootstrapAdmin(server.ctx(), "firstadmin", "FirstAdminPassword1"); err != nil {
		t.Fatalf("첫 관리자를 만들지 못합니다: %v", err)
	}
}
