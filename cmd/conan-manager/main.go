package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/conan-manager/internal/config"
	"github.com/example/conan-manager/internal/httpapi"
	"github.com/example/conan-manager/internal/store"
	"github.com/example/conan-manager/internal/unitservice"
)

func main() {
	// The conan-server@ unit runs this binary to exec the game. It must not load
	// the manager's environment: that holds the admin password and secret key.
	if len(os.Args) == 3 && os.Args[1] == "run-server" {
		if err := unitservice.RunServer(os.Args[2], os.Getenv("CONAN_MANAGER_UNIT_DIR")); err != nil {
			fmt.Fprintln(os.Stderr, "run-server:", err)
			os.Exit(1)
		}
		return
	}
	// The rcon client needs nothing of the manager either.
	if len(os.Args) >= 2 && os.Args[1] == "rcon" {
		os.Exit(runRcon(os.Args[2:], os.Stdout, os.Stderr))
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := config.Load()

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	handler, control := httpapi.NewManaged(db, logger, cfg)
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
	control.Logger = logger
	waitScheduler := control.StartScheduler(ctx)
	// Servers the operator left running come back after a reboot. Adoption of
	// still-running units has already happened, so nothing starts twice.
	go control.Reconcile(ctx)
	if cfg.UpdateCheckInterval > 0 {
		go control.WatchUpdates(ctx, cfg.UpdateCheckInterval)
	}

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

	// Stop accepting requests first, so nothing new is admitted while the
	// scheduler drains.
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), 10*time.Second)
	if err := server.Shutdown(httpCtx); err != nil {
		logger.Error("http shutdown", "error", err)
	}
	cancelHTTP()

	// A scheduled action already in flight holds its own reservation, so let it
	// finish before the shutdown sweep decides what still needs stopping.
	stop()
	waitScheduler()
	logger.Info("scheduler drained")

	controlDone := make(chan error, 1)
	go func() { controlDone <- control.Shutdown(shutdownCtx) }()
	if err := <-controlDone; err != nil {
		logger.Error("manager shutdown incomplete; inspect RAM data before restart", "error", err)
		// Do not auto-restart over a failed RAM persistence operation.
		_ = db.Close()
		os.Exit(2)
	}
	if !control.WaitForHooks(2 * time.Minute) {
		logger.Warn("background hooks still running at exit")
	}
	logger.Info("manager shutdown complete; server persistence finished")
	if serveFailed {
		_ = db.Close()
		os.Exit(1)
	}
}
