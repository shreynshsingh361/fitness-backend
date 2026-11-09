package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"fitness/gateway/internal/config"
	"fitness/gateway/internal/gateway"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	handler, err := gateway.New(gateway.Config{
		JWTSecret:    cfg.JWTSecret,
		AuthURL:      cfg.AuthURL,
		WorkoutURL:   cfg.WorkoutURL,
		NutritionURL: cfg.NutritionURL,
		ActivityURL:  cfg.ActivityURL,
		SocialURL:    cfg.SocialURL,
		CORSOrigins:  cfg.CORSOrigins,

		TrustedProxies:        cfg.TrustedProxies,
		AuthAttemptsPerMinute: cfg.AuthAttemptsPerMinute,
	})
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("gateway listening on :%s (auth=%s workout=%s nutrition=%s)",
			cfg.Port, cfg.AuthURL, cfg.WorkoutURL, cfg.NutritionURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done() // wait for Ctrl+C or `docker stop`
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
