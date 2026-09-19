package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyConanBackups(t *testing.T) {
	source, destination := t.TempDir(), filepath.Join(t.TempDir(), "backups")
	if err := os.WriteFile(filepath.Join(source, "game_backup_001.db"), []byte("backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "game.db"), []byte("active"), 0o640); err != nil {
		t.Fatal(err)
	}
	count, err := (Manager{}).CopyConanBackups(source, destination)
	if err != nil || count != 1 {
		t.Fatalf("copy: %v, %d", err, count)
	}
	if _, err := os.Stat(filepath.Join(destination, "game_backup_001.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "game.db")); !os.IsNotExist(err) {
		t.Fatalf("active db was copied")
	}
}
