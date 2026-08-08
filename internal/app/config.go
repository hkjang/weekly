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
}

func loadEnvironment() (environment, error) {
	env := environment{
		PostgresDSN:       strings.TrimSpace(os.Getenv("WEEKLY_POSTGRES_DSN")),
		BootstrapAdmin:    strings.TrimSpace(os.Getenv("WEEKLY_BOOTSTRAP_ADMIN")),
		BootstrapPassword: os.Getenv("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD"),
	}
	if env.PostgresDSN == "" || env.BootstrapAdmin == "" || env.BootstrapPassword == "" {
		return environment{}, errors.New("WEEKLY_POSTGRES_DSN, WEEKLY_BOOTSTRAP_ADMIN and WEEKLY_BOOTSTRAP_ADMIN_PASSWORD are required")
	}
	if len(env.BootstrapPassword) < 12 {
		return environment{}, errors.New("WEEKLY_BOOTSTRAP_ADMIN_PASSWORD must be at least 12 characters")
	}
	return env, nil
}
