package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/example/conan-manager/internal/ramstate"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/sessionlock"
)

type Lifecycle struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type reservation struct {
	Lifecycle
	paths []string
	lease *sessionlock.Lease
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
			return cfg, fmt.Errorf("server %q lifecycle is %s: %s", owner, held.State, held.Error)
		}
		for _, path := range paths {
			for _, other := range held.paths {
				if pathsOverlap(path, other) {
					return cfg, fmt.Errorf("directory %q is reserved by server %q (%s)", path, owner, held.State)
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
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if held := c.active[id]; held != nil {
		held.State = state
		if err != nil {
			held.Error = err.Error()
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
		if err := ramstate.Pending(cfg); err != nil {
			return Lifecycle{State: "recovery_required", Error: err.Error()}
		}
	}
	return Lifecycle{State: "idle"}
}

func (c *Controller) Delete(ctx context.Context, id string) error {
	if _, err := c.acquire(ctx, id, "deleting"); err != nil {
		return err
	}
	defer c.release(id)
	return c.Repo.Delete(ctx, id)
}
