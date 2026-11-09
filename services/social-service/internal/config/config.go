// Package config reads settings from environment variables.
package config

import (
	"errors"
	"os"
)

type Config struct {
	Port        string
	DatabaseURL string
	ActivityURL string // where to ask for step totals
	AuthURL     string // where to ask for people's names
}

func Load() (Config, error) {
	cfg := Config{
		Port:        getenv("PORT", "8085"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		ActivityURL: getenv("ACTIVITY_URL", "http://localhost:8084"),
		AuthURL:     getenv("AUTH_URL", "http://localhost:8081"),
	}
	if cfg.DatabaseURL == "" {
		return cfg, errors.New("DATABASE_URL is required")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
