//go:build !windows

package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ownedByRoot accepts only files root owns and nobody else can modify. The
// manager's own user must not be able to change a script it will execute.
func ownedByRoot(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read ownership")
	}
	if stat.Uid != 0 {
		return fmt.Errorf("owned by uid %d, must be owned by root", stat.Uid)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("writable by group or others")
	}
	return nil
}

// isolate puts a script in its own process group, so a timeout kills whatever
// it started as well, not just the script itself.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}
