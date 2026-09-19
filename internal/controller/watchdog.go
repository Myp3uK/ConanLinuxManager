package controller

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/watchdog"
)

type Health struct {
	Availability   watchdog.Availability `json:"availability"`
	Reason         string                `json:"reason,omitempty"`
	DetectedAt     time.Time             `json:"detected_at,omitempty"`
	Restarts       int                   `json:"restarts"`
	RestartPending bool                  `json:"restart_pending"`
	Error          string                `json:"error,omitempty"`
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
			a.Record(time.Now().UTC(), err)
			publish()
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
	h.RestartPending = !c.closing && strings.HasPrefix(reason, "game_thread_hang:") && h.Restarts < 3
	if err := c.stopProcess(context.Background(), id); err != nil {
		h.Error = err.Error()
		h.RestartPending = false
	} else {
		held.State = "stopping"
	}
	c.health[id] = h
}

func (c *Controller) restartAfterWatchdog(id string) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.lifecycleMu.Lock()
	h := c.health[id]
	if c.closing || !h.RestartPending {
		c.lifecycleMu.Unlock()
		return
	}
	h.RestartPending = false
	h.Restarts++
	c.health[id] = h
	c.lifecycleMu.Unlock()
	if err := c.start(context.Background(), id); err != nil {
		c.lifecycleMu.Lock()
		h = c.health[id]
		h.Error = err.Error()
		c.health[id] = h
		c.lifecycleMu.Unlock()
	}
}
