package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Myp3uK/ConanLinuxManager/internal/recoveryalert"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

// CheckConflicts refuses a server that would collide with another one on the
// host: the same ports, or install directories inside one another. Either
// fails only at start otherwise, and a shared directory means two games
// writing one world.
func (c *Controller) CheckConflicts(ctx context.Context, cfg server.Config) error {
	others, err := c.Repo.List(ctx)
	if err != nil {
		return err
	}
	install, err := canonicalPath(cfg.InstallDir)
	if err != nil {
		return err
	}
	for _, other := range others {
		if other.ID == cfg.ID {
			continue
		}
		if port, clash := portClash(cfg, other); clash {
			return server.ErrInvalidConfig(fmt.Sprintf("port %d is already used by server %q", port, other.ID))
		}
		otherInstall, err := canonicalPath(other.InstallDir)
		if err != nil {
			return err
		}
		if pathsOverlap(install, otherInstall) {
			return server.ErrInvalidConfig(fmt.Sprintf("install_dir overlaps the install directory of server %q", other.ID))
		}
	}
	return nil
}

// usedPorts are the UDP ports a server binds: the game port, the one after
// it, which the engine opens for its own traffic, and the Steam query port.
func usedPorts(cfg server.Config) []int {
	return []int{cfg.GamePort, cfg.GamePort + 1, cfg.QueryPort}
}

func portClash(a, b server.Config) (int, bool) {
	for _, p := range usedPorts(a) {
		for _, q := range usedPorts(b) {
			if p == q {
				return p, true
			}
		}
	}
	return 0, false
}

// DeleteOptions choose what goes with a server beyond its record.
type DeleteOptions struct {
	// Files removes the install directory: the game, its world and its logs.
	Files bool
	// Backups removes the manager's backups of the server.
	Backups bool
}

// Delete removes a stopped server. Its record, schedule and unit
// specification always go; the game files and backups stay unless asked for,
// since they hold the world and cannot be recreated.
func (c *Controller) Delete(ctx context.Context, id string, opts DeleteOptions) error {
	cfg, err := c.acquire(ctx, id, "deleting")
	if err != nil {
		return err
	}
	defer c.release(id)
	if opts.Files {
		if err := c.safeToRemove(ctx, cfg, cfg.InstallDir); err != nil {
			return err
		}
	}
	if err := c.Repo.Delete(ctx, id); err != nil {
		return err
	}
	c.dropRcon(id)
	if c.Units != nil {
		if err := os.Remove(c.Units.SpecPath(id)); err != nil && !os.IsNotExist(err) {
			c.Logger.Warn("remove unit specification", "server", id, "error", err)
		}
	}
	if err := recoveryalert.Clear(cfg); err != nil {
		c.Logger.Warn("clear recovery alert", "server", id, "error", err)
	}
	if opts.Backups {
		if err := os.RemoveAll(c.backupDir(cfg)); err != nil {
			return fmt.Errorf("server deleted, but its backups were not: %w", err)
		}
	}
	if opts.Files {
		if err := os.RemoveAll(cfg.InstallDir); err != nil {
			return fmt.Errorf("server deleted, but its files were not: %w", err)
		}
		// The usual layout is servers/<id>/game; the <id> directory left
		// empty goes too. os.Remove fails on a directory that is not empty.
		if parent := filepath.Dir(filepath.Clean(cfg.InstallDir)); filepath.Base(parent) == cfg.ID {
			_ = os.Remove(parent)
		}
	}
	return nil
}

// safeToRemove guards the one recursive delete the API can trigger: the
// directory must be deep enough not to be a system directory, and must not
// hold another server or the backups.
func (c *Controller) safeToRemove(ctx context.Context, cfg server.Config, dir string) error {
	path, err := canonicalPath(dir)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || len(strings.Split(strings.Trim(filepath.ToSlash(path), "/"), "/")) < 3 {
		return fmt.Errorf("refusing to delete %s: too close to the filesystem root", path)
	}
	others, err := c.Repo.List(ctx)
	if err != nil {
		return err
	}
	for _, other := range others {
		if other.ID == cfg.ID {
			continue
		}
		for _, p := range []string{other.InstallDir, other.SteamCMDDir} {
			if canonical, err := canonicalPath(p); err == nil && pathsOverlap(path, canonical) {
				return fmt.Errorf("refusing to delete %s: server %q uses %s", path, other.ID, p)
			}
		}
	}
	if steam, err := canonicalPath(cfg.SteamCMDDir); err == nil && pathsOverlap(path, steam) && path != steam {
		// SteamCMD inside the install directory is this server's own; one
		// that contains the install directory is shared and stays.
		if within(steam, path) {
			return fmt.Errorf("refusing to delete %s: it is inside the SteamCMD directory", path)
		}
	}
	if c.BackupDir != "" {
		if backups, err := canonicalPath(c.BackupDir); err == nil && pathsOverlap(path, backups) {
			return fmt.Errorf("refusing to delete %s: it overlaps the backup directory", path)
		}
	}
	return nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
