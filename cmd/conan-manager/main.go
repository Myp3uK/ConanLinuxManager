package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/conan-manager/internal/config"
	"github.com/example/conan-manager/internal/httpapi"
	"github.com/example/conan-manager/internal/store"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := config.Load()

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	handler, control := httpapi.NewManaged(db, logger)
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveDone := make(chan error, 1)
	logger.Info("conan manager started", "listen", cfg.ListenAddress, "database", cfg.DatabasePath)
	go func() { serveDone <- server.ListenAndServe() }()
	serveFailed := false
	select {
	case <-ctx.Done():
	case err := <-serveDone:
		logger.Error("http server stopped", "error", err)
		serveFailed = err != nil && err != http.ErrServerClosed
	}
	logger.Info("manager shutdown started")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	controlDone := make(chan error, 1)
	go func() { controlDone <- control.Shutdown(shutdownCtx) }()
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), 10*time.Second)
	if err := server.Shutdown(httpCtx); err != nil {
		logger.Error("http shutdown", "error", err)
	}
	cancelHTTP()
	if err := <-controlDone; err != nil {
		logger.Error("manager shutdown incomplete; inspect RAM data before restart", "error", err)
		// Do not auto-restart over a failed RAM persistence operation.
		_ = db.Close()
		os.Exit(2)
	}
	logger.Info("manager shutdown complete; server persistence finished")
	if serveFailed {
		_ = db.Close()
		os.Exit(1)
	}
}
