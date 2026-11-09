package main

import (
	"log/slog"
	"os"
)

// setupLogging makes every log line structured. In production it is one JSON object per line, easy
// to search and count with tools; on a developer's machine it is plain readable text. It also
// catches the plain log.Printf calls, which now come out in the same format.
func setupLogging() {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var handler slog.Handler = slog.NewTextHandler(os.Stdout, opts)
	if os.Getenv("APP_ENV") == "production" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}
