package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/events"
)

// fakeGame installs a script where the native runtime expects the server binary.
func fakeGame(t *testing.T, script string) string {
	t.Helper()
	game := t.TempDir()
	bin := filepath.Join(game, "ConanSandbox", "Binaries", "Linux", "ConanSandboxServer-Linux-Shipping")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(script), 0o750); err != nil {
		t.Fatal(err)
	}
	return game
}

// A server that keeps crashing is restarted a bounded number of times and then
// left down, and a reboot must not bring it back either.
func TestCrashIsRestartedThenAbandoned(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.CrashRestartLimit, c.CrashRestartWindow, c.CrashRestartDelay = 2, time.Minute, 10*time.Millisecond
	createServer(t, c, "a", fakeGame(t, "#!/bin/sh\nexit 3\n"), true)
	if err := c.Start(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "restart to be abandoned", func() bool {
		return countEvents(c.Events, "a", events.ServerRestartAbandoned) == 1 && c.Lifecycle("a").State == "idle"
	})
	if got := countEvents(c.Events, "a", events.ServerCrashed); got != 3 {
		t.Errorf("crashes reported: %d, want 3", got)
	}
	if got := countEvents(c.Events, "a", events.ServerRestarting); got != 2 {
		t.Errorf("restarts announced: %d, want 2", got)
	}
	h := c.Health("a")
	if h.RecentCrashes != 3 || h.LastExit == nil || h.LastExit.Code != 3 {
		t.Errorf("health: %+v exit=%+v", h, h.LastExit)
	}
	cfg, err := c.Repo.Get(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DesiredState != "stopped" {
		t.Errorf("abandoned server would come back after reboot: desired=%s", cfg.DesiredState)
	}
}

// An operator stop while a crash restart is pending wins over the restart.
func TestOperatorStopCancelsPendingCrashRestart(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.CrashRestartDelay = 300 * time.Millisecond
	createServer(t, c, "a", fakeGame(t, "#!/bin/sh\nexit 1\n"), true)
	if err := c.Start(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "crash", func() bool { return c.Health("a").RestartPending })
	if err := c.Stop(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if c.Health("a").Restarts != 0 || c.Lifecycle("a").State != "idle" {
		t.Fatalf("server restarted after an operator stop: %+v %+v", c.Health("a"), c.Lifecycle("a"))
	}
}

// A requested stop that ends in SIGTERM is not a crash, and must not trigger
// the crash policy.
func TestRequestedStopIsNotACrash(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	createServer(t, c, "a", fakeGame(t, "#!/bin/sh\nexec sleep 60\n"), true)
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "stop", func() bool { return countEvents(c.Events, "a", events.ServerStopped) == 1 })
	if countEvents(c.Events, "a", events.ServerCrashed) != 0 || c.Health("a").RestartPending {
		t.Fatalf("stop treated as crash: %+v", c.Events.Recent(0))
	}
	cfg, _ := c.Repo.Get(ctx, "a")
	if cfg.DesiredState != "stopped" {
		t.Fatalf("desired state after stop: %s", cfg.DesiredState)
	}
}

func TestKillEndsAServerThatIgnoresSIGTERM(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	createServer(t, c, "a", fakeGame(t, "#!/bin/sh\ntrap '' TERM\nwhile true; do sleep 1; done\n"), true)
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if c.Lifecycle("a").State != "stopping" {
		t.Fatalf("expected the server to ignore SIGTERM: %+v", c.Lifecycle("a"))
	}
	if err := c.Kill(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "kill", func() bool { return c.Lifecycle("a").State == "idle" })
	if countEvents(c.Events, "a", events.ServerCrashed) != 0 {
		t.Fatal("kill reported as crash")
	}
	if !c.Snapshot("a").Forced {
		t.Fatal("snapshot does not show the forced stop")
	}
}

// After a reboot, a server the operator left running comes back.
func TestReconcileRestoresDesiredRunning(t *testing.T) {
	c, _ := testController(t)
	createServer(t, c, "a", fakeGame(t, "#!/bin/sh\nexec sleep 60\n"), true)
	createServer(t, c, "b", fakeGame(t, "#!/bin/sh\nexec sleep 60\n"), true)
	ctx := context.Background()
	if err := c.Repo.SetDesiredState(ctx, "a", "running"); err != nil {
		t.Fatal(err)
	}
	c.Reconcile(ctx)
	t.Cleanup(func() {
		_ = c.Stop(ctx, "a")
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = c.Processes.Wait(wait, "a")
	})
	if c.Lifecycle("a").State != "running" {
		t.Fatalf("desired running server not restored: %+v", c.Lifecycle("a"))
	}
	if c.Lifecycle("b").State != "idle" {
		t.Fatalf("stopped server was started: %+v", c.Lifecycle("b"))
	}
}
