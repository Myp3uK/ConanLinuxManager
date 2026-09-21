package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/hooks"
	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/server"
)

func (c *Controller) emit(t events.Type, cfg server.Config, message string, details map[string]string) {
	c.Events.Publish(events.Event{Type: t, ServerID: cfg.ID, ServerName: cfg.Name, Message: message, Details: details})
}

// afterExit decides what an exit means once the process is gone and its data
// persisted. An exit while the lifecycle still said "running" was not asked
// for by anyone, which is the definition of a crash here: a clean exit code
// does not make an unrequested exit less of an outage.
func (c *Controller) afterExit(cfg server.Config, crashed bool, exit *process.Exit) {
	// cfg was read at start; restart_on_crash and hooks may have been changed
	// since, while the server ran.
	if fresh, err := c.Repo.Get(context.Background(), cfg.ID); err == nil {
		cfg = fresh
	}
	details := map[string]string{}
	if exit != nil {
		details["exit"] = exit.String()
	}
	if c.Lifecycle(cfg.ID).State != "idle" {
		// Persistence failed and transition already reported recovery_required.
		// Restarting over an unpersisted world would be the worst option.
		return
	}
	c.lifecycleMu.Lock()
	h := c.health[cfg.ID]
	h.LastExit = exit
	c.health[cfg.ID] = h
	c.lifecycleMu.Unlock()
	if crashed {
		c.handleCrash(cfg, exit, details)
		return
	}
	if text := strings.TrimSpace(cfg.DiscordMessages.Stopped); text != "" {
		details["announce"] = text
	}
	c.emit(events.ServerStopped, cfg, "Server stopped", details)
	_ = c.runHooks(context.Background(), hooks.Async, "post_stop", cfg, exitEnv(exit))
	if cfg.BackupOnStop && c.beginBackup(cfg.ID) {
		// The world is at rest now, which makes this the cleanest copy there
		// is; a failure is reported by takeBackup itself.
		_, _ = c.takeBackup(context.Background(), cfg, ReasonOnStop)
		c.endBackup(cfg.ID)
	}
	c.restartPending(cfg.ID)
}

func (c *Controller) crashPolicy() (int, time.Duration, time.Duration) {
	limit, window, delay := c.CrashRestartLimit, c.CrashRestartWindow, c.CrashRestartDelay
	if limit <= 0 {
		limit = 3
	}
	if window <= 0 {
		window = 30 * time.Minute
	}
	if delay <= 0 {
		delay = 15 * time.Second
	}
	return limit, window, delay
}

// handleCrash restarts a crashed server a bounded number of times. A server
// that keeps crashing is left down: restarting it forever would hide the
// problem and hammer the disk with startup writes.
func (c *Controller) handleCrash(cfg server.Config, exit *process.Exit, details map[string]string) {
	limit, window, delay := c.crashPolicy()
	now := time.Now().UTC()
	c.lifecycleMu.Lock()
	h := c.health[cfg.ID]
	recent := make([]time.Time, 0, len(h.crashes)+1)
	for _, at := range h.crashes {
		if now.Sub(at) < window {
			recent = append(recent, at)
		}
	}
	h.crashes = append(recent, now)
	h.RecentCrashes = len(h.crashes)
	attempt := len(h.crashes)
	closing := c.closing
	retry := cfg.RestartOnCrash && !closing && attempt <= limit
	wait := delay * time.Duration(attempt)
	h.RestartPending = retry
	h.RestartAt = time.Time{}
	if retry {
		h.RestartAt = now.Add(wait)
	}
	c.health[cfg.ID] = h
	c.lifecycleMu.Unlock()

	details["attempt"] = fmt.Sprintf("%d/%d", attempt, limit)
	c.emit(events.ServerCrashed, cfg, "Server exited without being asked to", details)
	crashEnv := exitEnv(exit)
	crashEnv["CONAN_CRASH_ATTEMPT"] = fmt.Sprintf("%d", attempt)
	crashEnv["CONAN_WILL_RESTART"] = fmt.Sprintf("%t", retry)
	_ = c.runHooks(context.Background(), hooks.Async, "on_crash", cfg, crashEnv)
	if closing {
		return
	}
	if !retry {
		message := "Automatic restart is disabled for this server"
		if cfg.RestartOnCrash {
			message = fmt.Sprintf("Server crashed %d times within %s; it will not be restarted automatically", attempt, window)
		}
		c.emit(events.ServerRestartAbandoned, cfg, message, details)
		// Nobody wants this server running right now, so a reboot must not
		// bring it back either until an operator starts it.
		if err := c.Repo.SetDesiredState(context.Background(), cfg.ID, "stopped"); err != nil {
			c.Logger.Error("record desired state after crash", "server", cfg.ID, "error", err)
		}
		return
	}
	c.emit(events.ServerRestarting, cfg, fmt.Sprintf("Restarting in %s", wait), details)
	time.AfterFunc(wait, func() { c.restartPending(cfg.ID) })
}

