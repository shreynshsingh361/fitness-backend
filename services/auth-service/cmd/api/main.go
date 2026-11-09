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

	"fitness/auth-service/internal/api"
	"fitness/auth-service/internal/config"
	"fitness/auth-service/internal/database"
	"fitness/auth-service/internal/firebase"
	"fitness/auth-service/internal/store"
	"fitness/auth-service/internal/token"
)

func main() {
	setupLogging()
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	if cfg.LegacyPasswords {
		log.Println("WARNING: the old sign-up and login are on (LEGACY_PASSWORD_AUTH=true); nobody checks those emails")
	}
	var fb api.IDTokenVerifier
	if cfg.FirebaseProjectID != "" {
		fb = firebase.NewVerifier(cfg.FirebaseProjectID, firebase.GoogleKeys(&http.Client{Timeout: 5 * time.Second}))
		log.Printf("Firebase sign-in is on for project %s", cfg.FirebaseProjectID)
	} else {
		log.Println("Firebase sign-in is off (FIREBASE_PROJECT_ID is not set)")
	}

	router := api.NewRouter(store.New(pool), token.NewMaker(cfg.JWTSecret, 15*time.Minute), fb, cfg.LegacyPasswords)
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second, // a client that sends a byte a minute cannot hold a connection
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	go func() {
		log.Printf("auth-service listening on :%s", cfg.Port)
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
