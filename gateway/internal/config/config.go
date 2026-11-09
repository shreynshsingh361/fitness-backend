// Package config reads settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port      string
	JWTSecret string // must be the same secret the auth service signs tokens with
	// JWTSecretPrevious is the secret that signed tokens until the last rotation. While it is set
	// the gateway still accepts tokens signed with it; remove it once access tokens (15 minutes)
	// signed with the old secret have all expired.
	JWTSecretPrevious string
	AuthURL           string
	WorkoutURL        string
	NutritionURL      string
	ActivityURL       string
	SocialURL         string
	CORSOrigins       string // comma-separated list of browser origins allowed to call the API
	// TrustedProxies: comma-separated IPs/CIDRs of the load balancer in front, if any.
	TrustedProxies []string
	// AuthAttemptsPerMinute: sign-in, sign-up and account deletion allowed per IP per minute.
	AuthAttemptsPerMinute int
}

func Load() (Config, error) {
	cfg := Config{
		Port:              getenv("PORT", "8080"),
		JWTSecret:         os.Getenv("JWT_SECRET"),
		JWTSecretPrevious: os.Getenv("JWT_SECRET_PREVIOUS"),
		AuthURL:           getenv("AUTH_URL", "http://localhost:8081"),
		WorkoutURL:        getenv("WORKOUT_URL", "http://localhost:8082"),
		NutritionURL:      getenv("NUTRITION_URL", "http://localhost:8083"),
		ActivityURL:       getenv("ACTIVITY_URL", "http://localhost:8084"),
		SocialURL:         getenv("SOCIAL_URL", "http://localhost:8085"),
		CORSOrigins:       corsOrigins(),
	}
	for _, p := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			cfg.TrustedProxies = append(cfg.TrustedProxies, p)
		}
	}
	if v := os.Getenv("AUTH_ATTEMPTS_PER_MINUTE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, errors.New("AUTH_ATTEMPTS_PER_MINUTE must be a whole number above 0")
		}
		cfg.AuthAttemptsPerMinute = n
	}
	if err := checkSecret(cfg.JWTSecret); err != nil {
		return cfg, err
	}
	if cfg.JWTSecretPrevious != "" {
		if err := checkSecret(cfg.JWTSecretPrevious); err != nil {
			return cfg, fmt.Errorf("JWT_SECRET_PREVIOUS: %w", err)
		}
		if cfg.JWTSecretPrevious == cfg.JWTSecret {
			return cfg, errors.New("JWT_SECRET_PREVIOUS must differ from JWT_SECRET")
		}
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

// corsOrigins returns the browser origins allowed to call the API (comma separated). CORS_ORIGINS
// wins when it is set. When it is not (or is empty, as docker compose passes an unset value):
// in production no origin is allowed, because the phone app is not a browser and needs none, and
// a real server must never trust a developer's localhost; anywhere else the local web app.
func corsOrigins() string {
	if v := os.Getenv("CORS_ORIGINS"); v != "" {
		return v
	}
	if os.Getenv("APP_ENV") == "production" {
		return ""
	}
	return "http://localhost:3000"
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
