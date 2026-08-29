package app

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type environment struct {
	PostgresDSN       string
	BootstrapAdmin    string
	BootstrapPassword string
	EncryptionKey     string
	// AllowSecretReset lets an operator start the service after accepting that
	// the stored secrets can no longer be decrypted and must be re-entered.
	AllowSecretReset bool
}

func loadEnvironment() (environment, error) {
	env := environment{
		PostgresDSN:       strings.TrimSpace(os.Getenv("WEEKLY_POSTGRES_DSN")),
		BootstrapAdmin:    strings.TrimSpace(os.Getenv("WEEKLY_BOOTSTRAP_ADMIN")),
		BootstrapPassword: os.Getenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD"),
		EncryptionKey:     strings.TrimSpace(os.Getenv("WEEKLY_ENCRYPTION_KEY")),
		AllowSecretReset:  strings.EqualFold(strings.TrimSpace(os.Getenv("WEEKLY_ALLOW_SECRET_RESET")), "true"),
	}
	// These two are the only errors in this package a person reads directly:
	// everything else becomes a Korean sentence in an HTTP response, but a
	// deployment that will not start prints this on a console instead. It is
	// read at the worst moment of the installation, by the operator, in the
	// language the rest of the product speaks — and it should say what to do
	// next rather than only what is wrong.
	if env.PostgresDSN == "" {
		return environment{}, fmt.Errorf(
			"%s 환경변수가 없습니다. deploy/.env.example 을 복사해 채운 뒤 --env-file 로 넘기십시오",
			"WEEKLY_POSTGRES_DSN")
	}
	// The bootstrap pair is checked after the database is open, because whether
	// it is required depends on what is in there.
	//
	// It used to be demanded on every boot. bootstrapAdmin already does nothing
	// once an administrator exists, so on every deployment past its first day
	// the two variables were read, validated and ignored — while the operator
	// had to keep the first administrator's password in the environment, in the
	// Compose file, in the Kubernetes manifest and in whatever CI writes them.
	// A secret that is never used again is a secret that should not still be
	// there.
	if len(env.BootstrapPassword) > 0 && len(env.BootstrapPassword) < 12 {
		return environment{}, errors.New(
			"WEEKLY_BOOTSTRAP_ADMIN_PASSWORD 는 12자 이상이어야 합니다. " +
				"첫 관리자 계정의 비밀번호이며, 기동한 뒤 화면에서 바꿀 수 있습니다")
	}
	return env, nil
}

// missingRequired names the variables that are actually absent. Listing all
// three when one is missing sends the operator to check the two that are fine.
//
// Only consulted when the database has no administrator: see loadEnvironment.
func missingRequired(env environment) []string {
	missing := []string{}
	if env.PostgresDSN == "" {
		missing = append(missing, "WEEKLY_POSTGRES_DSN")
	}
	if env.BootstrapAdmin == "" {
		missing = append(missing, "WEEKLY_BOOTSTRAP_ADMIN")
	}
	if env.BootstrapPassword == "" {
		missing = append(missing, "WEEKLY_BOOTSTRAP_ADMIN_PASSWORD")
	}
	return missing
}
