package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/scheduler"
	"github.com/example/conan-manager/internal/server"
)

// runningController has one server that the controller believes is running.
func runningController(t *testing.T) (*Controller, server.Config) {
	t.Helper()
	c, create := testController(t)
	c.Events = events.NewBus()
	cfg := create("pve", t.TempDir(), "")
	c.lifecycleMu.Lock()
	c.active[cfg.ID] = &reservation{Lifecycle: Lifecycle{State: "running"}}
	c.lifecycleMu.Unlock()
	t.Cleanup(func() { c.dropPending(cfg.ID) })
	return c, cfg
}

func scheduleIn(t *testing.T, c *Controller, id, action string, in time.Duration) {
	t.Helper()
	err := c.Repo.SetSchedule(context.Background(), scheduler.Schedule{ServerID: id, Action: action, Enabled: true,
		Mode: "interval", IntervalSeconds: 86400, NextRun: time.Now().Add(in)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDelayedRestartIsKeptUntilCancelled(t *testing.T) {
	c, cfg := runningController(t)
	ctx := context.Background()
	p, err := c.DelayRestart(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.DueAt.Sub(p.CreatedAt); got != 15*time.Minute {
		t.Fatalf("delay %s, want the default 15m", got)
	}
	if _, err := c.DelayRestart(ctx, cfg.ID); !errors.Is(err, ErrAlreadyPending) {
		t.Fatalf("second delayed restart: %v", err)
	}
	stored, _ := c.Repo.PendingRestarts(ctx)
	if len(stored) != 1 || stored[0].Reason != server.PendingManual {
		t.Fatalf("stored %+v", stored)
	}
	if next := c.NextPlanned(ctx, cfg.ID); next == nil || !next.At.Equal(p.DueAt) || next.Source != server.PendingManual {
		t.Fatalf("planned %+v", next)
	}
	if err := c.CancelPending(ctx, cfg.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ = c.Repo.PendingRestarts(ctx)
	if c.Pending(cfg.ID) != nil || len(stored) != 0 || countEvents(c.Events, cfg.ID, events.RestartCancelled) != 1 {
		t.Fatalf("after cancel: pending %v stored %v", c.Pending(cfg.ID), stored)
	}
}

func TestDelayedRestartGivesWayToASoonerSchedule(t *testing.T) {
	c, cfg := runningController(t)
	scheduleIn(t, c, cfg.ID, "restart", 8*time.Minute)
	var sooner ErrPlannedSooner
	if _, err := c.DelayRestart(context.Background(), cfg.ID); !errors.As(err, &sooner) {
		t.Fatalf("got %v", err)
	}
}

// An update found on a running server is announced once, with the time of
// the restart that installs it.
func TestUpdateSetsADelayedRestartOnce(t *testing.T) {
	c, cfg := runningController(t)
	c.updateSoon(context.Background(), cfg, "123")
	c.updateSoon(context.Background(), cfg, "123")
	p := c.Pending(cfg.ID)
	if p == nil || p.Reason != server.PendingUpdate || p.Detail != "123" {
		t.Fatalf("pending %+v", p)
	}
	var said []string
	for _, e := range c.Events.Recent(0) {
		if e.Type == events.UpdateAvailable {
			said = append(said, e.Details["announce"])
		}
	}
	if len(said) != 1 || !strings.Contains(said[0], "сборка 123") || !strings.Contains(said[0], "через 15 мин") {
		t.Fatalf("announced %q", said)
	}
}

// With a scheduled restart coming before the delay is out, the update waits
// for it, and the message says when that is.
func TestUpdateWaitsForASoonerScheduledRestart(t *testing.T) {
	c, cfg := runningController(t)
	scheduleIn(t, c, cfg.ID, "restart", 7*time.Minute+30*time.Second)
	c.updateSoon(context.Background(), cfg, "124")
	if p := c.Pending(cfg.ID); p != nil {
		t.Fatalf("pending %+v", p)
	}
	events := c.Events.Recent(0)
	last := events[len(events)-1]
	if !strings.Contains(last.Details["announce"], "через 8 мин") {
		t.Fatalf("announced %q", last.Details["announce"])
	}
}

func TestResumeDropsARestartWhoseTimePassed(t *testing.T) {
	c, cfg := runningController(t)
	ctx := context.Background()
	past := time.Now().Add(-10 * time.Minute)
	if err := c.Repo.SetPendingRestart(ctx, server.PendingRestart{ServerID: cfg.ID, Reason: server.PendingManual, DueAt: past, CreatedAt: past.Add(-15 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	c.ResumePending(ctx)
	stored, _ := c.Repo.PendingRestarts(ctx)
	if c.Pending(cfg.ID) != nil || len(stored) != 0 {
		t.Fatalf("resumed a restart from the past: %v", stored)
	}
	future := time.Now().Add(10 * time.Minute)
	_ = c.Repo.SetPendingRestart(ctx, server.PendingRestart{ServerID: cfg.ID, Reason: server.PendingUpdate, DueAt: future, CreatedAt: future.Add(-15 * time.Minute)})
	c.ResumePending(ctx)
	if p := c.Pending(cfg.ID); p == nil || !p.DueAt.Equal(future) {
		t.Fatalf("not resumed: %+v", p)
	}
}

func TestGameTextLeavesEmojiToDiscord(t *testing.T) {
	for in, want := range map[string]string{
		":warning: Рестарт через 5 мин.":        "Рестарт через 5 мин.",
		"⚠️ Рестарт 🔄 через 5 мин.":             "Рестарт через 5 мин.",
		"Сервер №1: 100% · «PvE» - 10:30 (UTC)": "Сервер №1: 100% · «PvE» - 10:30 (UTC)",
		"✅":                  "",
		"Рестарт в 10:30:00": "Рестарт в 10:30:00",
	} {
		if got := server.GameText(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// A run announced ready before the manager restarted is recognised by its
// start time, as systemd reports it to the next manager.
func TestReadyIsRememberedPerRun(t *testing.T) {
	c, cfg := runningController(t)
	ctx := context.Background()
	started := time.Date(2026, 9, 22, 3, 2, 30, 400_000_000, time.UTC)
	if c.Repo.WasReady(ctx, cfg.ID, started) {
		t.Fatal("ready before anything was recorded")
	}
	if err := c.Repo.MarkReady(ctx, cfg.ID, started); err != nil {
		t.Fatal(err)
	}
	if !c.Repo.WasReady(ctx, cfg.ID, started.Truncate(time.Second).Add(time.Second)) {
		t.Fatal("same run, as systemd reports it, not recognised")
	}
	if c.Repo.WasReady(ctx, cfg.ID, started.Add(2*time.Hour)) {
		t.Fatal("a later run counted as announced")
	}
}
