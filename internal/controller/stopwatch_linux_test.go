package controller

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	_ "modernc.org/sqlite"
)

func killReason(bus *events.Bus, id string) string {
	for _, e := range bus.Recent(0) {
		if e.ServerID == id && e.Type == events.ServerKilled {
			return e.Details["reason"]
		}
	}
	return ""
}

// writeWorld puts a small valid world where the game keeps its database.
func writeWorld(t *testing.T, install string) string {
	t.Helper()
	saved := filepath.Join(install, "ConanSandbox", "Saved")
	if err := os.MkdirAll(saved, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(saved, "game_0.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []string{"PRAGMA journal_mode=WAL", "CREATE TABLE actors (id INTEGER PRIMARY KEY, data BLOB)", "INSERT INTO actors(data) VALUES (randomblob(4000))"} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

const ignoresSIGTERM = "#!/bin/sh\ntrap '' TERM\nwhile true; do sleep 1; done\n"

func TestStopTimeoutKillsAndVerifiesTheWorld(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.StopTimeout = 500 * time.Millisecond
	game := fakeGame(t, ignoresSIGTERM)
	writeWorld(t, game)
	createServer(t, c, "a", game, true)
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "kill and check", func() bool { return c.Lifecycle("a").State == "idle" })
	if reason := killReason(c.Events, "a"); !strings.Contains(reason, "did not stop within") {
		t.Fatalf("kill reason %q", reason)
	}
	if countEvents(c.Events, "a", events.DatabaseVerified) != 1 || countEvents(c.Events, "a", events.DatabaseCorrupt) != 0 {
		t.Fatalf("world not verified after kill: %+v", c.Events.Recent(0))
	}
	if countEvents(c.Events, "a", events.ServerCrashed) != 0 {
		t.Fatal("a timed-out stop was reported as a crash")
	}
}

// A world that fails the check stays blocked, including across a manager
// restart, until an operator restores it and verifies again.
func TestDamagedWorldBlocksStartUntilVerified(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.StopTimeout = 500 * time.Millisecond
	game := fakeGame(t, ignoresSIGTERM)
	world := writeWorld(t, game)
	createServer(t, c, "a", game, true)
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	// Damage the world while the game runs, as a failing disk would.
	if err := os.Remove(world + "-wal"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(world, []byte(strings.Repeat("not a sqlite page ", 400)), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "damage to be reported", func() bool { return countEvents(c.Events, "a", events.DatabaseCorrupt) == 1 })
	waitFor(t, 5*time.Second, "server to settle", func() bool { return c.Lifecycle("a").State == "recovery_required" })
	if err := c.Start(ctx, "a"); err == nil || !strings.Contains(err.Error(), "needs recovery") {
		t.Fatalf("damaged world was allowed to start: %v", err)
	}

	// A fresh controller on the same database still refuses: the block is
	// stored, not remembered.
	restarted := New(c.Repo)
	if restarted.Lifecycle("a").State != "recovery_required" {
		t.Fatalf("block lost across manager restart: %+v", restarted.Lifecycle("a"))
	}

	// Verifying the damaged world does not lift the block.
	if err := c.Verify(ctx, "a"); err == nil {
		t.Fatal("verify accepted a damaged world")
	}
	// The operator restores a good world; verify lifts the block.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(world + suffix)
	}
	writeWorld(t, game)
	if err := c.Verify(ctx, "a"); err != nil {
		t.Fatalf("verify of a restored world: %v", err)
	}
	if c.Lifecycle("a").State != "idle" {
		t.Fatalf("still blocked after verify: %+v", c.Lifecycle("a"))
	}
}

func TestNormalStopIsNeitherKilledNorChecked(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.StopTimeout = 5 * time.Second
	game := fakeGame(t, "#!/bin/sh\nexec sleep 60\n")
	writeWorld(t, game)
	createServer(t, c, "a", game, true)
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "stop", func() bool { return c.Lifecycle("a").State == "idle" })
	if reason := killReason(c.Events, "a"); reason != "" {
		t.Fatalf("clean stop was killed: %q", reason)
	}
	if countEvents(c.Events, "a", events.DatabaseVerified) != 0 {
		t.Fatal("clean stop does not need a check")
	}
}

// A crash skipped the game's shutdown just like a kill, so the world is
// checked before the crash restart may open it.
func TestCrashedWorldIsCheckedBeforeRestart(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.CrashRestartLimit, c.CrashRestartDelay = 1, 10*time.Millisecond
	game := fakeGame(t, "#!/bin/sh\nexit 3\n")
	writeWorld(t, game)
	createServer(t, c, "a", game, true)
	if err := c.Start(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "crash handling", func() bool {
		return countEvents(c.Events, "a", events.ServerRestartAbandoned) == 1 && c.Lifecycle("a").State == "idle"
	})
	history := c.Events.Recent(0)
	// Newest first: each crash must be followed by its check, and the check
	// must precede the restart announcement.
	checked, crashed := 0, 0
	for i := len(history) - 1; i >= 0; i-- {
		switch history[i].Type {
		case "database.verified":
			checked++
		case "server.crashed":
			crashed++
			if checked != crashed {
				t.Fatalf("crash %d reported before its world was checked: %+v", crashed, history)
			}
		}
	}
	if checked != 2 {
		t.Fatalf("worlds checked %d times, want 2 (one per crash)", checked)
	}
}
