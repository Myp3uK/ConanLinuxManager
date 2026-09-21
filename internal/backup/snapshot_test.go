package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func count(t *testing.T, path string) int {
	t.Helper()
	db, err := open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM actors").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// gameDB is a world in WAL mode whose last commits are still only in the WAL,
// as the game's database is while it runs.
func gameDB(t *testing.T, rows int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "game_0.db")
	db, err := open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE actors(id INTEGER PRIMARY KEY, name TEXT)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < rows; i++ {
		if _, err := db.Exec("INSERT INTO actors(name) VALUES('Конан')"); err != nil {
			t.Fatal(err)
		}
	}
	return path, db
}

func TestSnapshotOfALiveDatabaseHoldsItsCommits(t *testing.T) {
	source, live := gameDB(t, 50)
	if info, err := os.Stat(source + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("test needs commits in the WAL: %v", err)
	}
	target := filepath.Join(t.TempDir(), "backups", "snap.db")
	snap, err := TakeSnapshot(context.Background(), source, target)
	if err != nil {
		t.Fatal(err)
	}
	if count(t, target) != 50 || snap.Size == 0 || len(snap.SHA256) != 64 {
		t.Fatalf("snapshot %+v", snap)
	}
	if _, err := os.Stat(target + ".partial"); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
	// The game goes on writing after the copy; the copy does not change.
	if _, err := live.Exec("INSERT INTO actors(name) VALUES('Валерия')"); err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshot(target, snap.SHA256); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreReplacesTheWorldAndItsWAL(t *testing.T) {
	source, live := gameDB(t, 3)
	target := filepath.Join(t.TempDir(), "snap.db")
	if _, err := TakeSnapshot(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := live.Exec("INSERT INTO actors(name) VALUES('later')"); err != nil {
			t.Fatal(err)
		}
	}
	live.Close() // the game is stopped for a restore
	if err := RestoreSnapshot(target, source); err != nil {
		t.Fatal(err)
	}
	if n := count(t, source); n != 3 {
		t.Fatalf("restored world has %d rows, want 3", n)
	}
}

func TestSnapshotOfAMissingDatabaseFails(t *testing.T) {
	if _, err := TakeSnapshot(context.Background(), filepath.Join(t.TempDir(), "none.db"), filepath.Join(t.TempDir(), "x.db")); err == nil {
		t.Fatal("snapshot of nothing succeeded")
	}
}

func TestVerifySnapshotDetectsChanges(t *testing.T) {
	source, _ := gameDB(t, 1)
	target := filepath.Join(t.TempDir(), "snap.db")
	snap, err := TakeSnapshot(context.Background(), source, target)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0)
	f.Write([]byte("x"))
	f.Close()
	if VerifySnapshot(target, snap.SHA256) == nil {
		t.Fatal("modified copy passed")
	}
}