// restartPending starts a server that the watchdog or the crash policy marked
// for restart. An operator stop in the meantime clears the mark, and wins.
func (c *Controller) restartPending(id string) {
	defer c.lockServer(id)()
	c.lifecycleMu.Lock()
	h := c.health[id]
	if c.closing || !h.RestartPending {
		c.lifecycleMu.Unlock()
		return
	}
	h.RestartPending = false
	h.RestartAt = time.Time{}
	h.Restarts++
	c.health[id] = h
	c.lifecycleMu.Unlock()
	if err := c.start(context.Background(), id); err != nil {
		c.lifecycleMu.Lock()
		h = c.health[id]
		h.Error = err.Error()
		c.health[id] = h
		c.lifecycleMu.Unlock()
		c.Logger.Error("automatic restart failed", "server", id, "error", err)
	}
}

// runHooks runs the scripts configured for event, if hooks are enabled.
func (c *Controller) runHooks(ctx context.Context, mode hooks.Mode, event string, cfg server.Config, extra map[string]string) error {
	if c.Hooks == nil {
		return nil
	}
	// A hook reaches the game with `conan-manager rcon -p $CONAN_RCON_PORT`;
	// the client reads the password from CONAN_RCON_PASSWORD. The scripts are
	// root's and could read Game.ini anyway.
	if settings, err := RconSettings(cfg); err == nil && settings.Usable() == nil {
		env := map[string]string{"CONAN_RCON_PORT": strconv.Itoa(settings.Port), "CONAN_RCON_PASSWORD": settings.Password}
		for key, value := range extra {
			env[key] = value
		}
		extra = env
	}
	return c.Hooks.Run(ctx, mode, event, cfg, extra)
}

// CheckHooks refuses a configuration naming a script that cannot run.
func (c *Controller) CheckHooks(cfg server.Config) error {
	if c.Hooks == nil {
		for _, scripts := range cfg.Hooks {
			if len(scripts) > 0 {
				return server.ErrInvalidConfig("hooks are disabled on this manager")
			}
		}
		return nil
	}
	for event, scripts := range cfg.Hooks {
		for _, name := range scripts {
			if err := c.Hooks.Check(name); err != nil {
				return server.ErrInvalidConfig(fmt.Sprintf("%s hook %s: %v", event, name, err))
			}
		}
	}
	return nil
}

// WaitForHooks lets background hooks finish before the manager exits.
func (c *Controller) WaitForHooks(timeout time.Duration) bool {
	if c.Hooks == nil {
		return true
	}
	return c.Hooks.Drain(timeout)
}

func exitEnv(exit *process.Exit) map[string]string {
	env := map[string]string{}
	if exit != nil {
		env["CONAN_EXIT"] = exit.String()
		env["CONAN_EXIT_CODE"] = fmt.Sprintf("%d", exit.Code)
		if exit.Signal != "" {
			env["CONAN_EXIT_SIGNAL"] = exit.Signal
		}
	}
	return env
}

// resetCrashes gives an operator start a clean slate, so a server the crash
// policy gave up on can be tried again deliberately.
func (c *Controller) resetCrashes(id string) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	h := c.health[id]
	h.crashes, h.RecentCrashes = nil, 0
	h.RestartPending, h.RestartAt = false, time.Time{}
	c.health[id] = h
}

const processLogKeep = 3

// rotateProcessLog starts each run with a fresh process log, keeping a few
// previous runs. The log is appended by every run and the watchdog refuses to
// start a server whose log has reached its size limit, so without rotation a
// long-lived server would eventually be unable to start at all.
func rotateProcessLog(install string) error {
	path := filepath.Join(install, "conan-manager-process.log")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	for i := processLogKeep - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", path, i)
		if err := os.Rename(from, fmt.Sprintf("%s.%d", path, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(path, path+".1")
}
