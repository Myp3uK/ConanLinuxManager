package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/conan-manager/internal/server"
)

func TestUpdateConfigStoresSettings(t *testing.T) {
	c, create := testController(t)
	root := t.TempDir()
	install := filepath.Join(root, "game")
	if err := os.MkdirAll(install, 0o750); err != nil {
		t.Fatal(err)
	}
	create("a", install, "")
	got, err := c.UpdateConfig(context.Background(), "a", func(cfg *server.Config) {
		cfg.PeriodicBackupIntervalSeconds = 2400
		cfg.Name = "Renamed"
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.PeriodicBackupIntervalSeconds != 2400 || got.Name != "Renamed" {
		t.Fatalf("not stored: %+v", got)
	}
	// The reservation must be released again, or the server could never start.
	if state := c.Lifecycle("a").State; state != "idle" {
		t.Fatalf("lifecycle stuck at %q", state)
	}
}

// The game reads its INI files only at startup, so a change applied to a
// running server would look accepted while doing nothing until the next stop.
func TestUpdateConfigRefusesABusyServer(t *testing.T) {
	c, create := testController(t)
	root := t.TempDir()
	install := filepath.Join(root, "game")
	if err := os.MkdirAll(install, 0o750); err != nil {
		t.Fatal(err)
	}
	create("a", install, "")
	if _, err := c.acquire(context.Background(), "a", "starting"); err != nil {
		t.Fatal(err)
	}
	defer c.release("a")
	if _, err := c.UpdateConfig(context.Background(), "a", func(cfg *server.Config) {
		cfg.PeriodicBackupIntervalSeconds = 2400
	}); err == nil {
		t.Fatal("expected a conflict while the server is starting")
	}
}

func TestUpdateConfigRefusesMovingOnDiskState(t *testing.T) {
	c, create := testController(t)
	root := t.TempDir()
	install := filepath.Join(root, "game")
	if err := os.MkdirAll(install, 0o750); err != nil {
		t.Fatal(err)
	}
	create("a", install, "")
	for name, mutate := range map[string]func(*server.Config){
		"install_dir":  func(cfg *server.Config) { cfg.InstallDir = filepath.Join(root, "elsewhere") },
		"steamcmd_dir": func(cfg *server.Config) { cfg.SteamCMDDir = filepath.Join(root, "elsewhere") },
		"ramdisk_path": func(cfg *server.Config) { cfg.RamdiskPath = "/run/other" },
		"id":           func(cfg *server.Config) { cfg.ID = "b" },
		"engine":       func(cfg *server.Config) { cfg.Engine = "ue4" },
		"runtime":      func(cfg *server.Config) { cfg.Runtime = "wine" },
	} {
		_, err := c.UpdateConfig(context.Background(), "a", mutate)
		if err == nil {
			t.Errorf("%s: change accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "cannot be changed") {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if state := c.Lifecycle("a").State; state != "idle" {
			t.Errorf("%s: lifecycle stuck at %q", name, state)
		}
	}
}
