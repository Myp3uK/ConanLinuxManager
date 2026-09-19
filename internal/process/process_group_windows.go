//go:build windows

package process

import (
	"os/exec"
)

func configureProcessGroup(cmd *exec.Cmd) {}

func signalProcessGroup(pid int, force bool) error {
	return nil
}
