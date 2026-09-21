package unitservice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// RunServer is the unit's ExecStart. It validates the specification, points
// stdout and stderr at the server's process log and replaces itself with the
// game, so the game becomes the unit's main process and receives systemd's
// SIGTERM directly.
func RunServer(id, specDir string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid server id %q", id)
	}
	if specDir == "" {
		specDir = DefaultSpecDir
	}
	spec, err := readSpec(filepath.Join(specDir, id+".json"), id)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("open process log: %w", err)
	}
	fmt.Fprintf(log, "=== conan-manager: starting %s at %s ===\n", id, time.Now().UTC().Format(time.RFC3339))
	for _, target := range []int{1, 2} {
		if err := unix.Dup3(int(log.Fd()), target, 0); err != nil {
			return fmt.Errorf("redirect output: %w", err)
		}
	}
	if err := os.Chdir(spec.Dir); err != nil {
		return err
	}
	argv := append([]string{spec.Executable}, spec.Args...)
	return syscall.Exec(spec.Executable, argv, os.Environ())
}

// readSpec rejects a specification anyone but this user could have written.
// The runner executes whatever the file names, so that file is the boundary.
func readSpec(path, id string) (Spec, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Spec{}, err
	}
	if !info.Mode().IsRegular() {
		return Spec{}, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return Spec{}, fmt.Errorf("%s is writable by group or others", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return Spec{}, fmt.Errorf("%s is not owned by the service user", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return Spec{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if spec.Version != 1 || spec.ServerID != id {
		return Spec{}, fmt.Errorf("%s does not describe server %q", path, id)
	}
	for name, value := range map[string]string{"executable": spec.Executable, "dir": spec.Dir, "log_path": spec.LogPath} {
		if !filepath.IsAbs(value) {
			return Spec{}, fmt.Errorf("%s must be absolute: %q", name, value)
		}
	}
	return spec, nil
}
