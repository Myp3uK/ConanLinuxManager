package dbcheck

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func makeDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "game_0.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		"PRAGMA journal_mode=WAL",
		"CREATE TABLE actors (id INTEGER PRIMARY KEY, name TEXT, data BLOB)",
		"CREATE INDEX actors_name ON actors(name)",
	}
	for _, s := range statements {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2000; i++ {
		if _, err := db.Exec("INSERT INTO actors(name, data) VALUES (?, randomblob(200))", i); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestVerifyAcceptsAHealthyDatabase(t *testing.T) {
	if err := Verify(context.Background(), makeDatabase(t)); err != nil {
		t.Fatalf("healthy database rejected: %v", err)
	}
}

func TestVerifyReportsAMissingDatabase(t *testing.T) {
	err := Verify(context.Background(), filepath.Join(t.TempDir(), "game_0.db"))
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("got %v", err)
	}
}

// Overwriting pages in the middle of the file is the damage a torn write or a
// failing disk leaves behind.
func TestVerifyDetectsDamagedPages(t *testing.T) {
	path := makeDatabase(t)
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	garbage := make([]byte, 8192)
	for i := range garbage {
		garbage[i] = 0xA5
	}
	if _, err := f.WriteAt(garbage, 3*4096); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var corrupt *CorruptError
	if err := Verify(context.Background(), path); !errors.As(err, &corrupt) {
		t.Fatalf("damage not detected: %v", err)
	}
}

func TestVerifyRejectsAFileThatIsNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game_0.db")
	if err := os.WriteFile(path, []byte("this is not sqlite at all, just text that is long enough"), 0o640); err != nil {
		t.Fatal(err)
	}
	var corrupt *CorruptError
	if err := Verify(context.Background(), path); !errors.As(err, &corrupt) {
		t.Fatalf("non-database accepted: %v", err)
	}
}
