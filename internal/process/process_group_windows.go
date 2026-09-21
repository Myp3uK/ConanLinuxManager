//go:build windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {}

func signalProcessGroup(pid int, force bool) error {
	return nil
}

func exitFrom(state *os.ProcessState) Exit {
	return Exit{Code: state.ExitCode(), At: time.Now().UTC()}
}

// SignalName exists so callers compile on Windows; signals are a Unix notion.
func SignalName(number int) string { return fmt.Sprintf("signal %d", number) }
