package controller

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/store"
)

func testController(t *testing.T) (*Controller, func(string, string, string) server.Config) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := New(server.NewRepository(db.DB()))
	create := func(id, install, ram string) server.Config {
		t.Helper()
		cfg := server.Config{ID: id, Name: id, Engine: server.EngineUE5,
			Runtime: server.RuntimeNative, InstallDir: install,
			SteamCMDDir: filepath.Join(root, "steamcmd"), GamePort: 7777, QueryPort: 27015,
			RamdiskEnabled: ram != "", RamdiskPath: ram, RamdiskSizeMB: 128}
		if err := c.Repo.Create(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	return c, create
}

func TestConcurrentReservationAndSharedDirectories(t *testing.T) {
	c, create := testController(t)
	root := t.TempDir()
	create("a", filepath.Join(root, "game"), "")
	create("b", filepath.Join(root, "game", "nested"), "")
	create("c", filepath.Join(root, "game-other"), "")
	for _, name := range []string{"game", "game-other"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0750); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.acquire(ctx, "a", "starting"); err == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("reservation winners: %d", winners.Load())
	}
	for _, state := range []string{"starting", "running", "stopping", "flushing", "recovery_required", "installing"} {
		c.transition("a", state, nil)
		for _, id := range []string{"a", "b"} {
			if err := c.Start(ctx, id); err == nil {
				t.Fatalf("start %s during %s", id, state)
			}
			if _, err := c.Install(ctx, id); err == nil {
				t.Fatalf("install %s during %s", id, state)
			}
			if err := c.Delete(ctx, id); err == nil {
				t.Fatalf("delete %s during %s", id, state)
			}
		}
	}
	if _, err := c.acquire(ctx, "c", "starting"); err != nil {
		t.Fatal(err)
	}
	c.release("a")
	if err := c.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "b"); err == nil {
		t.Fatal("started deleted config")
	}
}

func TestSharedRAMAndSteamCMDReservations(t *testing.T) {
	c, create := testController(t)
	root := t.TempDir()
	create("a", filepath.Join(root, "a"), filepath.Join(root, "ram"))
	create("b", filepath.Join(root, "b"), filepath.Join(root, "ram"))
	create("c", filepath.Join(root, "c"), "")
	for _, name := range []string{"a", "b", "c", "ram"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0750); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := c.acquire(ctx, "a", "running"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.acquire(ctx, "b", "starting"); err == nil {
		t.Fatal("shared RAM accepted")
	}
	c.release("a")
	if _, err := c.acquire(ctx, "a", "installing"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.acquire(ctx, "c", "installing"); err == nil {
		t.Fatal("shared SteamCMD accepted")
	}
	if _, err := c.acquire(ctx, "c", "starting"); err != nil {
		t.Fatal(err)
	}
}

func TestPersistFailureRetainsLease(t *testing.T) {
	c, create := testController(t)
	root := t.TempDir()
	game := filepath.Join(root, "game")
	if err := os.MkdirAll(game, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := create("a", game, filepath.Join(root, "ram"))
	ctx := context.Background()
	if _, err := c.acquire(ctx, "a", "running"); err != nil {
		t.Fatal(err)
	}
	// Inject the filesystem failure after admission, as it could occur at runtime.
	if err := os.WriteFile(filepath.Join(game, "ConanSandbox"), []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.persist(cfg)
	state := c.Lifecycle("a")
	if state.State != "recovery_required" || state.Error == "" {
		t.Fatalf("missing recovery status: %+v", state)
	}
	if err := c.Start(ctx, "a"); err == nil {
		t.Fatal("start after failed persistence")
	}
	if err := c.Delete(ctx, "a"); err == nil {
		t.Fatal("delete after failed persistence")
	}
}

func TestCanonicalSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	a, err := canonicalPath(filepath.Join(alias, "new", "game"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalPath(filepath.Join(target, "new", "game"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("alias mismatch: %s != %s", a, b)
	}
}

func TestFailedStartReleasesLease(t *testing.T) {
	c, create := testController(t)
	create("a", filepath.Join(t.TempDir(), "missing"), "")
	if err := c.Start(context.Background(), "a"); err == nil {
		t.Fatal("expected missing executable error")
	}
	if state := c.Lifecycle("a"); state.State != "idle" {
		t.Fatalf("leaked lease: %+v", state)
	}
}
