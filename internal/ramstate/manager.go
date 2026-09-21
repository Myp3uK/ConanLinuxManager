package ramstate

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/server"
)

const activeDatabase = "game_0.db"

// Manager keeps only the active SQLite database and its WAL/SHM sidecars in
// RAM. Backups, logs, configs and all other Saved files stay on SSD.
type Manager struct{}

type Layout struct {
	PersistentSaved string
	RamDatabase     string
	GameDatabase    string
}

func LayoutFor(cfg server.Config) Layout {
	saved := filepath.Join(cfg.InstallDir, "ConanSandbox", "Saved")
	return Layout{
		PersistentSaved: saved,
		RamDatabase:     filepath.Join(cfg.RamdiskPath, "database"),
		GameDatabase:    filepath.Join(saved, activeDatabase),
	}
}

func (m Manager) Prepare(cfg server.Config) (Layout, error) {
	if !cfg.RamdiskEnabled {
		return Layout{}, nil
	}
	layout := LayoutFor(cfg)
	if !filepath.IsAbs(layout.PersistentSaved) || !filepath.IsAbs(layout.RamDatabase) {
		return Layout{}, fmt.Errorf("saved paths must be absolute")
	}
	if err := m.migrateWholeSavedIfNeeded(layout); err != nil {
		return Layout{}, err
	}
	if err := os.MkdirAll(layout.PersistentSaved, 0o750); err != nil {
		return Layout{}, fmt.Errorf("create Saved: %w", err)
	}
	if err := os.MkdirAll(layout.RamDatabase, 0o750); err != nil {
		return Layout{}, fmt.Errorf("create RAM database directory: %w", err)
	}
	return layout, nil
}

// Migrate the previous prototype layout, where the entire Saved directory was
// a symlink. Existing RAM data is copied back before restoring a normal Saved.
func (m Manager) migrateWholeSavedIfNeeded(layout Layout) error {
	info, err := os.Lstat(layout.PersistentSaved)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	target, err := os.Readlink(layout.PersistentSaved)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(layout.PersistentSaved), target)
	}
	oldPersistent := filepath.Join(filepath.Dir(layout.PersistentSaved), "Saved.persistent")
	if _, err := os.Stat(oldPersistent); os.IsNotExist(err) {
		if err := os.MkdirAll(oldPersistent, 0o750); err != nil {
			return err
		}
	}
	if err := syncTree(target, oldPersistent); err != nil {
		return fmt.Errorf("migrate old RAM Saved: %w", err)
	}
	if err := os.Remove(layout.PersistentSaved); err != nil {
		return err
	}
	if err := os.Rename(oldPersistent, layout.PersistentSaved); err != nil {
		return err
	}
	return nil
}

func (m Manager) Load(cfg server.Config) error {
	// Check before Prepare/cleanup: after a crash the symlink may be the only
	// reference to the latest database. Never erase its target on another start.
	if cfg.RamdiskEnabled {
		if err := Pending(cfg); err != nil {
			return err
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			path := LayoutFor(cfg).GameDatabase + suffix
			info, err := os.Lstat(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("RAM database is still linked at %s: stop and persist or recover it before starting", path)
			}
		}
	}
	layout, err := m.Prepare(cfg)
	if err != nil || layout.PersistentSaved == "" {
		return err
	}
	if err := beginSession(cfg, layout); err != nil {
		return err
	}
	if err := clearDirectory(layout.RamDatabase); err != nil {
		return err
	}
	legacyRam := filepath.Join(cfg.RamdiskPath, "ConanSandbox")
	if _, err := os.Stat(legacyRam); err == nil {
		if err := os.RemoveAll(legacyRam); err != nil {
			return fmt.Errorf("remove legacy RAM Saved: %w", err)
		}
	}
	if err := copyIfExists(layout.GameDatabase, filepath.Join(layout.RamDatabase, activeDatabase)); err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		persistent := filepath.Join(layout.PersistentSaved, activeDatabase+suffix)
		ram := filepath.Join(layout.RamDatabase, activeDatabase+suffix)
		if suffix != "" {
			if err := copyIfExists(persistent, ram); err != nil {
				return err
			}
		}
		if _, err := os.Lstat(persistent); err == nil {
			if err := os.Remove(persistent); err != nil {
				return err
			}
		}
		if err := os.Symlink(ram, persistent); err != nil {
			return fmt.Errorf("link active database %s: %w", suffix, err)
		}
	}
	return nil
}

func clearDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) Flush(cfg server.Config) (BackupSummary, error) {
	layout, err := m.Prepare(cfg)
	if err != nil || layout.PersistentSaved == "" {
		return BackupSummary{}, err
	}
	if err := flushSession(cfg, layout); err != nil {
		return BackupSummary{}, err
	}
	return FindBackups(layout.PersistentSaved)
}

