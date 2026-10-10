package controller

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/ramstate"
	_ "modernc.org/sqlite"
)

func writeWALWorld(t *testing.T, path string, rows int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{"PRAGMA journal_mode=WAL", "CREATE TABLE actors(id INTEGER PRIMARY KEY)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < rows; i++ {
		if _, err := db.Exec("INSERT INTO actors DEFAULT VALUES"); err != nil {
			t.Fatal(err)
		}
	}
}

func walWorldRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
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

func TestBackupRestoreRoundTrip(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.BackupDir = t.TempDir()
	c.BackupKeep = 3
	install := t.TempDir()
	createServer(t, c, "a", install, false)
	ctx := context.Background()
	cfg, _ := c.Repo.Get(ctx, "a")
	world := ramstate.LayoutFor(cfg).GameDatabase
	writeWALWorld(t, world, 5)

	good, err := c.takeBackup(ctx, cfg, ReasonManual)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(good.Path) != filepath.Join(c.BackupDir, "a") || good.SHA256 == "" {
		t.Fatalf("backup %+v", good)
	}

	// The world is lost; a failed check has blocked the server.
	writeWALWorld(t, world, 1)
	if err := c.Repo.SetRecovery(ctx, "a", "integrity check failed"); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreBackup(ctx, "a", good.ID); err != nil {
		t.Fatal(err)
	}
	if n := walWorldRows(t, world); n != 5 {
		t.Fatalf("restored world has %d rows", n)
	}
	if cfg, _ := c.Repo.Get(ctx, "a"); cfg.RecoveryReason != "" {
		t.Fatal("a verified restore did not lift the recovery block")
	}
	list, err := c.Backups(ctx, "a")
	if err != nil || len(list) != 2 || list[0].Reason != ReasonPreRestore || !list[0].Exists {
		t.Fatalf("the replaced world was not kept: %+v %v", list, err)
	}
	if countEvents(c.Events, "a", events.BackupRestored) != 1 {
		t.Fatal("restore not reported")
	}

	// Only the newest BackupKeep remain, on disk and in the list.
	for i := 0; i < 3; i++ {
		if _, err := c.takeBackup(ctx, cfg, ReasonManual); err != nil {
			t.Fatal(err)
		}
	}
	list, _ = c.Backups(ctx, "a")
	files, _ := os.ReadDir(filepath.Join(c.BackupDir, "a"))
	if len(list) != 3 || len(files) != 3 {
		t.Fatalf("kept %d records and %d files, want 3", len(list), len(files))
	}
	if err := c.DeleteBackup(ctx, "a", list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(list[0].Path); !os.IsNotExist(err) {
		t.Fatal("deleted backup left its file")
	}
}

func TestRestoreRefusesATamperedBackup(t *testing.T) {
	c, _ := testController(t)
	c.BackupDir = t.TempDir()
	createServer(t, c, "a", t.TempDir(), false)
	ctx := context.Background()
	cfg, _ := c.Repo.Get(ctx, "a")
	world := ramstate.LayoutFor(cfg).GameDatabase
	writeWALWorld(t, world, 2)
	b, err := c.takeBackup(ctx, cfg, ReasonManual)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(b.Path, os.O_WRONLY|os.O_APPEND, 0)
	f.Write([]byte("junk"))
	f.Close()
	if err := c.RestoreBackup(ctx, "a", b.ID); err == nil {
		t.Fatal("tampered backup restored")
	}
	if n := walWorldRows(t, world); n != 2 {
		t.Fatal("world changed by a refused restore")
	}
}

func TestBackupReasonIsValidated(t *testing.T) {
	c, _ := testController(t)
	createServer(t, c, "a", t.TempDir(), false)
	if _, err := c.BackupJob(context.Background(), "a", "../../etc"); err == nil {
		t.Fatal("path in reason accepted")
	}
}
