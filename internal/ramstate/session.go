package ramstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/example/conan-manager/internal/server"
)

var databaseSuffixes = []string{"", "-wal", "-shm"}

// SessionPath is on SSD, outside Saved and outside tmpfs. Its existence alone
// blocks Load, including a crash before the manifest could be committed.
func SessionPath(cfg server.Config) string {
	return filepath.Join(cfg.InstallDir, "ConanSandbox", ".conan-manager-ram-session")
}

func Pending(cfg server.Config) error {
	path := SessionPath(cfg)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("unfinished RAM session at %s: recovery required before start, install or deletion", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

type sessionManifest struct {
	Version     int       `json:"version"`
	ServerID    string    `json:"server_id"`
	RAMDatabase string    `json:"ram_database"`
	CreatedAt   time.Time `json:"created_at"`
	Files       []string  `json:"baseline_files"`
}

func beginSession(cfg server.Config, layout Layout) error {
	root := SessionPath(cfg)
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	baseline := filepath.Join(root, "baseline")
	if err := os.Mkdir(baseline, 0700); err != nil {
		return err
	}
	manifest := sessionManifest{Version: 1, ServerID: cfg.ID, RAMDatabase: layout.RamDatabase, CreatedAt: time.Now().UTC(), Files: []string{}}
	for _, suffix := range databaseSuffixes {
		name := activeDatabase + suffix
		info, err := os.Lstat(filepath.Join(layout.PersistentSaved, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("baseline source %s is not a regular file", name)
		}
		if err := copyFile(filepath.Join(layout.PersistentSaved, name), filepath.Join(baseline, name)); err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, name)
	}
	if err := syncDirectory(baseline); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDirectory(root)
}

// The caller must have reaped the game process. Stage the complete file set
// before replacing any SSD file. Retain the marker and RAM on every failure.
func flushSession(cfg server.Config, layout Layout) error {
	root := SessionPath(cfg)
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("RAM session marker unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid RAM session directory")
	}
	if info, err := os.Lstat(filepath.Join(layout.RamDatabase, activeDatabase)); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("RAM database missing or invalid; preserve session for recovery")
	}
	stage, err := os.MkdirTemp(root, "flush-")
	if err != nil {
		return err
	}
	for _, suffix := range databaseSuffixes {
		source := filepath.Join(layout.RamDatabase, activeDatabase+suffix)
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid RAM file: %s", source)
		}
		if err := copyFile(source, filepath.Join(stage, activeDatabase+suffix)); err != nil {
			return err
		}
	}
	if err := syncDirectory(stage); err != nil {
		return err
	}
	for _, suffix := range databaseSuffixes {
		name := activeDatabase + suffix
		target := filepath.Join(layout.PersistentSaved, name)
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := copyIfExists(filepath.Join(stage, name), target); err != nil {
			return err
		}
	}
	if err := syncDirectory(layout.PersistentSaved); err != nil {
		return err
	}
	// Rename marks completion atomically; cleanup cannot expose a partial marker.
	completed := root + ".completed"
	if err := os.RemoveAll(completed); err != nil {
		return err
	}
	if err := os.Rename(root, completed); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	// Keep the completed baseline and staged snapshot until the next successful
	// flush. They are useful for operator inspection and never loaded implicitly.
	return nil
}
