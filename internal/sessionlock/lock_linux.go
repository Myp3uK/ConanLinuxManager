package sessionlock

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

type Lease struct{ files []*os.File }

// Acquire locks existing directory inodes. Missing paths use the nearest
// existing ancestor conservatively, so an installation can create directories.
// Locks are cooperative and must be held through process exit and persistence.
func Acquire(paths ...string) (*Lease, error) {
	l := &Lease{}
	sort.Strings(paths)
	seen := map[[2]uint64]bool{}
	for _, path := range paths {
		path, err := filepath.Abs(path)
		if err != nil {
			l.Close()
			return nil, err
		}
		var f *os.File
		for {
			f, err = os.Open(path)
			if !os.IsNotExist(err) || filepath.Dir(path) == path {
				break
			}
			path = filepath.Dir(path)
		}
		if err != nil {
			l.Close()
			return nil, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &st); err != nil {
			f.Close()
			l.Close()
			return nil, err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			f.Close()
			l.Close()
			return nil, fmt.Errorf("lock path is not a directory: %s", path)
		}
		key := [2]uint64{uint64(st.Dev), st.Ino}
		if seen[key] {
			f.Close()
			continue
		}
		seen[key] = true
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			f.Close()
			l.Close()
			return nil, fmt.Errorf("session lock %s: %w", path, err)
		}
		l.files = append(l.files, f)
	}
	return l, nil
}

func (l *Lease) Close() {
	if l != nil {
		for _, f := range l.files {
			_ = f.Close()
		}
		l.files = nil
	}
}
