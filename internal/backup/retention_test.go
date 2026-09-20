package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionBoundaryCounts(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			root := t.TempDir()
			for i := 0; i < count; i++ {
				path := filepath.Join(root, fmt.Sprintf("game_0_backup_%d.db", i))
				if err := os.WriteFile(path, []byte("backup"), 0600); err != nil {
					t.Fatal(err)
				}
				when := time.Unix(int64(i+1), 0)
				if err := os.Chtimes(path, when, when); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"game.db", "game_0.db", "game_0.db-wal", "game_0.db-shm", "config.ini"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("protected"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			removed, err := (Manager{}).PruneConanBackups(root, 2)
			want := 0
			if count > 2 {
				want = count - 2
			}
			if err != nil || removed != want {
				t.Fatalf("removed %d want %d: %v", removed, want, err)
			}
			removed, err = (Manager{}).PruneConanBackups(root, 2)
			if err != nil || removed != 0 {
				t.Fatalf("second sweep: %d %v", removed, err)
			}
			for _, name := range []string{"game.db", "game_0.db", "game_0.db-wal", "game_0.db-shm", "config.ini"} {
				data, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(data) != "protected" {
					t.Fatalf("changed %s: %v", name, err)
				}
			}
		})
	}
}

func TestRetentionPreservesSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "external.db")
	if err := os.WriteFile(target, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "game_backup_link.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("game_backup_%d.db", i)), []byte("backup"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := (Manager{}).PruneConanBackups(root, 1)
	if err != nil || removed != 1 {
		t.Fatalf("prune: %d %v", removed, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "external" {
		t.Fatalf("external changed: %v", err)
	}
}
