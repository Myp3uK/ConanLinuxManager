package ramstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/Myp3uK/ConanLinuxManager/internal/recoveryalert"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
	_ "modernc.org/sqlite"
)

// Recover requires exclusive offline access: manager and game must be stopped.
// It never opens an original database with SQLite, only a disposable copy.
func (m Manager) Recover(ctx context.Context, cfg server.Config, source string) error {
	if source != "ram" && source != "baseline" {
		return fmt.Errorf("source must be ram or baseline")
	}
	root := SessionPath(cfg)
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid session directory")
	}
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest sessionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	layout := LayoutFor(cfg)
	if manifest.Version != 1 || manifest.ServerID != cfg.ID || manifest.RAMDatabase != layout.RamDatabase {
		return fmt.Errorf("session manifest does not match server configuration")
	}
	origin := layout.RamDatabase
	if source == "baseline" {
		origin = filepath.Join(root, "baseline")
	}
	stage, err := os.MkdirTemp(root, "recover-")
	if err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal"} {
		name := activeDatabase + suffix
		path := filepath.Join(origin, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && suffix != "" {
			if source == "baseline" {
				for _, recorded := range manifest.Files {
					if recorded == name {
						return fmt.Errorf("baseline file missing: %s", name)
					}
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("recovery source is not regular: %s", path)
		}
		if suffix == "" && info.Size() < 100 {
			return fmt.Errorf("recovery database is empty or truncated: %s", path)
		}
		if err := copyFile(path, filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	// SHM is a transient index; SQLite rebuilds it from the copied WAL.
	if err := validateAndCheckpoint(ctx, filepath.Join(stage, activeDatabase)); err != nil {
		return err
	}
	// Persist the validated file before touching Saved. Recovery stages remain
	// available for diagnosis if any following filesystem operation fails.
	f, err := os.OpenFile(filepath.Join(stage, activeDatabase), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := syncDirectory(stage); err != nil {
		return err
	}
	for _, suffix := range databaseSuffixes {
		target := layout.GameDatabase + suffix
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
		if suffix == "" {
			if err := copyFile(filepath.Join(stage, activeDatabase), target); err != nil {
				return err
			}
		}
	}
	if err := syncDirectory(layout.PersistentSaved); err != nil {
		return err
	}
	if err := restoreOwner(layout.GameDatabase, layout.PersistentSaved); err != nil {
		return err
	}
	// Unique archive retains baseline and all attempts, including the validated
	// result. Removing the active marker is the final commit operation.
	archive := root + ".recovered-" + filepath.Base(stage)
	if err := os.Rename(root, archive); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	_ = recoveryalert.Clear(cfg)
	return nil
}

func validateAndCheckpoint(ctx context.Context, path string) error {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("SQLite integrity check: %w", err)
	}
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			rows.Close()
			return err
		}
		if result != "ok" {
			rows.Close()
			return fmt.Errorf("SQLite integrity check: %s", result)
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("SQLite integrity check returned no single OK result")
	}
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("SQLite checkpoint busy")
	}
	return db.Close()
}
