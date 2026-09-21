package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// Every pooled connection must carry the pragmas, not only the first one.
func TestEveryConnectionHasForeignKeysAndBusyTimeout(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var conns []interface{ Close() error }
	for i := 0; i < 4; i++ {
		conn, err := s.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		var fk, timeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || timeout < 5000 {
			t.Fatalf("connection %d: foreign_keys=%d busy_timeout=%d", i, fk, timeout)
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// Concurrent writers must wait for each other instead of failing.
func TestConcurrentWritesDoNotFailWithBusy(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`CREATE TABLE probe (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				tx, err := s.DB().Begin()
				if err != nil {
					errs <- err
					return
				}
				var n int
				_ = tx.QueryRow(`SELECT count(*) FROM probe`).Scan(&n)
				if _, err := tx.Exec(`INSERT INTO probe(v) VALUES ('x')`); err != nil {
					_ = tx.Rollback()
					errs <- err
					return
				}
				if err := tx.Commit(); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
}
