package ramstate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func TestLoadAndFlushKeepsSavedOnSSD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated Windows privileges")
	}
	root := t.TempDir()
	cfg := server.Config{InstallDir: filepath.Join(root, "game"), RamdiskEnabled: true, RamdiskPath: filepath.Join(root, "ram"), RamdiskSizeMB: 512}
	saved := filepath.Join(cfg.InstallDir, "ConanSandbox", "Saved")
	if err := os.MkdirAll(saved, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saved, activeDatabase), []byte("persistent"), 0o640); err != nil {
		t.Fatal(err)
	}
	manager := Manager{}
	if err := manager.Load(cfg); err != nil {
		t.Fatal(err)
	}
	layout := LayoutFor(cfg)
	info, err := os.Lstat(layout.PersistentSaved)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("Saved should remain normal directory: %v", err)
	}
	dbInfo, err := os.Lstat(filepath.Join(layout.PersistentSaved, activeDatabase))
	if err != nil || dbInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("active DB is not symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.PersistentSaved, "game_0_backup_1.db"), []byte("backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Flush(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(layout.PersistentSaved, "game_0_backup_1.db"))
	if err != nil || string(data) != "backup" {
		t.Fatalf("backup was not flushed: %v %q", err, data)
	}
}

func TestFindBackups(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "game_0_backup_1.db"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "game_0.db"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	summary, err := FindBackups(root)
	if err != nil || len(summary.Files) != 1 {
		t.Fatalf("summary: %#v, %v", summary, err)
	}
}
