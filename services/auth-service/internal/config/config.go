// Package config reads settings from environment variables.
// Every service in this project is configured the same way, so the same
// code can run on your laptop, in Docker, or in the cloud.
package config

import (
	"errors"
	"os"
)

type Config struct {
	Port        string
	DatabaseURL string
	JWTSecret   string
	// FirebaseProjectID turns on POST /auth/firebase. Empty = Firebase sign-in is off.
	FirebaseProjectID string
	// LegacyPasswords (LEGACY_PASSWORD_AUTH=true) keeps the old sign-up and login, where
	// nobody checks the email. For local development and the smoke tests only.
	LegacyPasswords bool
}

func Load() (Config, error) {
	cfg := Config{
		Port:              getenv("PORT", "8081"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		JWTSecret:         os.Getenv("JWT_SECRET"),
		FirebaseProjectID: os.Getenv("FIREBASE_PROJECT_ID"),
		LegacyPasswords:   os.Getenv("LEGACY_PASSWORD_AUTH") == "true",
	}
	if cfg.LegacyPasswords && os.Getenv("APP_ENV") == "production" {
		return cfg, errors.New("LEGACY_PASSWORD_AUTH must not be on in production: nobody checks those emails")
	}
	if cfg.DatabaseURL == "" {
		return cfg, errors.New("DATABASE_URL is required")
	}
	if err := checkSecret(cfg.JWTSecret); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// devSecret is the JWT secret docker-compose falls back to. It is in this repo for
// anyone to read, so a server on the internet must never use it.
const devSecret = "dev-only-secret-change-me-before-deploying"

// checkSecret refuses the well-known development secret, and short secrets, when
// APP_ENV=production.
func checkSecret(secret string) error {
	if len(secret) < 16 {
		return errors.New("JWT_SECRET is required and must be at least 16 characters")
	}
	if os.Getenv("APP_ENV") == "production" && (secret == devSecret || len(secret) < 32) {
		return errors.New("JWT_SECRET: in production use your own random secret of at least 32 characters (openssl rand -hex 32)")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
