package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/ramstate"
	"github.com/Myp3uK/ConanLinuxManager/internal/recoveryalert"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
	"github.com/Myp3uK/ConanLinuxManager/internal/sessionlock"
)

type Lifecycle struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type reservation struct {
	Lifecycle
	paths      []string
	lease      *sessionlock.Lease
	backend    string
	supervised bool
	// forced is set when this run was ended by a kill rather than a stop.
	forced bool
}

// canonicalPath also resolves existing ancestors of not-yet-created paths.
// These reservations coordinate this controller, not external filesystem edits.
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	base := abs
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(base)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			if runtime.GOOS == "windows" {
				resolved = strings.ToLower(resolved)
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(base) == base {
			return "", err
		}
		// A dangling symlink must not be treated as a new directory.
		if info, statErr := os.Lstat(base); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("unresolved symlink: %s", base)
		}
		tail = append(tail, filepath.Base(base))
		base = filepath.Dir(base)
	}
}

func pathsOverlap(a, b string) bool {
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
}

// acquire reads the config under the same lock used by deletion, closing the
// read-config/delete/start race. A running server owns its lease through flush.
func (c *Controller) acquire(ctx context.Context, id, state string) (server.Config, error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closing {
		return server.Config{}, fmt.Errorf("manager is shutting down")
	}
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return cfg, err
	}
	if err := ramstate.Pending(cfg); err != nil {
		return cfg, err
	}
	// A world that failed its check must not be opened by the game or replaced
	// by an install until an operator has verified it again.
	if cfg.RecoveryReason != "" && (state == "starting" || state == "installing") {
		return cfg, fmt.Errorf("server %q needs recovery before it can start: %s", id, cfg.RecoveryReason)
	}
	paths := []string{cfg.InstallDir}
	if cfg.RamdiskEnabled {
		paths = append(paths, cfg.RamdiskPath)
	}
	if state == "installing" {
		paths = append(paths, cfg.SteamCMDDir)
	}
	for i, path := range paths {
		paths[i], err = canonicalPath(path)
		if err != nil {
			return cfg, err
		}
	}
	for owner, held := range c.active {
		if owner == id {
			if held.Error != "" {
				return cfg, fmt.Errorf("server %q is %s: %s", owner, held.State, held.Error)
			}
			return cfg, fmt.Errorf("server %q is %s", owner, held.State)
		}
		for _, path := range paths {
			for _, other := range held.paths {
				if pathsOverlap(path, other) {
					// The path itself is deliberately omitted: it is an absolute
					// server path, and the owning server and state are what the
					// caller needs in order to act.
					return cfg, fmt.Errorf("a directory of server %q is reserved by server %q (%s)", id, owner, held.State)
				}
			}
		}
	}
	lease, err := sessionlock.Acquire(paths...)
	if err != nil {
		return cfg, err
	}
	// Recheck after acquiring the cross-process lease.
	if err := ramstate.Pending(cfg); err != nil {
		lease.Close()
		return cfg, err
	}
	c.active[id] = &reservation{Lifecycle: Lifecycle{State: state}, paths: paths, lease: lease}
	return cfg, nil
}

func (c *Controller) transition(id, state string, err error) {
	cfg, cfgErr := c.Repo.Get(context.Background(), id)
	c.lifecycleMu.Lock()
	if held := c.active[id]; held != nil {
		held.State = state
		if err != nil {
			held.Error = err.Error()
		}
	}
	c.lifecycleMu.Unlock()
	if cfgErr != nil {
		c.Logger.Error("read server while recording lifecycle state", "server", id, "state", state, "error", cfgErr)
		return
	}
	// A failure here loses the operator's only out-of-band notice, so it is
	// reported rather than swallowed. The lifecycle state itself already holds.
	if state == "recovery_required" && err != nil {
		if alertErr := recoveryalert.Write(cfg, err); alertErr != nil {
			c.Logger.Error("write recovery alert", "server", id, "path", recoveryalert.Path(cfg), "error", alertErr)
		}
		c.emit(events.ServerRecovery, cfg, "Server needs offline recovery before it can start again", map[string]string{"error": err.Error()})
	} else if state == "running" || state == "idle" {
		if alertErr := recoveryalert.Clear(cfg); alertErr != nil {
			c.Logger.Error("clear recovery alert", "server", id, "path", recoveryalert.Path(cfg), "error", alertErr)
		}
	}
}

