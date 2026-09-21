package controller

import (
	"context"

	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/watchdog"
)

// AdoptUnits reconnects to server units that kept running while the manager
// was down. It must run before Reconcile, or Reconcile would see those servers
// as stopped and try to start them a second time.
func (c *Controller) AdoptUnits(ctx context.Context) {
	if c.Units == nil {
		return
	}
	configs, err := c.Repo.List(ctx)
	if err != nil {
		c.Logger.Error("list servers for adoption", "error", err)
		return
	}
	for _, cfg := range configs {
		if c.backendFor(cfg) != backendUnit {
			continue
		}
		s, err := c.Units.Inspect(ctx, cfg.ID)
		if err != nil {
			// A unit that is not loaded simply was never started on this host.
			c.Logger.Debug("inspect server unit", "server", cfg.ID, "error", err)
			continue
		}
		if s.State != process.StateRunning && s.State != process.StateStopping {
			continue
		}
		if _, err := c.acquire(ctx, cfg.ID, "running"); err != nil {
			c.Logger.Error("adopt running server unit", "server", cfg.ID, "error", err)
			continue
		}
		c.setBackend(cfg.ID, backendUnit)
		if s.State == process.StateStopping {
			// A stop already under way still needs supervising, or a hung
			// save would wait for systemd's own last-resort timeout.
			c.lifecycleMu.Lock()
			c.beginStopping(cfg.ID)
			c.lifecycleMu.Unlock()
		}
		c.Logger.Info("adopted running server", "server", cfg.ID, "pid", s.PID)
		go c.monitor(cfg, watchdog.New(watchdog.Paths(cfg.InstallDir)), backendUnit)
	}
}

// Reconcile starts every server the operator last left running and that is not
// running now: after a host reboot, or after the manager itself was restarted
// while an in-process server was stopped with it. Failures are reported and
// left for the operator; nothing here retries in a loop.
func (c *Controller) Reconcile(ctx context.Context) {
	configs, err := c.Repo.List(ctx)
	if err != nil {
		c.Logger.Error("list servers for reconcile", "error", err)
		return
	}
	for _, cfg := range configs {
		if cfg.DesiredState != "running" {
			continue
		}
		c.lifecycleMu.Lock()
		_, active := c.active[cfg.ID]
		c.lifecycleMu.Unlock()
		if active {
			continue
		}
		c.Logger.Info("restoring server the operator left running", "server", cfg.ID)
		unlock := c.lockServer(cfg.ID)
		err := c.start(ctx, cfg.ID)
		unlock()
		if err != nil {
			c.Logger.Error("restore server", "server", cfg.ID, "error", err)
		}
	}
}
