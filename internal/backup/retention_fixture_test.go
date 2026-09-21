package backup

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// TestRetentionOnRealConanFiles runs the retention pass against copies of an
// actual Conan Saved directory. It is skipped unless CONAN_SAVED_FIXTURE points
// at one, so the unit suite stays hermetic while the acceptance host can verify
// the real filenames, sizes and modification times.
//
//	CONAN_SAVED_FIXTURE=/var/lib/conan-manager/servers/ue5-test/game/ConanSandbox/Saved go test ./internal/backup/ -run RealConan -v
func TestRetentionOnRealConanFiles(t *testing.T) {
	source := os.Getenv("CONAN_SAVED_FIXTURE")
	if source == "" {
		t.Skip("set CONAN_SAVED_FIXTURE to a real Conan Saved directory")
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	// Copy the real files into a disposable tree; the fixture is never modified.
	work := t.TempDir()
	var copied []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		// A symlinked active database must survive untouched.
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.Symlink("/nonexistent/game_0.db", filepath.Join(work, entry.Name())); err != nil {
				t.Fatal(err)
			}
			copied = append(copied, entry.Name())
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatalf("copy %s: %v", entry.Name(), err)
		}
		target := filepath.Join(work, entry.Name())
		if err := os.WriteFile(target, data, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		copied = append(copied, entry.Name())
	}
	t.Logf("fixture copied: %v", copied)

	// Add more backups so the set exceeds any plausible retention limit, with
	// distinct modification times so the newest ones are unambiguous.
	base := time.Now().Add(-24 * time.Hour)
	var generated []string
	for i := 4; i <= 10; i++ {
		name := "game_0_backup_" + strconv.Itoa(i) + ".db"
		path := filepath.Join(work, name)
		if err := os.WriteFile(path, []byte("generated backup"), 0o640); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		generated = append(generated, name)
	}

	before := backupNames(t, work)
	if len(before) < 4 {
		t.Fatalf("fixture has too few backups to exercise retention: %v", before)
	}
	keep := 3
	removed, err := (Manager{}).PruneConanBackups(work, keep)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	after := backupNames(t, work)
	if len(after) != keep {
		t.Fatalf("kept %d backups, want %d: %v", len(after), keep, after)
	}
	if removed != len(before)-keep {
		t.Fatalf("removed %d, want %d", removed, len(before)-keep)
	}

	// The active database, its sidecars and every non-backup file must remain.
	for _, name := range []string{"game_0.db", "game_0.db-wal", "game_0.db-shm", "serveruid.txt"} {
		original := filepath.Join(source, name)
		if _, err := os.Lstat(original); err != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(work, name)); err != nil {
			t.Fatalf("retention removed %s: %v", name, err)
		}
	}

	// A second pass over an already-trimmed directory changes nothing.
	removed, err = (Manager{}).PruneConanBackups(work, keep)
	if err != nil || removed != 0 {
		t.Fatalf("second pass removed %d: %v", removed, err)
	}
	t.Logf("retention kept newest %d of %d backups: %v", keep, len(before), after)
}

func backupNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, entry := range entries {
		if !entry.IsDir() && isConanBackup(entry.Name()) {
			result = append(result, entry.Name())
		}
	}
	sort.Strings(result)
	return result
}
