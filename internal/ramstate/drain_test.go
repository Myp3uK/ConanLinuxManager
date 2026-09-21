package ramstate

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/server"
)

func drainFixture(t *testing.T) (server.Config, Layout) {
	t.Helper()
	cfg := server.Config{ID: "one", InstallDir: t.TempDir(), RamdiskEnabled: true, RamdiskPath: t.TempDir()}
	layout := LayoutFor(cfg)
	if err := os.MkdirAll(layout.RamDatabase, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.PersistentSaved, 0o750); err != nil {
		t.Fatal(err)
	}
	return cfg, layout
}

// settled writes a file and backdates it past the settle window.
func settled(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestDrainMovesSettledBackupToSSD(t *testing.T) {
	cfg, layout := drainFixture(t)
	source := filepath.Join(layout.RamDatabase, "game_0_backup_1.db")
	settled(t, source, "backup payload")
	written, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := (Manager{}).DrainBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := "game_0_backup_1_" + written.ModTime().UTC().Format("20060102T150405Z") + ".db"
	if len(summary.Files) != 1 || summary.Files[0] != want {
		t.Fatalf("summary: %+v, want %s", summary, want)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("backup left on the RAM disk: %v", err)
	}
	target := filepath.Join(layout.PersistentSaved, want)
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "backup payload" {
		t.Fatalf("SSD copy: %q %v", content, err)
	}
	// Retention orders by modification time, so the copy must carry the time
	// the game finished writing it rather than the time the drain ran.
	copied, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !copied.ModTime().Truncate(time.Second).Equal(written.ModTime().Truncate(time.Second)) {
		t.Errorf("modification time not preserved: %v vs %v", copied.ModTime(), written.ModTime())
	}
}

// The drain removes each backup before the game can rotate it, so the game
// always writes the same name, and draining under that name collapsed the whole
// history into one file. Recovery depth
// is the entire point of the RAM-disk drain, so it is asserted here.
func TestDrainKeepsEveryCycleInsteadOfOverwritingOne(t *testing.T) {
	cfg, layout := drainFixture(t)
	source := filepath.Join(layout.RamDatabase, "game_0_backup_1.db")
	base := time.Now().Add(-time.Hour)
	for cycle := 0; cycle < 3; cycle++ {
		if err := os.WriteFile(source, []byte(fmt.Sprintf("cycle %d", cycle)), 0o640); err != nil {
			t.Fatal(err)
		}
		when := base.Add(time.Duration(cycle) * 3 * time.Minute)
		if err := os.Chtimes(source, when, when); err != nil {
			t.Fatal(err)
		}
		if _, err := (Manager{}).DrainBackups(cfg); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}
	entries, err := filepath.Glob(filepath.Join(layout.PersistentSaved, "game_0_backup_*.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("history is %d cycles deep, want 3: %v", len(entries), entries)
	}

	// Draining the same untouched file again must not produce a duplicate.
	if err := os.WriteFile(source, []byte("cycle 2"), 0o640); err != nil {
		t.Fatal(err)
	}
	when := base.Add(2 * 3 * time.Minute)
	if err := os.Chtimes(source, when, when); err != nil {
		t.Fatal(err)
	}
	if _, err := (Manager{}).DrainBackups(cfg); err != nil {
		t.Fatal(err)
	}
	entries, err = filepath.Glob(filepath.Join(layout.PersistentSaved, "game_0_backup_*.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("re-draining an identical file duplicated it: %v", entries)
	}
}

// A backup the game is still writing must not be copied and deleted half-way:
// that would destroy the only complete copy. On a large save the write takes
// seconds, so this window is real.
func TestDrainSkipsBackupStillBeingWritten(t *testing.T) {
	cfg, layout := drainFixture(t)
	fresh := filepath.Join(layout.RamDatabase, "game_0_backup_1.db")
	if err := os.WriteFile(fresh, []byte("partial"), 0o640); err != nil {
		t.Fatal(err)
	}

	summary, err := (Manager{}).DrainBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Files) != 0 {
		t.Fatalf("moved a file that was still being written: %+v", summary)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("source removed while still being written: %v", err)
	}

	// Once it stops changing, the next pass moves it.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(fresh, old, old); err != nil {
		t.Fatal(err)
	}
	summary, err = (Manager{}).DrainBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Files) != 1 {
		t.Fatalf("settled backup was not drained: %+v", summary)
	}
}

// The active database and its sidecars belong in RAM and must never be drained.
func TestDrainLeavesActiveDatabaseInRAM(t *testing.T) {
	cfg, layout := drainFixture(t)
	for _, suffix := range databaseSuffixes {
		settled(t, filepath.Join(layout.RamDatabase, activeDatabase+suffix), "active")
	}
	summary, err := (Manager{}).DrainBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Files) != 0 {
		t.Fatalf("drained the active database: %+v", summary)
	}
	for _, suffix := range databaseSuffixes {
		if _, err := os.Stat(filepath.Join(layout.RamDatabase, activeDatabase+suffix)); err != nil {
			t.Fatalf("active file %q removed: %v", activeDatabase+suffix, err)
		}
	}
}

func TestDrainHandlesSeveralBackups(t *testing.T) {
	cfg, layout := drainFixture(t)
	for _, name := range []string{"game_0_backup_1.db", "game_0_backup_2.db", "game_0_backup_3.db"} {
		settled(t, filepath.Join(layout.RamDatabase, name), name)
	}
	summary, err := (Manager{}).DrainBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Files) != 3 {
		t.Fatalf("drained %d of 3: %+v", len(summary.Files), summary)
	}
	entries, err := os.ReadDir(layout.RamDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("RAM disk still holds %d entries", len(entries))
	}
}
