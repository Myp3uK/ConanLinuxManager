package ramstate

import (
	"os"
	"syscall"
)

func restoreOwner(path, directory string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	stat := info.Sys().(*syscall.Stat_t)
	return os.Chown(path, int(stat.Uid), int(stat.Gid))
}
