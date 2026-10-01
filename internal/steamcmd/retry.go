package steamcmd

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// updateAttempts bounds app_update retries. A SteamCMD that was just
// downloaded often fails its first app_update with "Missing configuration"
// (exit status 8): it has not fetched the app's info yet. The same command
// run again succeeds, so an install on a fresh host needs a second try.
const updateAttempts = 3

// retryPause separates the attempts; a variable so tests need not wait.
var retryPause = 5 * time.Second

// runUpdate runs an app_update command line and reports SteamCMD's own
// reason when it fails, rather than a bare exit status.
func runUpdate(ctx context.Context, binary, dir string, args []string, output io.Writer) error {
	var err error
	for attempt := 1; attempt <= updateAttempts; attempt++ {
		tail := &tailBuffer{max: 16 << 10}
		var w io.Writer = tail
		if output != nil {
			w = io.MultiWriter(output, tail)
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = w, w
		err = cmd.Run()
		if err == nil {
			return nil
		}
		reason := steamError(tail.String())
		if reason != "" {
			err = fmt.Errorf("%w: %s", err, reason)
		}
		if ctx.Err() != nil || !transient(reason) || attempt == updateAttempts {
			return err
		}
		if output != nil {
			fmt.Fprintf(output, "\n=== conan-manager: %s, trying again (%d of %d) ===\n", reason, attempt+1, updateAttempts)
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(retryPause):
		}
	}
	return err
}

// transient tells failures that a second run fixes. Workshop downloads fail
// with a bare "(Failure)" often enough that not retrying them would make
// installing a dozen mods a matter of luck, but a refusal to serve the item at
// all is settled and retrying only delays the report.
func transient(reason string) bool {
	for _, settled := range []string{"Access Denied", "No subscription", "Invalid Platform", "File Not Found", "Disk write failure"} {
		if strings.Contains(reason, settled) {
			return false
		}
	}
	for _, retryable := range []string{"Missing configuration", "(Failure)", "Timeout", "Connection"} {
		if strings.Contains(reason, retryable) {
			return true
		}
	}
	return false
}

// steamError picks SteamCMD's last error line, such as
// "ERROR! Failed to install app '443030' (Missing configuration)".
func steamError(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "ERROR!") || strings.Contains(line, "Failed to install app") ||
			strings.HasPrefix(line, "Error!") {
			if len(line) > 300 {
				line = line[:300]
			}
			return line
		}
	}
	return ""
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
