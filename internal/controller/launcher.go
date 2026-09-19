package controller

import (
	"context"
	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/ramstate"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/watchdog"
)

func (c *Controller) stopProcess(ctx context.Context, id string) error {
	if c.Launcher.Enabled(id) {
		return c.Launcher.Stop(ctx, id)
	}
	return c.Processes.Stop(ctx, id)
}

// AdoptLaunchers reconnects to independent units after manager restart. On
// uncertainty reserve the server and poll; never infer stopped from a D-Bus error.
func (c *Controller) AdoptLaunchers(ctx context.Context) {
	if c.Launcher == nil {
		return
	}
	for id := range c.Launcher.IDs {
		cfg, err := c.Repo.Get(ctx, id)
		if err != nil {
			continue
		}
		s, inspectErr := c.Launcher.Inspect(ctx, id)
		if inspectErr == nil && s.State != process.StateRunning && s.State != process.StateStopping {
			continue
		}
		paths := []string{cfg.InstallDir, cfg.RamdiskPath}
		for i, p := range paths {
			if canonical, err := canonicalPath(p); err == nil {
				paths[i] = canonical
			}
		}
		c.lifecycleMu.Lock()
		c.active[id] = &reservation{Lifecycle: Lifecycle{State: "running"}, paths: paths}
		c.lifecycleMu.Unlock()
		go c.monitor(cfg, watchdog.New(watchdog.Paths(cfg.InstallDir)))
	}
}

func (c *Controller) waitProcess(ctx context.Context, id string) error {
	if c.Launcher.Enabled(id) {
		return c.Launcher.Wait(ctx, id)
	}
	return c.Processes.Wait(ctx, id)
}

func (c *Controller) Snapshot(id string) process.Snapshot {
	if c.Launcher.Enabled(id) {
		return c.Launcher.Snapshot(id)
	}
	return c.Processes.Snapshot(id)
}

func (c *Controller) launcherPersist(cfg server.Config) {
	if err := ramstate.Pending(cfg); err != nil {
		c.transition(cfg.ID, "recovery_required", err)
		return
	}
	c.release(cfg.ID)
}
