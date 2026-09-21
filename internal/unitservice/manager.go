// Package unitservice runs each game server as its own systemd unit, so a
// server outlives the manager process. Restarting or upgrading the manager used
// to stop every game server it had started, because they were its children.
//
// The unit runs as the manager's own user and executes a launch specification
// the manager writes. No privilege is gained through it, which is why the
// polkit rule may allow any conan-server@ instance rather than an allowlist.
package unitservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/systemd"
)

// DefaultSpecDir holds one launch specification per server.
const DefaultSpecDir = "/var/lib/conan-manager/units"

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// Unit is the systemd instance that runs a server.
func Unit(id string) string { return "conan-server@" + id + ".service" }

// Spec is everything the runner needs. It is written by the manager and read
// by the runner as the same user, immediately before exec.
type Spec struct {
	Version    int      `json:"version"`
	ServerID   string   `json:"server_id"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	Dir        string   `json:"dir"`
	LogPath    string   `json:"log_path"`
}

// SpecFor builds the launch specification from a server configuration.
func SpecFor(cfg server.Config) (Spec, error) {
	if !validID.MatchString(cfg.ID) {
		return Spec{}, fmt.Errorf("server id %q cannot name a systemd unit", cfg.ID)
	}
	path, args, err := process.Argv(cfg)
	if err != nil {
		return Spec{}, err
	}
	// The runner receives a minimal environment, so a bare name such as wine64
	// is resolved here, where PATH is the one the operator configured.
	if !filepath.IsAbs(path) {
		resolved, err := exec.LookPath(path)
		if err != nil {
			return Spec{}, fmt.Errorf("find %s: %w", path, err)
		}
		path = resolved
	}
	return Spec{
		Version:    1,
		ServerID:   cfg.ID,
		Executable: path,
		Args:       args,
		Dir:        cfg.InstallDir,
		LogPath:    filepath.Join(cfg.InstallDir, "conan-manager-process.log"),
	}, nil
}

type Manager struct {
	SpecDir string
	Run     systemd.Runner
	mu      sync.Mutex
	states  map[string]process.Snapshot
}

func New(specDir string) *Manager {
	if specDir == "" {
		specDir = DefaultSpecDir
	}
	return &Manager{SpecDir: specDir, Run: systemd.Systemctl, states: map[string]process.Snapshot{}}
}

func (m *Manager) SpecPath(id string) string { return filepath.Join(m.SpecDir, id+".json") }

// Start writes the specification and starts the unit. It refuses a unit that
// is still active: that is a server this manager does not currently track, and
// starting over it would run two copies against one world.
func (m *Manager) Start(ctx context.Context, cfg server.Config) error {
	spec, err := SpecFor(cfg)
	if err != nil {
		return err
	}
	state, err := systemd.Show(ctx, m.Run, Unit(cfg.ID))
	if err != nil {
		return err
	}
	if state.Running() || state.Stopping() {
		return fmt.Errorf("unit %s is already active", Unit(cfg.ID))
	}
	if err := m.writeSpec(spec); err != nil {
		return err
	}
	m.remember(cfg.ID, process.Snapshot{State: process.StateRunning, StartedAt: time.Now().UTC()})
	if _, err := m.Run(ctx, "start", Unit(cfg.ID)); err != nil {
		// Refresh so the snapshot shows the failed unit and its exit status
		// instead of the optimistic running state recorded above.
		_, _ = m.Inspect(ctx, cfg.ID)
		return err
	}
	_, err = m.Inspect(ctx, cfg.ID)
	return err
}

func (m *Manager) writeSpec(spec Spec) error {
	if err := os.MkdirAll(m.SpecDir, 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	path := m.SpecPath(spec.ServerID)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o640); err != nil {
		return err
	}
	// WriteFile keeps the mode of an existing file, and the runner refuses a
	// group or world writable specification.
	if err := os.Chmod(temporary, 0o640); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// Stop asks systemd to stop the unit and returns at once. systemd sends SIGTERM
// and escalates to SIGKILL only after the unit's TimeoutStopSec, which is sized
// for a large world's final save.
func (m *Manager) Stop(ctx context.Context, id string) error {
	_, err := m.Run(ctx, "stop", "--no-block", Unit(id))
	return err
}

// Kill ends the unit immediately. Anything the game had not yet saved is lost.
//
// systemctl can report a failure to signal a unit's auxiliary processes while
// the unit is already going down, even though the main process was killed; it
// was seen with the real game. Only a main process that is still alive
// afterwards means the kill did not work.
func (m *Manager) Kill(ctx context.Context, id string) error {
	_, err := m.Run(ctx, "kill", "--signal=SIGKILL", Unit(id))
	if err == nil {
		return nil
	}
	for i := 0; i < 20; i++ {
		state, showErr := systemd.Show(ctx, m.Run, Unit(id))
		if showErr == nil && (state.MainPID == 0 || state.Terminal()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(250 * time.Millisecond):
		}
	}
	if _, retryErr := m.Run(ctx, "kill", "--kill-whom=main", "--signal=SIGKILL", Unit(id)); retryErr == nil {
		return nil
	}
	return err
}

// Inspect queries systemd and updates the cached snapshot.
func (m *Manager) Inspect(ctx context.Context, id string) (process.Snapshot, error) {
	state, err := systemd.Show(ctx, m.Run, Unit(id))
	if err != nil {
		return process.Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.states[id]
	s := process.Snapshot{State: process.StateStopped, StartedAt: previous.StartedAt, LastExit: previous.LastExit}
	switch {
	case state.Running():
		s.State = process.StateRunning
		s.PID = state.MainPID
		// An adopted unit was started by an earlier manager; systemd knows when.
		if !state.StartedAt.IsZero() {
			s.StartedAt = state.StartedAt
		}
	case state.Stopping():
		s.State = process.StateStopping
		s.PID = state.MainPID
	case state.Active == "failed":
		s.State = process.StateFailed
	}
	s.Forced = state.Result == "timeout" || previous.Forced
	// Only an exit this manager watched happen is recorded: systemd keeps the
	// status of a run from long ago, and reporting it as fresh would mislead.
	if state.Terminal() && (previous.State == process.StateRunning || previous.State == process.StateStopping) {
		exit := process.Exit{At: time.Now().UTC()}
		switch state.ExitCode {
		case "exited":
			exit.Code = state.ExitStatus
		case "killed", "dumped":
			exit.Signal = process.SignalName(state.ExitStatus)
		default:
			// After a successful stop systemd clears the exit status of the
			// main process; SuccessExitStatus=143 makes Conan's normal exit
			// one of those. Success is all that is known, so report a clean
			// exit rather than none.
			if state.Result != "success" {
				exit.Signal = "unknown (" + state.Result + ")"
			}
		}
		s.LastExit = &exit
	}
	m.states[id] = s
	return s, nil
}

// Wait returns once systemd reports the unit terminal. A failed query is not
// evidence that the game exited, so it keeps polling rather than returning.
func (m *Manager) Wait(ctx context.Context, id string) error {
	for {
		s, err := m.Inspect(ctx, id)
		if err == nil && s.State != process.StateRunning && s.State != process.StateStopping {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Snapshot returns the last state observed, without asking systemd.
func (m *Manager) Snapshot(id string) process.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.states[id]; ok {
		return s
	}
	return process.Snapshot{State: process.StateStopped}
}

func (m *Manager) remember(id string, s process.Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[id] = s
}
