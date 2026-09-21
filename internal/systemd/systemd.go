// Package systemd talks to systemctl for units the manager owns. It never
// infers that a unit stopped from a failed query: callers that wait on a unit
// must keep waiting until systemd itself reports a terminal state.
package systemd

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes systemctl. Tests replace it; production uses Systemctl.
type Runner func(ctx context.Context, args ...string) (string, error)

// Systemctl runs /usr/bin/systemctl with a bounded timeout, so a wedged D-Bus
// cannot hang a lifecycle operation that holds a reservation.
func Systemctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/systemctl", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// State is the subset of unit properties lifecycle decisions depend on.
type State struct {
	Load    string
	Active  string
	Result  string
	MainPID int
	// ExitCode is how the main process ended: "exited", "killed" or "dumped".
	ExitCode string
	// ExitStatus is the exit status for "exited" and the signal number otherwise.
	ExitStatus int
	// StartedAt is when the main process started; zero if it never did.
	StartedAt time.Time
}

const properties = "--property=LoadState,ActiveState,Result,MainPID,ExecMainCode,ExecMainStatus,ExecMainStartTimestamp"

// Show reads the unit state. A unit that is not loaded is an error, because
// every caller expects a unit that was installed on purpose.
func Show(ctx context.Context, run Runner, unit string) (State, error) {
	// Unix timestamps ("@1789981221") avoid parsing a local time zone name.
	out, err := run(ctx, "show", unit, properties, "--timestamp=unix")
	if err != nil {
		return State{}, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = value
		}
	}
	s := State{
		Load:   values["LoadState"],
		Active: values["ActiveState"],
		Result: values["Result"],
	}
	if s.Load != "loaded" {
		return s, fmt.Errorf("unit %s is not loaded (%s)", unit, s.Load)
	}
	s.MainPID, _ = strconv.Atoi(values["MainPID"])
	s.ExitStatus, _ = strconv.Atoi(values["ExecMainStatus"])
	if raw, ok := strings.CutPrefix(values["ExecMainStartTimestamp"], "@"); ok {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
			s.StartedAt = time.Unix(seconds, 0).UTC()
		}
	}
	// systemd reports the code as a number (1 exited, 2 killed, 3 dumped) on
	// some versions and as a word on others.
	switch values["ExecMainCode"] {
	case "1", "exited":
		s.ExitCode = "exited"
	case "2", "killed":
		s.ExitCode = "killed"
	case "3", "dumped":
		s.ExitCode = "dumped"
	}
	return s, nil
}

// Running reports whether the unit still owns a process that may be writing.
func (s State) Running() bool {
	return s.Active == "active" || s.Active == "activating" || s.Active == "reloading"
}

// Stopping reports whether systemd is terminating the unit.
func (s State) Stopping() bool { return s.Active == "deactivating" }

// Terminal reports a state from which the unit will not change by itself.
func (s State) Terminal() bool { return s.Active == "inactive" || s.Active == "failed" }
