package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/example/conan-manager/internal/dbcheck"
	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/ramstate"
	"github.com/example/conan-manager/internal/recoveryalert"
	"github.com/example/conan-manager/internal/server"
)

// DefaultStopTimeout is how long a server may take to stop after SIGTERM.
// The UE5 server was measured taking about two and a half minutes on a small
// world, most of it a silent wait before the final save.
const DefaultStopTimeout = 5 * time.Minute

// superviseStop kills a server that is still stopping when the stop timeout
// runs out. The database is checked after the exit either way; see
// verifyAfterUncleanExit.
func (c *Controller) superviseStop(id string) {
	timeout := c.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if c.Lifecycle(id).State != "stopping" {
				return
			}
		case <-deadline.C:
			c.forceKill(id, fmt.Sprintf("did not stop within %s", timeout))
			return
		}
	}
}

// forceKill ends a stopping server and says why. The stop was requested, so
// the exit that follows is still reported as a stop, not as a crash.
func (c *Controller) forceKill(id, reason string) {
	c.lifecycleMu.Lock()
	held := c.active[id]
	if held == nil || held.State != "stopping" {
		c.lifecycleMu.Unlock()
		return
	}
	// Set before the kill: whatever the kill call reports, if the process ends
	// from here on, its shutdown did not complete and the world gets checked.
	held.forced = true
	err := c.killProcess(context.Background(), id)
	h := c.health[id]
	h.Reason, h.DetectedAt = "stop_timeout: "+reason, time.Now().UTC()
	if err != nil {
		h.Error = err.Error()
	}
	c.health[id] = h
	c.lifecycleMu.Unlock()
	details := map[string]string{"reason": reason}
	if err != nil {
		details["error"] = err.Error()
	}
	if cfg, getErr := c.Repo.Get(context.Background(), id); getErr == nil {
		c.emit(events.ServerKilled, cfg, "Server was killed: "+reason, details)
	}
	c.Logger.Warn("killed stopping server", "server", id, "reason", reason, "error", err)
}

// beginStopping marks a reserved server as stopping and starts the stop timer.
// The caller holds lifecycleMu.
func (c *Controller) beginStopping(id string) {
	held := c.active[id]
	if held == nil {
		return
	}
	held.State = "stopping"
	// The launcher persists the RAM database after the game exits and must
	// not be killed half-way; its own unit timeout governs it.
	if held.supervised || held.backend == backendLauncher {
		return
	}
	held.supervised = true
	go c.superviseStop(id)
}

// wasForced reports whether the server's current run was ended by a kill.
func (c *Controller) wasForced(id string) bool {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	held := c.active[id]
	return held != nil && held.forced
}

// verifyAfterUncleanExit checks the world after a kill or a crash, while the
// server is still reserved so nothing can start it in the meantime. A world
// that fails, or that could not be checked, is blocked from starting until an
// operator verifies it again; the block is stored, so it survives a manager
// restart.
func (c *Controller) verifyAfterUncleanExit(cfg server.Config, why string) error {
	c.transition(cfg.ID, "verifying", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	started := time.Now()
	err := dbcheck.Verify(ctx, ramstate.LayoutFor(cfg).GameDatabase)
	took := time.Since(started).Round(time.Millisecond).String()
	if errors.Is(err, dbcheck.ErrNoDatabase) {
		return nil
	}
	if err == nil {
		c.emit(events.DatabaseVerified, cfg, "World database checked after "+why+": intact", map[string]string{"took": took})
		return nil
	}
	reason := fmt.Sprintf("database check after %s: %v", why, err)
	if setErr := c.Repo.SetRecovery(context.Background(), cfg.ID, reason); setErr != nil {
		c.Logger.Error("record recovery reason", "server", cfg.ID, "error", setErr)
	}
	if alertErr := recoveryalert.Write(cfg, errors.New(reason)); alertErr != nil {
		c.Logger.Error("write recovery alert", "server", cfg.ID, "error", alertErr)
	}
	var corrupt *dbcheck.CorruptError
	message := "World database could not be checked; the server will not start until it is"
	if errors.As(err, &corrupt) {
		message = "World database is damaged; the server will not start until it is restored"
	}
	c.emit(events.DatabaseCorrupt, cfg, message, map[string]string{"error": err.Error(), "took": took})
	return err
}

// Verify checks a stopped server's database on request and, if it is intact,
// lifts a block left by an earlier failed check. This is how an operator
// clears a server after restoring a backup.
func (c *Controller) Verify(ctx context.Context, id string) error {
	cfg, err := c.acquire(ctx, id, "verifying")
	if err != nil {
		return err
	}
	defer c.release(id)
	err = dbcheck.Verify(ctx, ramstate.LayoutFor(cfg).GameDatabase)
	if err != nil && !errors.Is(err, dbcheck.ErrNoDatabase) {
		return err
	}
	if cfg.RecoveryReason != "" {
		if err := c.Repo.ClearRecovery(ctx, id); err != nil {
			return err
		}
		if err := recoveryalert.Clear(cfg); err != nil {
			c.Logger.Warn("clear recovery alert", "server", id, "error", err)
		}
	}
	c.emit(events.DatabaseVerified, cfg, "World database checked on request: intact", nil)
	return nil
}
