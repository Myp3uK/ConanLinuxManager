package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/example/conan-manager/internal/config"
	"github.com/example/conan-manager/internal/httpapi"
	"github.com/example/conan-manager/internal/panel"
	"github.com/example/conan-manager/internal/store"
	"github.com/example/conan-manager/internal/unitservice"
)

// exitRestart asks systemd to start the manager again; it is not 2, which
// RestartPreventExitStatus reserves for a failed shutdown.
const exitRestart = 75

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
	// Settings saved in the panel take the place of the environment file.
	stored, err := panel.NewStore(db.DB()).Values(context.Background())
	if err != nil {
		logger.Error("read panel settings", "error", err)
		os.Exit(1)
	}
	if len(stored) > 0 {
		cfg = config.LoadFrom(panel.Overlay(stored, os.Getenv))
		logger.Info("panel settings applied", "count", len(stored))
	}

	// A mistyped allowlist or half a TLS setup would leave the manager more
	// open than the operator meant; it does not start until they are fixed.
	if len(cfg.Problems) > 0 {
		for _, p := range cfg.Problems {
			logger.Error("invalid setting", "problem", p)
		}
		os.Exit(1)
	}

	handler, control := httpapi.NewManaged(db, logger, cfg)
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	control.Logger = logger
	var restartRequested atomic.Bool
	control.RestartManager = func() {
		restartRequested.Store(true)
		stop()
	}
	waitScheduler := control.StartScheduler(ctx)
	// Servers the operator left running come back after a reboot. Adoption of
	// still-running units has already happened, so nothing starts twice.
	go control.Reconcile(ctx)
	go control.ResumePending(ctx)
	go control.WatchRcon(ctx)
	go control.WatchStats(ctx)
	if cfg.UpdateCheckInterval > 0 {
		go control.WatchUpdates(ctx, cfg.UpdateCheckInterval)
	}

	serveDone := make(chan error, 1)
	logger.Info("conan manager started", "listen", cfg.ListenAddress, "database", cfg.DatabasePath,
		"https", cfg.TLSCert != "", "allowed_ips", len(cfg.AllowedIPs), "trusted_proxies", len(cfg.TrustedProxies))
	go func() {
		if cfg.TLSCert != "" {
			serveDone <- server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
			return
		}
		serveDone <- server.ListenAndServe()
	}()
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
	if restartRequested.Load() {
		// Restart=on-failure in conan-manager.service starts the manager again.
		_ = db.Close()
		os.Exit(exitRestart)
	}
	if serveFailed {
		_ = db.Close()
		os.Exit(1)
	}
}
