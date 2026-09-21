package controller

import (
	"context"
	"fmt"

	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/server"
)

// A backend is how a server's process is run and watched.
const (
	// backendProcess runs the game as a child of the manager. It dies with the
	// manager, so it is kept for development hosts and RAM-disk servers.
	backendProcess = "process"
	// backendUnit runs the game as its own systemd unit, which outlives the
	// manager: upgrading the manager no longer disconnects every player.
	backendUnit = "unit"
	// backendLauncher is the root launcher that bind-mounts the RAM database.
	backendLauncher = "launcher"
)

func (c *Controller) backendFor(cfg server.Config) string {
	switch {
	case c.Launcher.Enabled(cfg.ID):
		return backendLauncher
	case c.Units != nil && !cfg.RamdiskEnabled:
		return backendUnit
	}
	return backendProcess
}

func (c *Controller) setBackend(id, backend string) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if held := c.active[id]; held != nil {
		held.backend = backend
	}
}

// backendOf reports the backend of a reserved server. The caller holds
// lifecycleMu.
func (c *Controller) backendOf(id string) string {
	if held := c.active[id]; held != nil && held.backend != "" {
		return held.backend
	}
	return backendProcess
}

// stopProcess asks the backend to stop gracefully. The caller holds lifecycleMu.
func (c *Controller) stopProcess(ctx context.Context, id string) error {
	switch c.backendOf(id) {
	case backendLauncher:
		return c.Launcher.Stop(ctx, id)
	case backendUnit:
		return c.Units.Stop(ctx, id)
	}
	return c.Processes.Stop(ctx, id)
}

// killProcess ends the server at once. The caller holds lifecycleMu.
func (c *Controller) killProcess(ctx context.Context, id string) error {
	switch c.backendOf(id) {
	case backendLauncher:
		// The launcher must unmount and persist after the game exits; killing
		// it would leave the RAM session for offline recovery.
		return fmt.Errorf("kill is not supported for launcher servers; use stop")
	case backendUnit:
		return c.Units.Kill(ctx, id)
	}
	return c.Processes.Kill(ctx, id)
}

func (c *Controller) waitProcess(ctx context.Context, id, backend string) error {
	switch backend {
	case backendLauncher:
		return c.Launcher.Wait(ctx, id)
	case backendUnit:
		return c.Units.Wait(ctx, id)
	}
	return c.Processes.Wait(ctx, id)
}

func (c *Controller) snapshotOf(backend, id string) process.Snapshot {
	switch backend {
	case backendLauncher:
		return c.Launcher.Snapshot(id)
	case backendUnit:
		return c.Units.Snapshot(id)
	}
	return c.Processes.Snapshot(id)
}

// Snapshot reports the process state of a server whether or not it is running.
func (c *Controller) Snapshot(id string) process.Snapshot {
	c.lifecycleMu.Lock()
	held := c.active[id] != nil
	backend := c.backendOf(id)
	c.lifecycleMu.Unlock()
	if !held {
		if cfg, err := c.Repo.Get(context.Background(), id); err == nil {
			backend = c.backendFor(cfg)
		}
	}
	return c.snapshotOf(backend, id)
}
