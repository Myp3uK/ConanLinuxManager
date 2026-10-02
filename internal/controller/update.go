package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/hooks"
	"github.com/example/conan-manager/internal/ramstate"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/steamcmd"
)

// UpdateStatus compares the installed build with the newest one on Steam.
type UpdateStatus struct {
	Branch    string    `json:"branch"`
	Installed string    `json:"installed,omitempty"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

// latestCache avoids asking Steam once per server when several servers share
// a branch, and keeps an API client from hammering SteamCMD.
type latestCache struct {
	mu      sync.Mutex
	entries map[string]cachedBuild
}

type cachedBuild struct {
	build string
	at    time.Time
}

const latestCacheFor = 5 * time.Minute

func (c *Controller) latestBuild(ctx context.Context, cfg server.Config) (string, error) {
	branch := cfg.Branch
	if branch == "" {
		branch = "public"
	}
	key := cfg.SteamCMDDir + "\x00" + branch
	c.latest.mu.Lock()
	if c.latest.entries == nil {
		c.latest.entries = map[string]cachedBuild{}
	}
	if hit, ok := c.latest.entries[key]; ok && time.Since(hit.at) < latestCacheFor {
		c.latest.mu.Unlock()
		return hit.build, nil
	}
	c.latest.mu.Unlock()
	build, err := c.Steam.LatestBuild(ctx, cfg.SteamCMDDir, branch)
	if err != nil {
		return "", err
	}
	c.latest.mu.Lock()
	c.latest.entries[key] = cachedBuild{build: build, at: time.Now()}
	c.latest.mu.Unlock()
	return build, nil
}

// CheckUpdate reports whether Steam has a newer build than the one installed.
func (c *Controller) CheckUpdate(ctx context.Context, id string) (UpdateStatus, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return UpdateStatus{}, err
	}
	status := UpdateStatus{Branch: cfg.Branch, CheckedAt: time.Now().UTC()}
	if status.Branch == "" {
		status.Branch = "public"
	}
	installed, installedErr := steamcmd.InstalledBuild(cfg.InstallDir)
	status.Installed = installed
	latest, err := c.latestBuild(ctx, cfg)
	if err != nil {
		status.Error = err.Error()
		return status, nil
	}
	status.Latest = latest
	// An install that was never completed has no build; that is an update too.
	status.Available = installedErr != nil || installed != latest
	return status, nil
}

// UpdateJob updates the server files in the background: pre_update hooks, a
// stop if the server runs, SteamCMD, post_update hooks, and a start again if
// it was running. A running server is always brought back, even when the
// update failed, rather than left down.
// UpdateOptions choose how an update runs.
type UpdateOptions struct {
	// Validate re-checks every installed file against Steam.
	Validate bool `json:"validate"`
	// Force updates even when the installed build is the latest.
	Force bool `json:"force"`
	// Warn tells players first, as for a scheduled restart.
	Warn bool `json:"warn"`
}

func (c *Controller) UpdateJob(ctx context.Context, id string, opts UpdateOptions) (Job, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !c.beginUpdate(id) {
		return Job{}, fmt.Errorf("an update of server %q is already running", id)
	}
	now := time.Now().UTC()
	job := Job{ID: fmt.Sprintf("update-%d", now.UnixNano()), ServerID: id, Kind: "update", Status: "running", CreatedAt: now, UpdatedAt: now}
	c.setJob(job)
	// The goroutine gets its own copy: the caller returns job concurrently.
	go func(job Job) {
		defer c.endUpdate(id)
		detail, err := c.runUpdate(context.Background(), cfg, opts)
		job.UpdatedAt = time.Now().UTC()
		job.Status, job.Error = "completed", ""
		if detail != "" {
			job.Error = detail
		}
		if err != nil {
			job.Status, job.Error = "failed", err.Error()
		}
		c.setJob(job)
	}(job)
	return job, nil
}

func (c *Controller) runUpdate(ctx context.Context, cfg server.Config, opts UpdateOptions) (string, error) {
	validate, force := opts.Validate, opts.Force
	status, err := c.CheckUpdate(ctx, cfg.ID)
	if err != nil {
		return "", err
	}
	if !status.Available && !force && !validate {
		return "already up to date (build " + status.Installed + ")", nil
	}
	env := map[string]string{"CONAN_BUILD_FROM": status.Installed, "CONAN_BUILD_TO": status.Latest}
	if err := c.runHooks(ctx, hooks.Gate, "pre_update", cfg, env); err != nil {
		c.emit(events.UpdateFailed, cfg, "A pre_update hook refused the update", map[string]string{"error": err.Error()})
		return "", fmt.Errorf("pre_update hook: %w", err)
	}
	wasRunning := c.Lifecycle(cfg.ID).State == "running"
	if wasRunning {
		if err := c.warnPlayers(ctx, cfg, server.MsgUpdate, updateVars(status.Latest), opts.Warn); err != nil {
			return "", err
		}
	}
	c.emit(events.UpdateStarted, cfg, "Updating server files", map[string]string{"from": status.Installed, "to": status.Latest})
	if wasRunning {
		if err := c.stopAndWait(ctx, cfg.ID); err != nil {
			c.emit(events.UpdateFailed, cfg, "Could not stop the server for the update", map[string]string{"error": err.Error()})
			return "", err
		}
	}
	if _, err := os.Stat(ramstate.LayoutFor(cfg).GameDatabase); err == nil {
		if _, err := c.takeBackup(ctx, cfg, ReasonPreUpdate); err != nil {
			c.emit(events.UpdateFailed, cfg, "Could not back up the world before the update", map[string]string{"error": err.Error()})
			if wasRunning {
				if startErr := c.Start(ctx, cfg.ID); startErr != nil {
					err = errors.Join(err, fmt.Errorf("start after failed backup: %w", startErr))
				}
			}
			return "", fmt.Errorf("backup before update: %w", err)
		}
	}
	updateErr := c.installFiles(ctx, cfg, validate)
	installed, _ := steamcmd.InstalledBuild(cfg.InstallDir)
	if updateErr != nil {
		c.emit(events.UpdateFailed, cfg, "Update failed", map[string]string{"error": updateErr.Error(), "build": installed})
	} else {
		c.emit(events.UpdateFinished, cfg, "Server files updated", map[string]string{"from": status.Installed, "to": installed})
		env["CONAN_BUILD_TO"] = installed
		_ = c.runHooks(ctx, hooks.Async, "post_update", cfg, env)
	}
	if wasRunning {
		if err := c.Start(ctx, cfg.ID); err != nil {
			return "", errors.Join(updateErr, fmt.Errorf("start after update: %w", err))
		}
	}
	return "", updateErr
}

// updateBeforeStart installs a newer build before an operator's or the
// schedule's start of a server with auto_update. A crash restart and the
// start after a host reboot skip it: they should bring the server back fast.
// Any failure leaves the installed build, and the server starts on it.
func (c *Controller) updateBeforeStart(ctx context.Context, id string) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil || !cfg.AutoUpdate || c.Lifecycle(id).State != "idle" {
		return
	}
	// An update already running starts the server itself when it is done.
	if !c.beginUpdate(id) {
		return
	}
	defer c.endUpdate(id)
	c.buildBeforeStart(ctx, cfg)
	// Mods go in at the same moment as a new build, under the same
	// reservation: one switch covers both. A build that was up to date, or one
	// whose update failed, does not keep the mods from being brought up.
	c.modsBeforeStart(ctx, cfg)
}

// modsBeforeStart installs the mods a server is missing and those that changed
// on Steam. The server is idle here, so nothing is stopped or started; a
// failure leaves the mods as they are and the server starts on them.
func (c *Controller) modsBeforeStart(ctx context.Context, cfg server.Config) {
	if _, err := c.runMods(ctx, cfg, ModsOptions{}); err != nil {
		c.Logger.Warn("mods not updated before the start", "server", cfg.ID, "error", err)
	}
}

func (c *Controller) buildBeforeStart(ctx context.Context, cfg server.Config) {
	id := cfg.ID
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	status, err := c.CheckUpdate(checkCtx, id)
	cancel()
	if err != nil || status.Error != "" {
		c.Logger.Warn("update check before start failed; starting the installed build", "server", id, "error", err, "detail", status.Error)
		return
	}
	if !status.Available {
		return
	}
	env := map[string]string{"CONAN_BUILD_FROM": status.Installed, "CONAN_BUILD_TO": status.Latest}
	if err := c.runHooks(ctx, hooks.Gate, "pre_update", cfg, env); err != nil {
		c.emit(events.UpdateFailed, cfg, "A pre_update hook refused the update", map[string]string{"error": err.Error()})
		return
	}
	// Found by the periodic check, the build was announced already.
	if c.noteUpdate(ctx, id, server.NoticeBuild, status.Latest) {
		_ = c.message(ctx, cfg, server.MsgUpdate, updateVars(status.Latest), false)
	}
	c.emit(events.UpdateStarted, cfg, "Updating server files before the start", map[string]string{"from": status.Installed, "to": status.Latest})
	if _, err := os.Stat(ramstate.LayoutFor(cfg).GameDatabase); err == nil {
		if _, err := c.takeBackup(ctx, cfg, ReasonPreUpdate); err != nil {
			c.emit(events.UpdateFailed, cfg, "Could not back up the world before the update", map[string]string{"error": err.Error()})
			return
		}
	}
	if err := c.installFiles(ctx, cfg, false); err != nil {
		installed, _ := steamcmd.InstalledBuild(cfg.InstallDir)
		c.emit(events.UpdateFailed, cfg, "Update failed", map[string]string{"error": err.Error(), "build": installed})
		return
	}
	installed, _ := steamcmd.InstalledBuild(cfg.InstallDir)
	c.emit(events.UpdateFinished, cfg, "Server files updated", map[string]string{"from": status.Installed, "to": installed})
	env["CONAN_BUILD_TO"] = installed
	_ = c.runHooks(ctx, hooks.Async, "post_update", cfg, env)
}

// installFiles runs SteamCMD under the install reservation, which keeps any
// start, and any other install into the same directories, out meanwhile.
func (c *Controller) installFiles(ctx context.Context, cfg server.Config, validate bool) error {
	if _, err := c.acquire(ctx, cfg.ID, "installing"); err != nil {
		return err
	}
	defer c.release(cfg.ID)
	what := "update"
	if validate {
		what = "verify"
	}
	out := openSteamLog(cfg, what)
	defer out.Close()
	return c.Steam.UpdateServer(ctx, cfg.SteamCMDDir, cfg.InstallDir, cfg.Branch, validate, out)
}

// stopAndWait stops a server and waits until it is idle, as a restart does.
func (c *Controller) stopAndWait(ctx context.Context, id string) error {
	if err := c.Stop(ctx, id); err != nil {
		return err
	}
	deadline := time.Now().Add(restartStopTimeout)
	for time.Now().Before(deadline) {
		switch c.Lifecycle(id).State {
		case "idle":
			return nil
		case "recovery_required":
			return fmt.Errorf("server needs recovery after stopping")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for server %q to stop", id)
}

func (c *Controller) beginUpdate(id string) bool {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.updating == nil {
		c.updating = map[string]bool{}
	}
	if c.updating[id] {
		return false
	}
	c.updating[id] = true
	return true
}

func (c *Controller) endUpdate(id string) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	delete(c.updating, id)
}

// WatchUpdates checks every server for a newer build at the given interval.
// It announces each new build once per server, and updates servers that have
// auto_update enabled.
func (c *Controller) WatchUpdates(ctx context.Context, interval time.Duration) {
	check := func() {
		configs, err := c.Repo.List(ctx)
		if err != nil {
			c.Logger.Error("list servers for update check", "error", err)
			return
		}
		for _, cfg := range configs {
			status, err := c.CheckUpdate(ctx, cfg.ID)
			if status.Error != "" {
				c.Logger.Warn("update check failed", "server", cfg.ID, "error", status.Error)
			}
			running := c.Lifecycle(cfg.ID).State == "running"
			if err == nil && status.Error == "" && status.Available && status.Latest != "" {
				switch {
				case cfg.AutoUpdate && running:
					c.updateSoon(ctx, cfg, status.Latest)
				case cfg.AutoUpdate:
					// Nobody plays on a stopped server: it is updated right away.
					if c.Lifecycle(cfg.ID).State == "idle" {
						if _, err := c.UpdateJob(ctx, cfg.ID, UpdateOptions{}); err != nil {
							c.Logger.Info("automatic update not started", "server", cfg.ID, "error", err)
						}
					}
				case worthAnnouncing(status) && c.noteUpdate(ctx, cfg.ID, server.NoticeBuild, status.Latest):
					c.announceUpdate(ctx, cfg, updateVars(status.Latest), running)
				}
			}
			// A build that is current, or that could not be checked, says
			// nothing about the mods, so they are checked either way.
			c.watchMods(ctx, cfg, running)
		}
	}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			check()
			timer.Reset(interval)
		}
	}
}

// worthAnnouncing reports whether an available build is worth telling the
// operator about. A server whose game was never installed is not behind a
// build, it has none: CheckUpdate calls that available so the panel can offer
// the install, but announcing it as an update points at the wrong button, and
// the card already says the server is not installed. Auto-update is not gated
// by this - a server set to update itself is left to install itself as before.
func worthAnnouncing(status UpdateStatus) bool { return status.Installed != "" }

// updateSoon announces a new build on a running server with auto_update and
// sets the restart that installs it: a delayed restart, or the scheduled one
// when that comes before the delay is out. Each build is handled once, so a
// delayed restart the operator cancelled is not set again; the build then
// goes in at the next restart.
func (c *Controller) updateSoon(ctx context.Context, cfg server.Config, build string) {
	if !c.noteUpdate(ctx, cfg.ID, server.NoticeBuild, build) {
		return
	}
	c.announceSoon(ctx, cfg, updateVars(build), build)
}

// announceSoon announces what is waiting and sets the restart that installs it.
// Builds and mods share it: to a player they are the same interruption.
func (c *Controller) announceSoon(ctx context.Context, cfg server.Config, vars map[string]string, detail string) {
	p, err := c.planRestart(ctx, cfg, server.PendingUpdate, detail)
	var sooner ErrPlannedSooner
	switch {
	case err == nil:
		vars["minutes"] = minutesUntil(p.DueAt)
	case errors.As(err, &sooner):
		vars["minutes"] = minutesUntil(sooner.At)
	case errors.Is(err, ErrAlreadyPending):
		if p := c.Pending(cfg.ID); p != nil {
			vars["minutes"] = minutesUntil(p.DueAt)
		}
	default:
		c.Logger.Warn("no restart set for the update", "server", cfg.ID, "error", err)
	}
	c.announceUpdate(ctx, cfg, vars, true)
}

// announceUpdate says the update message, in the game if anyone is there.
func (c *Controller) announceUpdate(ctx context.Context, cfg server.Config, vars map[string]string, running bool) {
	if err := c.message(ctx, cfg, server.MsgUpdate, vars, running); err != nil {
		c.Logger.Warn("update announcement not said in game", "server", cfg.ID, "error", err)
	}
}

// noteUpdate records what was last announced for a server and kind, and
// reports whether it is new. The answer is kept in the database: in memory
// alone, every restart of the manager announced the same waiting build again,
// which is how a stopped server came to report the same update hourly.
func (c *Controller) noteUpdate(ctx context.Context, id, kind, value string) bool {
	fresh, err := c.Repo.NoteOnce(ctx, id, kind, value)
	if err == nil {
		return fresh
	}
	// Without the database the manager still must not repeat itself within
	// its own run, so the old in-memory note stands in.
	c.Logger.Warn("announcement not recorded", "server", id, "kind", kind, "error", err)
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.updateNotes == nil {
		c.updateNotes = map[string]string{}
	}
	key := id + "\x00" + kind
	if c.updateNotes[key] == value {
		return false
	}
	c.updateNotes[key] = value
	return true
}

// updateVars fill the update message for a new game build. Mods will pass
// "мода" and their names.
func updateVars(build string) map[string]string {
	return map[string]string{"target": "игры", "version": "сборка " + build}
}
