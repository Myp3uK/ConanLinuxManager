package ramstate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/example/conan-manager/internal/server"
)

func TestPersistBindCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux RAM lifecycle")
	}
	for _, mode := range []string{"success", "corrupt", "unexpected", "symlink", "no-session"} {
		t.Run(mode, func(t *testing.T) {
			cfg := server.Config{ID: "cleanup", InstallDir: t.TempDir(), RamdiskPath: t.TempDir(), RamdiskEnabled: true}
			layout := LayoutFor(cfg)
			for _, dir := range []string{layout.PersistentSaved, layout.RamDatabase} {
				if err := os.MkdirAll(dir, 0750); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", layout.GameDatabase)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE probe(value INTEGER); INSERT INTO probe VALUES (42)"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			m := Manager{}
			if err := m.LoadBind(cfg); err != nil {
				t.Fatal(err)
			}
			ramDB := filepath.Join(layout.RamDatabase, activeDatabase)
			switch mode {
			case "corrupt":
				if err := os.WriteFile(ramDB, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unexpected":
				if err := os.WriteFile(filepath.Join(layout.RamDatabase, "keep"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				shm := ramDB + "-shm"
				if err := os.Remove(shm); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(layout.GameDatabase, shm); err != nil {
					t.Fatal(err)
				}
			case "no-session":
				if err := m.Recover(context.Background(), cfg, "ram"); err != nil {
					t.Fatal(err)
				}
			}
			err = m.PersistBind(context.Background(), cfg)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(layout.RamDatabase)
				if err != nil || len(entries) != 0 {
					t.Fatalf("cleanup: %v %v", entries, err)
				}
				if err := m.LoadBind(cfg); err != nil {
					t.Fatal("repeat load", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected refusal")
				}
				if _, err := os.Stat(ramDB); err != nil {
					t.Fatal("RAM lost", err)
				}
				if mode == "corrupt" && Pending(cfg) == nil {
					t.Fatal("marker lost")
				}
			}
		})
	}
}