// ReleaseRAM frees the RAM disk after a verified flush. A stopped server must
// not keep holding its database in tmpfs: on a large save that is hundreds of
// megabytes of RAM reserved for a server that is not running.
//
// It is deliberately separate from Flush. The data is already persisted by the
// time this runs, so a refusal here is a warning, never a recovery condition.
func (m Manager) ReleaseRAM(cfg server.Config) error {
	if !cfg.RamdiskEnabled {
		return nil
	}
	layout := LayoutFor(cfg)
	// Only a completed session authorizes deleting RAM. An in-progress marker
	// means the flush did not finish and RAM is still the only copy.
	if _, err := os.Lstat(SessionPath(cfg)); err == nil {
		return fmt.Errorf("RAM retained: session marker is still present")
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(layout.RamDatabase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Validate the whole set before removing anything, so an unexpected file or
	// a link is never deleted implicitly.
	for _, entry := range entries {
		name := entry.Name()
		if name != activeDatabase && name != activeDatabase+"-wal" && name != activeDatabase+"-shm" {
			return fmt.Errorf("RAM retained: unexpected entry %s", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("RAM retained: non-regular entry %s", name)
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(layout.RamDatabase, entry.Name())); err != nil {
			return fmt.Errorf("RAM cleanup incomplete: %w", err)
		}
	}
	return nil
}

// settleFor is how long a backup file must sit unchanged before it is treated
// as fully written. A multi hundred megabyte backup takes seconds to write, and
// moving it early would copy a truncated file and then delete the original.
const settleFor = 3 * time.Second

// DrainBackups moves any backups SQLite created next to the resolved RAM DB
// back to the SSD Saved directory. Symlink resolution can make SQLite place
// backup siblings in the RAM directory, so this runs frequently while the
// server is active: the RAM disk is sized for the active database alone.
//
// A file is only removed from RAM once it has been copied whole. Files that are
// still being written are left for a later call rather than moved half-copied.
func (m Manager) DrainBackups(cfg server.Config) (BackupSummary, error) {
	layout, err := m.Prepare(cfg)
	if err != nil || layout.PersistentSaved == "" {
		return BackupSummary{}, err
	}
	result := BackupSummary{Files: make([]string, 0)}
	entries, err := os.ReadDir(layout.RamDatabase)
	if err != nil {
		return result, err
	}
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() || !isBackupName(entry.Name()) {
			continue
		}
		source := filepath.Join(layout.RamDatabase, entry.Name())
		before, err := os.Lstat(source)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return result, err
		}
		if !before.Mode().IsRegular() {
			continue
		}
		// Still being written: wait for it to stop changing.
		if now.Sub(before.ModTime()) < settleFor {
			continue
		}
		target := filepath.Join(layout.PersistentSaved, drainedName(entry.Name(), before.ModTime()))
		if err := copyFile(source, target); err != nil {
			return result, err
		}
		// The copy carries the time the game finished writing it, so retention
		// orders the series by when each save was taken rather than by when the
		// drain happened to run.
		if err := os.Chtimes(target, before.ModTime(), before.ModTime()); err != nil {
			return result, err
		}
		// Re-check after the copy: if the file changed while it was being read,
		// the copy is not trustworthy and the original must survive.
		after, err := os.Lstat(source)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return result, err
		}
		if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			continue
		}
		if err := os.Remove(source); err != nil {
			return result, err
		}
		result.Files = append(result.Files, filepath.Base(target))
	}
	return result, nil
}

// drainedName makes each drained copy distinct. The drain takes each backup
// out of RAM before the game rotates it, so the game only ever writes
// game_0_backup_1.db, and draining under that name would leave exactly one file
// on disk: the history an operator
// restores from would be a single cycle deep, which is worse than not using a
// RAM disk at all. The timestamp is the game's own write time, so draining the
// same file twice produces the same name instead of a duplicate.
func drainedName(name string, modified time.Time) string {
	extension := filepath.Ext(name)
	return strings.TrimSuffix(name, extension) + "_" + modified.UTC().Format("20060102T150405Z") + extension
}

type BackupSummary struct {
	Files []string `json:"files"`
}

func FindBackups(root string) (BackupSummary, error) {
	result := BackupSummary{Files: make([]string, 0)}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isBackupName(entry.Name()) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result.Files = append(result.Files, relative)
		return nil
	})
	if err != nil {
		return BackupSummary{}, err
	}
	return result, nil
}

func isBackupName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "backup") && (strings.HasSuffix(lower, ".db") || strings.HasSuffix(lower, ".log"))
}

func copyIfExists(source, target string) error {
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return copyFile(source, target)
}

func syncTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		return copyFile(path, target)
	})
}

func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	temporary := target + ".tmp"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, target)
}
