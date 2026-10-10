package controller

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/hooks"
	"github.com/Myp3uK/ConanLinuxManager/internal/process"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
	"github.com/Myp3uK/ConanLinuxManager/internal/serverstats"
	"github.com/Myp3uK/ConanLinuxManager/internal/watchdog"
)

type Health struct {
	Availability watchdog.Availability `json:"availability"`
	// Ready is set when the game reports it has loaded and accepts players;
	// until then a running server is still starting.
	Ready          bool          `json:"ready"`
	ReadyAt        time.Time     `json:"ready_at,omitempty"`
	Reason         string        `json:"reason,omitempty"`
	DetectedAt     time.Time     `json:"detected_at,omitempty"`
	Restarts       int           `json:"restarts"`
	RestartPending bool          `json:"restart_pending"`
	RestartAt      time.Time     `json:"restart_at,omitempty"`
	RecentCrashes  int           `json:"recent_crashes"`
	LastExit       *process.Exit `json:"last_exit,omitempty"`
	Error          string        `json:"error,omitempty"`
	crashes        []time.Time
}

func (c *Controller) observeAvailability(ctx context.Context, cfg server.Config) {
	policy := c.AvailabilityPolicy
	endpoint := net.JoinHostPort(c.QueryHost, strconv.Itoa(cfg.QueryPort))
	// The loading allowance counts from the process start: a server the
	// manager adopted after its own restart has long finished loading.
	started := time.Now().UTC()
	if at := c.Snapshot(cfg.ID).StartedAt; !at.IsZero() && at.Before(started) {
		started = at.UTC()
	}
	a := watchdog.NewAvailability(endpoint, started, policy)
	go c.watchStartup(ctx, cfg)
	publish := func() {
		c.lifecycleMu.Lock()
		h := c.health[cfg.ID]
		h.Availability = a
		c.health[cfg.ID] = h
		c.lifecycleMu.Unlock()
	}
	publish()
	ticker := time.NewTicker(policy.Interval)
	defer ticker.Stop()
	defer func() { a.State = "inactive"; a.WouldRecover = false; publish() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.Lifecycle(cfg.ID).State != "running" {
				a.State, a.WouldRecover = "suspended", false
				publish()
				continue
			}
			err := watchdog.ProbeQuery(ctx, endpoint, policy.Timeout)
			if ctx.Err() != nil {
				return
			}
			if c.Lifecycle(cfg.ID).State != "running" {
				continue
			}
			previous := a.State
			a.Record(time.Now().UTC(), err)
			publish()
			if a.State == "unavailable" && previous != "unavailable" {
				c.emit(events.ServerUnavailable, cfg, "Server stopped answering queries", map[string]string{"error": a.Error})
			}
			// The zombie check: a process that is alive but has answered no
			// query for this long is not serving anyone, so it is restarted.
			// The setting is read each time, so a change applies at once.
			if limit := c.zombieLimit(cfg.ID); limit > 0 && !a.FailureSince.IsZero() && time.Since(a.FailureSince) >= limit {
				c.trip(cfg.ID, fmt.Sprintf("zombie: no query answer for %s", time.Since(a.FailureSince).Round(time.Second)))
			}
		}
	}
}

// watchStartup waits for the game's startup report, which is when the
// server has loaded and accepts players; the process starting is not that,
// a large world takes minutes. Then it announces the server as started and
// runs on_ready. A run already announced before the manager restarted and
// adopted it is ready without a word: players would read that the server
// just started, and on_ready scripts would run twice.
func (c *Controller) watchStartup(ctx context.Context, cfg server.Config) {
	c.setReady(cfg.ID, time.Time{})
	var scan *serverstats.Startup
	var since time.Time
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if c.Lifecycle(cfg.ID).State != "running" {
			continue
		}
		if scan == nil {
			// An adopted unit's start time comes from systemd a moment after
			// adoption; the run is judged by it, so it is waited for.
			run := c.Snapshot(cfg.ID).StartedAt
			if run.IsZero() {
				continue
			}
			if c.Repo.WasReady(ctx, cfg.ID, run) {
				c.setReady(cfg.ID, run)
				return
			}
			since = run
			scan = &serverstats.Startup{Path: filepath.Join(cfg.InstallDir, LogNames["game"])}
		}
		at, found, _ := scan.Find(since)
		if !found {
			continue
		}
		c.setReady(cfg.ID, at)
		c.emit(events.ServerReady, cfg, "Server is accepting connections", nil)
		_ = c.runHooks(ctx, hooks.Async, "on_ready", cfg, nil)
		if err := c.Repo.MarkReady(ctx, cfg.ID, since); err != nil {
			c.Logger.Warn("readiness not recorded", "server", cfg.ID, "error", err)
		}
		return
	}
}

// setReady records readiness; a zero time means not ready yet.
func (c *Controller) setReady(id string, at time.Time) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	h := c.health[id]
	h.Ready, h.ReadyAt = !at.IsZero(), at
	c.health[id] = h
}

func (c *Controller) zombieLimit(id string) time.Duration {
	cfg, err := c.Repo.Get(context.Background(), id)
	if err != nil {
		return 0
	}
	return time.Duration(cfg.ZombieCheckSeconds) * time.Second
}

func (c *Controller) Health(id string) Health {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.health[id]
}

func (c *Controller) trip(id, reason string) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	held := c.active[id]
	if held == nil || held.State != "running" {
		return
	}
	h := c.health[id]
	h.Reason, h.DetectedAt = reason, time.Now().UTC()
	// Three automatic attempts per manager lifetime. Log floods require operator
	// inspection rather than immediately recreating the same disk pressure.
	hang := strings.HasPrefix(reason, "game_thread_hang:") || strings.HasPrefix(reason, "zombie:")
	h.RestartPending = !c.closing && hang && h.Restarts < 3
	if err := c.stopProcess(context.Background(), id); err != nil {
		h.Error = err.Error()
		h.RestartPending = false
	} else {
		c.beginStopping(id)
	}
	c.health[id] = h
	if cfg, err := c.Repo.Get(context.Background(), id); err == nil {
		details := map[string]string{"reason": reason}
		if strings.HasPrefix(reason, "zombie:") {
			c.emit(events.ServerHang, cfg, "Server has not answered for too long (zombie check); restarting it", details)
		} else if hang {
			c.emit(events.ServerHang, cfg, "Game thread hang detected; stopping the server", details)
		} else {
			c.emit(events.ServerWatchdogStop, cfg, "Watchdog is stopping the server", details)
		}
	}
}