func (c *Controller) release(id string) {
	c.lifecycleMu.Lock()
	if held := c.active[id]; held != nil {
		held.lease.Close()
	}
	delete(c.active, id)
	c.lifecycleMu.Unlock()
}

func (c *Controller) Lifecycle(id string) Lifecycle {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if held := c.active[id]; held != nil {
		return held.Lifecycle
	}
	if cfg, err := c.Repo.Get(context.Background(), id); err == nil {
		if cfg.RecoveryReason != "" {
			return Lifecycle{State: "recovery_required", Error: cfg.RecoveryReason}
		}
		if err := ramstate.Pending(cfg); err != nil {
			if alertErr := recoveryalert.Write(cfg, err); alertErr != nil {
				c.Logger.Error("write recovery alert", "server", id, "path", recoveryalert.Path(cfg), "error", alertErr)
			}
			return Lifecycle{State: "recovery_required", Error: err.Error()}
		}
	}
	return Lifecycle{State: "idle"}
}

// managerOnly clears the settings that only the manager reads. Two configs
// equal after it differ in nothing the game or a running session depends on.
func managerOnly(cfg server.Config) server.Config {
	cfg.Name = ""
	cfg.RestartOnCrash = false
	cfg.Hooks = nil
	cfg.AutoUpdate = false
	cfg.Warnings = server.Warnings{}
	cfg.ZombieCheckSeconds = 0
	cfg.RestartMinUptimeSeconds = 0
	cfg.BackupOnStop = false
	cfg.DiscordMessages = server.DiscordMessages{}
	cfg.Messages = nil
	cfg.UpdatedAt = time.Time{}
	return cfg
}

// updateLive applies a change that touches only manager settings without the
// lifecycle reservation, so it works on a running server: the game never reads
// these, and the crash handler and hooks read them fresh from the database.
// ok is false when the change needs the reservation.
func (c *Controller) updateLive(ctx context.Context, id string, mutate func(*server.Config)) (server.Config, bool, error) {
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	current, err := c.Repo.Get(ctx, id)
	if err != nil {
		return server.Config{}, false, err
	}
	updated := current
	mutate(&updated)
	if !reflect.DeepEqual(managerOnly(current), managerOnly(updated)) {
		return server.Config{}, false, nil
	}
	if err := c.CheckHooks(updated); err != nil {
		return server.Config{}, true, err
	}
	if err := c.Repo.Update(ctx, updated); err != nil {
		return server.Config{}, true, err
	}
	fresh, err := c.Repo.Get(ctx, id)
	return fresh, true, err
}

// UpdateConfig changes a stored server under the same reservation that start
// and delete use, so a running server cannot have its settings moved underneath
// it. That matters beyond bookkeeping: the game only reads its INI files during
// startup, and a RAM session in flight owns the paths this config names.
func (c *Controller) UpdateConfig(ctx context.Context, id string, mutate func(*server.Config)) (server.Config, error) {
	if updated, ok, err := c.updateLive(ctx, id, mutate); ok || err != nil {
		return updated, err
	}
	current, err := c.acquire(ctx, id, "configuring")
	if err != nil {
		return server.Config{}, err
	}
	defer c.release(id)
	updated := current
	mutate(&updated)
	// These identify on-disk state rather than describe behaviour. Changing one
	// would orphan an install or a RAM session, so it is refused loudly instead
	// of being dropped quietly by the repository.
	if updated.ID != current.ID || updated.Engine != current.Engine ||
		updated.Runtime != current.Runtime || updated.InstallDir != current.InstallDir ||
		updated.SteamCMDDir != current.SteamCMDDir || updated.RamdiskPath != current.RamdiskPath {
		return server.Config{}, fmt.Errorf("id, engine, runtime, install_dir, steamcmd_dir and ramdisk_path cannot be changed")
	}
	if err := c.CheckHooks(updated); err != nil {
		return server.Config{}, err
	}
	if err := c.CheckConflicts(ctx, updated); err != nil {
		return server.Config{}, err
	}
	if err := c.Repo.Update(ctx, updated); err != nil {
		return server.Config{}, err
	}
	return c.Repo.Get(ctx, id)
}
