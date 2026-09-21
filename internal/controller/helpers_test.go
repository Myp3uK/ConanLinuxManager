package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/server"
)

func createServer(t *testing.T, c *Controller, id, install string, restartOnCrash bool) {
	t.Helper()
	cfg := server.Config{ID: id, Name: id, Engine: server.EngineUE5, Runtime: server.RuntimeNative,
		InstallDir: install, SteamCMDDir: filepath.Join(t.TempDir(), "steamcmd"),
		GamePort: 7777, QueryPort: 27015, RestartOnCrash: restartOnCrash}
	if err := c.Repo.Create(context.Background(), cfg); err != nil {
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
