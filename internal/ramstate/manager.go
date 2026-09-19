package ramstate

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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

// DrainBackups moves any backups SQLite created next to the resolved RAM DB
// back to the SSD Saved directory. Symlink resolution can make SQLite place
// backup siblings in the RAM directory, so this runs periodically while the
// server is active.
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
	for _, entry := range entries {
		if entry.IsDir() || !isBackupName(entry.Name()) {
			continue
		}
		source := filepath.Join(layout.RamDatabase, entry.Name())
		target := filepath.Join(layout.PersistentSaved, entry.Name())
		if err := copyFile(source, target); err != nil {
			return result, err
		}
		if err := os.Remove(source); err != nil {
			return result, err
		}
		result.Files = append(result.Files, entry.Name())
	}
	return result, nil
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
