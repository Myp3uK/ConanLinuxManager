package ramstate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/example/conan-manager/internal/server"
)

func TestRepeatedLoadPreservesLiveDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux symlink lifecycle test")
	}
	cfg := server.Config{InstallDir: filepath.Join(t.TempDir(), "game"), RamdiskPath: t.TempDir(), RamdiskEnabled: true}
	layout := LayoutFor(cfg)
	if err := os.MkdirAll(layout.PersistentSaved, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.GameDatabase, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manager{}
	if err := m.Load(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.GameDatabase, []byte("new live data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(cfg); err == nil {
		t.Fatal("repeated load accepted")
	}
	data, err := os.ReadFile(layout.GameDatabase)
	if err != nil || string(data) != "new live data" {
		t.Fatalf("live data lost: %q, %v", data, err)
	}
	if _, err := m.Flush(cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(cfg); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(layout.GameDatabase)
	if err != nil || string(data) != "new live data" {
		t.Fatalf("restart lost data: %q, %v", data, err)
	}
}

func TestLostRAMKeepsBaselineAndBlocksRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux symlink lifecycle test")
	}
	cfg := server.Config{InstallDir: filepath.Join(t.TempDir(), "game"), RamdiskPath: t.TempDir(), RamdiskEnabled: true}
	layout := LayoutFor(cfg)
	if err := os.MkdirAll(layout.PersistentSaved, 0750); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range databaseSuffixes {
		if err := os.WriteFile(layout.GameDatabase+suffix, []byte("baseline"+suffix), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := Manager{}
	if err := m.Load(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(layout.RamDatabase); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Flush(cfg); err == nil {
		t.Fatal("flush accepted missing RAM database")
	}
	if err := m.Load(cfg); err == nil {
		t.Fatal("restart accepted lost RAM")
	}
	for _, suffix := range databaseSuffixes {
		data, err := os.ReadFile(filepath.Join(SessionPath(cfg), "baseline", activeDatabase+suffix))
		if err != nil || string(data) != "baseline"+suffix {
			t.Fatalf("baseline lost: %q %v", data, err)
		}
	}
}

func TestFlushRetainsMarkerOnFailureAndRemovesStaleWAL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux symlink lifecycle test")
	}
	cfg := server.Config{InstallDir: filepath.Join(t.TempDir(), "game"), RamdiskPath: t.TempDir(), RamdiskEnabled: true}
	layout := LayoutFor(cfg)
	if err := os.MkdirAll(layout.PersistentSaved, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.GameDatabase, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manager{}
	if err := m.Load(cfg); err != nil {
		t.Fatal(err)
	}
	// An invalid sidecar must fail staging before any SSD link is replaced.
	ramWAL := filepath.Join(layout.RamDatabase, activeDatabase+"-wal")
	if err := os.Mkdir(ramWAL, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Flush(cfg); err == nil {
		t.Fatal("invalid sidecar accepted")
	}
	if err := Pending(cfg); err == nil {
		t.Fatal("marker disappeared")
	}
	if _, err := os.Readlink(layout.GameDatabase); err != nil {
		t.Fatal("link changed before staging succeeded", err)
	}
	if err := os.Remove(ramWAL); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(layout.GameDatabase + "-wal"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.GameDatabase+"-wal", []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Flush(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Pending(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(layout.GameDatabase + "-wal"); !os.IsNotExist(err) {
		t.Fatal("stale WAL remains", err)
	}
}
