package app

import (
	"errors"
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
	if env.PostgresDSN == "" || env.BootstrapAdmin == "" || env.BootstrapPassword == "" {
		return environment{}, errors.New("WEEKLY_POSTGRES_DSN, WEEKLY_BOOTSTRAP_ADMIN and WEEKLY_BOOTSTRAP_ADMIN_PASSWORD are required")
	}
	if len(env.BootstrapPassword) < 12 {
		return environment{}, errors.New("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD must be at least 12 characters")
	}
	return env, nil
}
