package ramstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func releaseFixture(t *testing.T) (server.Config, Layout) {
	t.Helper()
	install := t.TempDir()
	ram := t.TempDir()
	cfg := server.Config{ID: "one", InstallDir: install, RamdiskEnabled: true, RamdiskPath: ram}
	layout := LayoutFor(cfg)
	if err := os.MkdirAll(layout.RamDatabase, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.PersistentSaved, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range databaseSuffixes {
		path := filepath.Join(layout.RamDatabase, activeDatabase+suffix)
		if err := os.WriteFile(path, []byte("payload"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, layout
}

func TestReleaseRAMFreesTheActiveDatabase(t *testing.T) {
	cfg, layout := releaseFixture(t)
	if err := (Manager{}).ReleaseRAM(cfg); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(layout.RamDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("RAM disk still holds %d entries", len(entries))
	}
}

// An unfinished session means RAM is still the only copy of the database.
func TestReleaseRAMRefusesWhileSessionMarkerExists(t *testing.T) {
	cfg, layout := releaseFixture(t)
	if err := os.MkdirAll(SessionPath(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (Manager{}).ReleaseRAM(cfg); err == nil {
		t.Fatal("released RAM while the session was unfinished")
	}
	entries, err := os.ReadDir(layout.RamDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(databaseSuffixes) {
		t.Fatalf("RAM files were removed anyway: %d left", len(entries))
	}
}

// Anything the manager does not recognise blocks the whole cleanup, so an
// unexpected file is never deleted as a side effect.
func TestReleaseRAMRefusesUnexpectedEntries(t *testing.T) {
	cfg, layout := releaseFixture(t)
	stray := filepath.Join(layout.RamDatabase, "game_0_backup_1.db")
	if err := os.WriteFile(stray, []byte("backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := (Manager{}).ReleaseRAM(cfg); err == nil {
		t.Fatal("released RAM with an unexpected entry present")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("unexpected entry was deleted: %v", err)
	}
	for _, suffix := range databaseSuffixes {
		if _, err := os.Stat(filepath.Join(layout.RamDatabase, activeDatabase+suffix)); err != nil {
			t.Fatalf("active database removed despite refusal: %v", err)
		}
	}
}

func TestReleaseRAMIgnoresDisabledAndMissing(t *testing.T) {
	disabled := server.Config{ID: "one", InstallDir: t.TempDir(), RamdiskEnabled: false}
	if err := (Manager{}).ReleaseRAM(disabled); err != nil {
		t.Fatalf("disabled RAM reported an error: %v", err)
	}
	missing := server.Config{ID: "one", InstallDir: t.TempDir(), RamdiskEnabled: true, RamdiskPath: filepath.Join(t.TempDir(), "absent")}
	if err := (Manager{}).ReleaseRAM(missing); err != nil {
		t.Fatalf("missing RAM directory reported an error: %v", err)
	}
}
