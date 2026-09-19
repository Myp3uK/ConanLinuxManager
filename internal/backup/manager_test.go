package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestPruneConanBackupsKeepsNewest(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 3; i++ {
		path := filepath.Join(root, fmt.Sprintf("game_backup_%03d.db", i))
		if err := os.WriteFile(path, []byte("backup"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(int64(i), 0), time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := (Manager{}).PruneConanBackups(root, 2)
	if err != nil || removed != 1 {
		t.Fatalf("prune: %v, %d", err, removed)
	}
	if _, err := os.Stat(filepath.Join(root, "game_backup_001.db")); !os.IsNotExist(err) {
		t.Fatalf("oldest backup was retained")
	}
}
