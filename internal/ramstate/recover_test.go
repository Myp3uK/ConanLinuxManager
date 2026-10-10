package ramstate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

func TestRecoveryChecksCopyAndCommitsWAL(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux RAM lifecycle")
	}
	for _, source := range []string{"ram", "baseline", "corrupt"} {
		t.Run(source, func(t *testing.T) {
			cfg := server.Config{ID: "a", InstallDir: filepath.Join(t.TempDir(), "game"), RamdiskPath: t.TempDir(), RamdiskEnabled: true}
			layout := LayoutFor(cfg)
			if err := os.MkdirAll(layout.PersistentSaved, 0750); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", layout.GameDatabase)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE test (value TEXT); INSERT INTO test VALUES ('baseline')"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			m := Manager{}
			if err := m.Load(cfg); err != nil {
				t.Fatal(err)
			}
			if source == "ram" {
				db, err := sql.Open("sqlite", filepath.Join(layout.RamDatabase, activeDatabase))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE test SET value='ram'"); err != nil {
					t.Fatal(err)
				}
				// Keep connection open to retain committed WAL in this isolated fixture.
			}
			if source == "baseline" {
				if err := os.RemoveAll(layout.RamDatabase); err != nil {
					t.Fatal(err)
				}
			}
			if source == "corrupt" {
				if err := os.WriteFile(filepath.Join(layout.RamDatabase, activeDatabase), []byte("not sqlite"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := m.Recover(context.Background(), cfg, "ram"); err == nil {
					t.Fatal("corruption accepted")
				}
				if err := Pending(cfg); err == nil {
					t.Fatal("marker lost")
				}
				if _, err := os.Readlink(layout.GameDatabase); err != nil {
					t.Fatal("Saved changed on failed validation", err)
				}
				return
			}
			if err := m.Recover(context.Background(), cfg, source); err != nil {
				t.Fatal(err)
			}
			if err := Pending(cfg); err != nil {
				t.Fatal(err)
			}
			result, err := sql.Open("sqlite", layout.GameDatabase)
			if err != nil {
				t.Fatal(err)
			}
			defer result.Close()
			var value string
			if err := result.QueryRow("SELECT value FROM test").Scan(&value); err != nil {
				t.Fatal(err)
			}
			if value != source {
				t.Fatalf("got %s want %s", value, source)
			}
		})
	}
}
