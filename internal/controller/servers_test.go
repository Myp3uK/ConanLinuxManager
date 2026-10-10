package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func TestConflictsAreFoundBeforeTheyBite(t *testing.T) {
	c, _ := testController(t)
	root := t.TempDir()
	createServer(t, c, "a", filepath.Join(root, "a"), false) // ports 7777, 7778, 27015
	ctx := context.Background()
	base := server.Config{ID: "b", Name: "b", Engine: server.EngineUE5, Runtime: server.RuntimeNative,
		InstallDir: filepath.Join(root, "b"), SteamCMDDir: filepath.Join(root, "steam"), GamePort: 7787, QueryPort: 27025}
	if err := c.CheckConflicts(ctx, base); err != nil {
		t.Fatalf("distinct server refused: %v", err)
	}
	for name, mutate := range map[string]func(*server.Config){
		"same game port":        func(s *server.Config) { s.GamePort = 7777 },
		"game port next to one": func(s *server.Config) { s.GamePort = 7776 },
		"query port on game+1":  func(s *server.Config) { s.QueryPort = 7778 },
		"same query port":       func(s *server.Config) { s.QueryPort = 27015 },
		"install inside other":  func(s *server.Config) { s.InstallDir = filepath.Join(root, "a", "nested") },
		"install around other":  func(s *server.Config) { s.InstallDir = root },
	} {
		cfg := base
		mutate(&cfg)
		if c.CheckConflicts(ctx, cfg) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A server does not conflict with itself when it is edited.
	existing, _ := c.Repo.Get(ctx, "a")
	if err := c.CheckConflicts(ctx, existing); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteKeepsTheWorldUnlessAsked(t *testing.T) {
	c, _ := testController(t)
	c.BackupDir = filepath.Join(t.TempDir(), "backups")
	root := t.TempDir()
	ctx := context.Background()
	for _, id := range []string{"keep", "wipe"} {
		install := filepath.Join(root, "servers", id, "game")
		if err := os.MkdirAll(install, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(install, "game_0.db"), []byte("world"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(c.BackupDir, id), 0o750); err != nil {
			t.Fatal(err)
		}
		createServer(t, c, id, install, false)
	}
	if err := c.Delete(ctx, "keep", DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "servers", "keep", "game", "game_0.db")); err != nil {
		t.Fatal("a plain delete removed the world")
	}
	if err := c.Delete(ctx, "wipe", DeleteOptions{Files: true, Backups: true}); err != nil {
		t.Fatal(err)
	}
	// The emptied servers/<id> directory goes with the game; the other
	// server's stays.
	for _, gone := range []string{filepath.Join(root, "servers", "wipe"), filepath.Join(c.BackupDir, "wipe")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(c.BackupDir, "keep")); err != nil {
		t.Fatal("another server's backups were removed")
	}
	if _, err := os.Stat(filepath.Join(root, "servers")); err != nil {
		t.Fatal("the servers directory was removed")
	}
}

func TestDeleteRefusesToRemoveSharedOrShallowDirectories(t *testing.T) {
	c, _ := testController(t)
	root := t.TempDir()
	createServer(t, c, "outer", root, false)
	createServer(t, c, "inner", filepath.Join(root, "inner"), false)
	if err := c.Delete(context.Background(), "outer", DeleteOptions{Files: true}); err == nil {
		t.Fatal("deleting a directory holding another server was allowed")
	}
	if _, err := c.Repo.Get(context.Background(), "outer"); err != nil {
		t.Fatal("a refused delete removed the record")
	}
	createServer(t, c, "shallow", string(filepath.Separator)+"x", false)
	if err := c.safeToRemove(context.Background(), server.Config{ID: "shallow", InstallDir: string(filepath.Separator) + "x"}, string(filepath.Separator)+"x"); err == nil {
		t.Fatal("a top-level directory may be removed")
	}
}

func TestServerIDMustBeSafeForPaths(t *testing.T) {
	for _, id := range []string{"../x", "a/b", "-a", "a b", ""} {
		cfg := server.Config{ID: id, Name: "n", Engine: server.EngineUE5, Runtime: server.RuntimeNative,
			InstallDir: "/srv/x", SteamCMDDir: "/srv/s", GamePort: 7777, QueryPort: 27015}
		if cfg.Validate() == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}
