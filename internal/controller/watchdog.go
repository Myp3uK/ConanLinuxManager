package controller

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/hooks"
	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/watchdog"
)

type Health struct {
	Availability   watchdog.Availability `json:"availability"`
	Reason         string                `json:"reason,omitempty"`
	DetectedAt     time.Time             `json:"detected_at,omitempty"`
	Restarts       int                   `json:"restarts"`
	RestartPending bool                  `json:"restart_pending"`
	RestartAt      time.Time             `json:"restart_at,omitempty"`
	RecentCrashes  int                   `json:"recent_crashes"`
	LastExit       *process.Exit         `json:"last_exit,omitempty"`
	Error          string                `json:"error,omitempty"`
	crashes        []time.Time
}

func (c *Controller) observeAvailability(ctx context.Context, cfg server.Config) {
	policy := c.AvailabilityPolicy
	endpoint := net.JoinHostPort(c.QueryHost, strconv.Itoa(cfg.QueryPort))
	a := watchdog.NewAvailability(endpoint, time.Now().UTC(), policy)
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
			// The process starting is not the server being joinable: a large
			// world takes minutes to load. The first query reply is.
			if a.State == "responsive" && previous != "responsive" {
				c.emit(events.ServerReady, cfg, "Server is accepting connections", nil)
				_ = c.runHooks(ctx, hooks.Async, "on_ready", cfg, nil)
			}
			if a.State == "unavailable" && previous != "unavailable" {
				c.emit(events.ServerUnavailable, cfg, "Server stopped answering queries", map[string]string{"error": a.Error})
			}
		}
	}
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
	hang := strings.HasPrefix(reason, "game_thread_hang:")
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
		if hang {
			c.emit(events.ServerHang, cfg, "Game thread hang detected; stopping the server", details)
		} else {
			c.emit(events.ServerWatchdogStop, cfg, "Watchdog is stopping the server", details)
		}
	}
}
