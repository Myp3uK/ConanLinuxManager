package ramstate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/example/conan-manager/internal/server"
)

// PersistBind requires the session lease, all game descendants reaped and all
// binds unmounted. Only this successful RAM commit authorizes cleanup; an old
// archive or an absent marker alone never authorizes deleting RAM.
func (m Manager) PersistBind(ctx context.Context, cfg server.Config) error {
	if err := m.Recover(ctx, cfg, "ram"); err != nil {
		return fmt.Errorf("persist failed; RAM retained, inspect session marker/archive: %w", err)
	}
	dir := LayoutFor(cfg).RamDatabase
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("database persisted; RAM cleanup failed: %w", err)
	}
	// Validate the whole set before removing anything. Never recursively delete
	// unexpected files, directories or links left in the RAM directory.
	for _, entry := range entries {
		name := entry.Name()
		if name != activeDatabase && name != activeDatabase+"-wal" && name != activeDatabase+"-shm" {
			return fmt.Errorf("database persisted; unexpected RAM entry retained: %s", name)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("database persisted; RAM cleanup validation: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("database persisted; non-regular RAM entry retained: %s", name)
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("database persisted; RAM cleanup incomplete: %w", err)
		}
	}
	return nil
}

// LoadBind prepares a durable baseline and regular files for bindmount.Run.
// The offline caller owns exclusive locks and validates directories/tmpfs first.
// Unlike Load it never replaces Saved files with symlinks or drains backups.
func (m Manager) LoadBind(cfg server.Config) error {
	if !cfg.RamdiskEnabled {
		return fmt.Errorf("bind sessions require RAM enabled")
	}
	if err := Pending(cfg); err != nil {
		return err
	}
	layout := LayoutFor(cfg)
	// Never erase an old RAM directory implicitly, even without a marker.
	entries, err := os.ReadDir(layout.RamDatabase)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("bind RAM database directory must be empty")
	}
	if err := beginSession(cfg, layout); err != nil {
		return err
	}
	for _, suffix := range databaseSuffixes {
		name := activeDatabase + suffix
		source := filepath.Join(layout.PersistentSaved, name)
		target := filepath.Join(layout.RamDatabase, name)
		if err := copyIfExists(source, target); err != nil {
			return err
		}
		for _, path := range []string{source, target} {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if os.IsExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}
