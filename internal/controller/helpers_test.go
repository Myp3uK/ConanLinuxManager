package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func createServer(t *testing.T, c *Controller, id, install string, restartOnCrash bool) {
	t.Helper()
	cfg := server.Config{ID: id, Name: id, Engine: server.EngineUE5, Runtime: server.RuntimeNative,
		InstallDir: install, SteamCMDDir: filepath.Join(t.TempDir(), "steamcmd"),
		GamePort: 7777, QueryPort: 27015, RestartOnCrash: restartOnCrash}
	if err := c.Repo.Create(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	placeGame(t, install)
}

// placeGame puts a stand-in for the game's executable where a start looks
// for it; the tests replace the process itself.
func placeGame(t *testing.T, install string) {
	t.Helper()
	path := filepath.Join(install, "ConanSandbox", "Binaries", "Linux", "ConanSandboxServer-Linux-Shipping")
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o750); err != nil {
		t.Fatal(err)
	}
}

func countEvents(bus *events.Bus, id string, t events.Type) int {
	n := 0
	for _, e := range bus.Recent(0) {
		if e.ServerID == id && e.Type == t {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
