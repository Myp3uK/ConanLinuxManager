package bindmount

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

var names = []string{"game_0.db", "game_0.db-wal", "game_0.db-shm"}

type pair struct {
	source, target int
	path           string
}

// openPinned rejects symlinks in every path component, including proc magic
// links. No pathname supplied by the caller is passed to mount(2).
func openPinned(path string, directory bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, fmt.Errorf("path must be absolute and clean: %q", path)
	}
	flags := uint64(unix.O_PATH | unix.O_CLOEXEC)
	if directory {
		flags |= unix.O_DIRECTORY
	}
	return unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags: flags, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

func fdPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

func privateNamespace() error {
	self, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return err
	}
	init, err := os.Stat("/proc/1/ns/mnt")
	if err != nil {
		return err
	}
	if os.SameFile(self, init) {
		return fmt.Errorf("requires a private mount namespace (unshare --mount --propagation private)")
	}
	return nil
}

func mountPoints() (map[string]bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		fields := strings.Fields(parts[0])
		if len(parts) != 2 || len(fields) < 6 {
			return nil, fmt.Errorf("invalid mountinfo")
		}
		for _, field := range fields[6:] {
			if strings.HasPrefix(field, "shared:") || strings.HasPrefix(field, "master:") {
				return nil, fmt.Errorf("mount namespace must have private propagation")
			}
		}
		result[decode.Replace(fields[4])] = true
	}
	return result, nil
}

func Run(action, saved, ram string) error {
	if os.Geteuid() != 0 || os.Getuid() != 0 {
		return fmt.Errorf("requires root; setuid invocation is unsupported")
	}
	if action != "mount" && action != "unmount" {
		return fmt.Errorf("action must be mount or unmount")
	}
	if err := privateNamespace(); err != nil {
		return err
	}
	mounts, err := mountPoints()
	if err != nil {
		return err
	}
	savedFD, err := openPinned(saved, true)
	if err != nil {
		return err
	}
	defer unix.Close(savedFD)
	ramFD, err := openPinned(ram, true)
	if err != nil {
		return err
	}
	defer unix.Close(ramFD)
	var fs unix.Statfs_t
	if err := unix.Fstatfs(ramFD, &fs); err != nil {
		return err
	}
	if fs.Type != unix.TMPFS_MAGIC {
		return fmt.Errorf("RAM directory is not on tmpfs")
	}
	if err := unix.Fstatfs(savedFD, &fs); err != nil {
		return err
	}
	if fs.Type == unix.TMPFS_MAGIC {
		return fmt.Errorf("Saved must be on persistent storage")
	}
	var pairs []pair
	defer func() {
		for _, p := range pairs {
			unix.Close(p.source)
			unix.Close(p.target)
		}
	}()
	for _, name := range names {
		path := filepath.Join(saved, name)
		source, err := unix.Openat(ramFD, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		target, err := unix.Openat(savedFD, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			unix.Close(source)
			return err
		}
		pairs = append(pairs, pair{source, target, path})
		var s, t unix.Stat_t
		if err := unix.Fstat(source, &s); err != nil {
			return err
		}
		if err := unix.Fstat(target, &t); err != nil {
			return err
		}
		if s.Mode&unix.S_IFMT != unix.S_IFREG || t.Mode&unix.S_IFMT != unix.S_IFREG || s.Nlink != 1 || t.Nlink != 1 {
			return fmt.Errorf("requires regular single-link files: %s", name)
		}
		if err := unix.Fstatfs(source, &fs); err != nil {
			return err
		}
		if fs.Type != unix.TMPFS_MAGIC {
			return fmt.Errorf("source is not tmpfs: %s", name)
		}
		same := s.Dev == t.Dev && s.Ino == t.Ino
		if action == "mount" && (mounts[path] || same) {
			return fmt.Errorf("target already mounted or aliases source: %s", path)
		}
		if action == "unmount" && (!mounts[path] || !same) {
			return fmt.Errorf("target is not the expected source mount: %s", path)
		}
	}
	if action == "unmount" {
		// O_PATH descriptors opened on the mounted targets themselves hold the
		// mounts busy. Release them after validating the entire set.
		for i := range pairs {
			unix.Close(pairs[i].target)
			pairs[i].target = -1
		}
		// Offline operation: callers must exclude concurrent renames/mounts and
		// stop SQLite/Conan first. Never use lazy or forced unmount.
		for i := len(pairs) - 1; i >= 0; i-- {
			if err := unix.Unmount(pairs[i].path, unix.UMOUNT_NOFOLLOW); err != nil {
				return fmt.Errorf("unmount %s: %w", pairs[i].path, err)
			}
		}
		return nil
	}
	for i, p := range pairs {
		if err := unix.Mount(fdPath(p.source), fdPath(p.target), "", unix.MS_BIND, ""); err != nil {
			result := fmt.Errorf("mount %s: %w", p.path, err)
			for j := i - 1; j >= 0; j-- {
				if rollback := unix.Unmount(pairs[j].path, unix.UMOUNT_NOFOLLOW); rollback != nil {
					result = errors.Join(result, fmt.Errorf("rollback %s: %w", pairs[j].path, rollback))
				}
			}
			return result
		}
	}
	return nil
}
