//go:build linux

package tmpfs

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// UsedRatio reports how full the filesystem holding path is, from 0 to 1.
// The RAM disk is sized for the active database and its two SQLite sidecars;
// anything else landing there can exhaust it within minutes on a large save.
func (Manager) UsedRatio(path string) (float64, error) {
	clean, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(clean, &stat); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", clean, err)
	}
	if stat.Blocks == 0 {
		return 0, fmt.Errorf("filesystem at %s reports no blocks", clean)
	}
	used := stat.Blocks - stat.Bfree
	return float64(used) / float64(stat.Blocks), nil
}
