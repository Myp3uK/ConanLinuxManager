package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHangWatchdogStopsAndRestarts(t *testing.T) {
	c, create := testController(t)
	game := t.TempDir()
	bin := filepath.Join(game, "ConanSandbox", "Binaries", "Linux", "ConanSandboxServer-Linux-Shipping")
	if err := os.MkdirAll(filepath.Dir(bin), 0750); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ ! -f watchdog-once ]; then\n touch watchdog-once\n echo 'LogCore: Error: Hang detected on GameThread (thread has not sent a heartbeat)'\nfi\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0750); err != nil {
		t.Fatal(err)
	}
	create("a", game, "")
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Stop(ctx, "a")
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = c.Processes.Wait(wait, "a")
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		h := c.Health("a")
		if h.Restarts == 1 && c.Lifecycle("a").State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watchdog failed: %+v lifecycle=%+v", h, c.Lifecycle("a"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := c.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorReleasesAfterUnexpectedExit(t *testing.T) {
	c, create := testController(t)
	game := t.TempDir()
	bin := filepath.Join(game, "ConanSandbox", "Binaries", "Linux", "ConanSandboxServer-Linux-Shipping")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	create("a", game, "")
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.Lifecycle("a").State != "idle" {
		if time.Now().After(deadline) {
			t.Fatalf("lease retained: %+v", c.Lifecycle("a"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c.Snapshot("a").State != "failed" {
		t.Fatalf("process: %+v", c.Snapshot("a"))
	}
	if err := c.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownStopsRunningProcess(t *testing.T) {
	c, create := testController(t)
	game := t.TempDir()
	bin := filepath.Join(game, "ConanSandbox", "Binaries", "Linux", "ConanSandboxServer-Linux-Shipping")
	if err := os.MkdirAll(filepath.Dir(bin), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 60\n"), 0750); err != nil {
		t.Fatal(err)
	}
	create("a", game, "")
	if err := c.Start(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Processes.Stop(context.Background(), "a") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Lifecycle("a").State != "idle" || c.Snapshot("a").PID != 0 {
		t.Fatalf("shutdown incomplete: %+v %+v", c.Lifecycle("a"), c.Snapshot("a"))
	}
	if c.Health("a").Availability.State != "inactive" {
		t.Fatal("query observer still active")
	}
}
