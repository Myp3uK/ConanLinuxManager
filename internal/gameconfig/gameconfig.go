// Package gameconfig writes the game's own INI settings that the manager owns.
//
// Conan reads these files once during startup and rewrites them during
// shutdown, so a value written while the server runs is silently discarded.
// Every function here therefore requires the server to be stopped; the
// controller's lifecycle lock provides that guarantee.
//
// Only settings that were verified to take effect are written. Measured on a
// Linux UE5 dedicated server:
//
//   - PeriodicBackupInterval works from Game.ini and overrides the value in
//     ServerSettings.ini. It has no lower bound worth avoiding: 120 seconds was
//     honoured exactly over a 17 minute run.
//   - MaxPeriodicBackupsToKeep is not written: retention is not "newest N".
//     libDreamworld carries a HistogramBackupScheduler with useTimedBackups, and
//     the number of copies kept depends on the interval (two at one to two
//     minutes, growing past fifteen at ten minutes). Until the rule is known,
//     retention depth is the backup package's job, not a guessed INI value.
//   - There is no setting for the backup directory. Backups are always written
//     next to the database. The BackupDirectory string in the binary belongs to
//     Epic's BuildPatchServices installer and is unrelated.
package gameconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

// gameModeSection holds the UPROPERTY(config) members of AConanGameMode. The
// name is the reflected class path, which survives Shipping builds.
const gameModeSection = "/Script/ConanSandbox.ConanGameMode"

const periodicBackupIntervalKey = "PeriodicBackupInterval"

// Settings are the values the manager enforces. A zero field leaves whatever
// the game itself wrote in place, so an operator who has not chosen a value
// keeps the stock behaviour.
type Settings struct {
	PeriodicBackupInterval time.Duration
}

// Managed reports whether the manager enforces anything at all. When it does
// not, the game keeps whatever it wrote itself.
func (s Settings) Managed() bool { return s.PeriodicBackupInterval > 0 }

// Path is the Game.ini the running server reads. The platform directory follows
// the runtime rather than the host: a Windows build under Wine or Proton reads
// WindowsServer even though the manager runs on Linux.
func Path(cfg server.Config) string {
	platform := "LinuxServer"
	if cfg.Runtime == server.RuntimeWine || cfg.Runtime == server.RuntimeProton {
		platform = "WindowsServer"
	}
	return filepath.Join(cfg.InstallDir, "ConanSandbox", "Saved", "Config", platform, "Game.ini")
}

// Apply merges the managed settings into Game.ini, leaving every other key,
// section and comment untouched. The game owns the rest of this file and
// rewrites it on shutdown, so anything not understood here must survive.
func Apply(cfg server.Config, settings Settings) error {
	if !settings.Managed() {
		return nil
	}
	path := Path(cfg)
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := merge(string(current), map[string]string{
		periodicBackupIntervalKey: strconv.Itoa(int(settings.PeriodicBackupInterval / time.Second)),
	})
	if updated == string(current) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(updated))
}

// merge sets keys inside gameModeSection, preserving the order of the file and
// each line's original ending. An absent section is appended rather than
// inserted, because the game does not care about section order.
func merge(content string, values map[string]string) string {
	lines := strings.Split(content, "\n")
	remaining := make(map[string]string, len(values))
	for key, value := range values {
		remaining[key] = value
	}
	inSection := false
	sectionEnd := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inSection {
				break
			}
			inSection = trimmed == "["+gameModeSection+"]"
			if inSection {
				sectionEnd = i
			}
			continue
		}
		if !inSection {
			continue
		}
		if trimmed != "" {
			sectionEnd = i
		}
		key, _, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value, wanted := remaining[key]
		if !wanted {
			continue
		}
		ending := ""
		if strings.HasSuffix(line, "\r") {
			ending = "\r"
		}
		lines[i] = key + "=" + value + ending
		delete(remaining, key)
	}
	if len(remaining) == 0 {
		return strings.Join(lines, "\n")
	}
	// Keys are emitted in a fixed order so that a rewrite of an unchanged
	// configuration produces an identical file.
	added := make([]string, 0, len(remaining)+1)
	for _, key := range []string{periodicBackupIntervalKey} {
		if value, ok := remaining[key]; ok {
			added = append(added, key+"="+value)
		}
	}
	if sectionEnd < 0 {
		return appendSection(lines, added)
	}
	tail := append([]string{}, lines[sectionEnd+1:]...)
	return strings.Join(append(append(lines[:sectionEnd+1:sectionEnd+1], added...), tail...), "\n")
}

func appendSection(lines, entries []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, "["+gameModeSection+"]")
	lines = append(lines, entries...)
	return strings.Join(append(lines, ""), "\n")
}

// WriteFileAtomic replaces a game INI file in one rename.
func WriteFileAtomic(path string, data []byte) error { return writeFileAtomic(path, data) }

// writeFileAtomic replaces the file in one rename. A torn Game.ini would be
// read by the game on the next start, so a partial write must never be visible.
func writeFileAtomic(path string, data []byte) error {
	temporary := path + ".conan-manager.tmp"
	f, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// SettingsFor maps a server configuration onto the values this package writes.
func SettingsFor(cfg server.Config) Settings {
	return Settings{PeriodicBackupInterval: time.Duration(cfg.PeriodicBackupIntervalSeconds) * time.Second}
}

// Describe reports what Apply would enforce, for logs and the API.
func Describe(settings Settings) string {
	if !settings.Managed() {
		return "unmanaged"
	}
	return fmt.Sprintf("%s=%d", periodicBackupIntervalKey, int(settings.PeriodicBackupInterval/time.Second))
}
