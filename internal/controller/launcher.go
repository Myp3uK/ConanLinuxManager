package controller

import (
	"context"
	"github.com/Myp3uK/ConanLinuxManager/internal/process"
	"github.com/Myp3uK/ConanLinuxManager/internal/ramstate"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
	"github.com/Myp3uK/ConanLinuxManager/internal/watchdog"
)

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
		c.active[id] = &reservation{Lifecycle: Lifecycle{State: "running"}, paths: paths, backend: backendLauncher}
		c.lifecycleMu.Unlock()
		go c.monitor(cfg, watchdog.New(watchdog.Paths(cfg.InstallDir)), backendLauncher)
	}
}

func (c *Controller) launcherPersist(cfg server.Config) {
	if err := ramstate.Pending(cfg); err != nil {
		c.transition(cfg.ID, "recovery_required", err)
		return
	}
	c.release(cfg.ID)
}
