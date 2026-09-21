package hooks

import (
	"os"
	"os/exec"
	"time"
)

// Hooks are deployed on Linux. Windows has no uid to check, so development
// builds accept any file; the unix build is the one that enforces ownership.
func ownedByRoot(os.FileInfo) error { return nil }

func isolate(cmd *exec.Cmd) { cmd.WaitDelay = 5 * time.Second }
