package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/server"
)

type State string

// StopTimeout is the last-resort kill for an in-process server. The controller
// kills earlier, after its own stop timeout, and checks the database; this only
// matters if the controller's supervisor is not running.
const StopTimeout = 10 * time.Minute

const (
	StateStopped  State = "stopped"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

type Runtime interface {
	Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error)
}

// Argv is the program and arguments that start a server. The in-process
// runtime and the systemd runner share it, so both launch exactly the same
// command line.
func Argv(cfg server.Config) (string, []string, error) {
	extra := append(strings.Fields(cfg.LaunchArgs), portArgs(cfg)...)
	if cfg.Multihome != "" {
		extra = append(extra, "-MULTIHOME="+cfg.Multihome)
	}
	// The map is the first argument after the project; the game loads the
	// Exiled Lands without one.
	if cfg.Map != "" {
		extra = append([]string{cfg.Map}, extra...)
	}
	windowsBinary := cfg.InstallDir + "/ConanSandbox/Binaries/Win64/ConanSandboxServer-Win64-Shipping.exe"
	switch cfg.Runtime {
	case server.RuntimeNative:
		path := cfg.InstallDir + "/ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping"
		return path, append([]string{"ConanSandbox"}, extra...), nil
	case server.RuntimeWine:
		return "wine64", append([]string{windowsBinary}, extra...), nil
	case server.RuntimeProton:
		return "proton", append([]string{"run", windowsBinary}, extra...), nil
	}
	return "", nil, fmt.Errorf("unsupported runtime %q", cfg.Runtime)
}

// portArgs passes the configured ports unless the operator set them in the
// launch arguments. Without them the game silently uses its defaults, so two
// servers on one host collide and the query probe watches a port nobody uses.
func portArgs(cfg server.Config) []string {
	var result []string
	for _, port := range []struct {
		flag  string
		value int
	}{{"-Port=", cfg.GamePort}, {"-QueryPort=", cfg.QueryPort}} {
		if port.value > 0 && !hasFlag(cfg.LaunchArgs, port.flag) {
			result = append(result, fmt.Sprintf("%s%d", port.flag, port.value))
		}
	}
	return result
}

func hasFlag(args, flag string) bool {
	for _, arg := range strings.Fields(args) {
		if len(arg) >= len(flag) && strings.EqualFold(arg[:len(flag)], flag) {
			return true
		}
	}
	return false
}

type NativeRuntime struct{}

func (NativeRuntime) Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error) {
	path, args, err := Argv(cfg)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, path, args...), nil
}

type WineRuntime struct{ Binary string }

func (r WineRuntime) Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error) {
	path, args, err := Argv(cfg)
	if err != nil {
		return nil, err
	}
	if r.Binary != "" {
		path = r.Binary
	}
	return exec.CommandContext(ctx, path, args...), nil
}

type ProtonRuntime struct{ Binary string }

func (r ProtonRuntime) Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error) {
	path, args, err := Argv(cfg)
	if err != nil {
		return nil, err
	}
	if r.Binary != "" {
		path = r.Binary
	}
	return exec.CommandContext(ctx, path, args...), nil
}

type Process struct {
	Cmd       *exec.Cmd
	StartedAt time.Time
	State     State
	done      chan struct{}
	stopping  bool
	forced    bool
	exit      *Exit
}

// Exit describes how a server process ended. A crash and a requested stop can
// both end in a signal, so the lifecycle state at exit time, not this record,
// decides whether an exit was expected.
type Exit struct {
	Code   int       `json:"code"`
	Signal string    `json:"signal,omitempty"`
	At     time.Time `json:"at"`
}

func (e Exit) String() string {
	if e.Signal != "" {
		return "signal " + e.Signal
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

type Snapshot struct {
	State     State     `json:"state"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Forced    bool      `json:"forced_stop"`
	LastExit  *Exit     `json:"last_exit,omitempty"`
}

type Manager struct {
	mu      sync.Mutex
	process map[string]*Process
	runtime map[server.Runtime]Runtime
}

func NewManager() *Manager {
	return &Manager{
		process: make(map[string]*Process),
		runtime: map[server.Runtime]Runtime{
			server.RuntimeNative: NativeRuntime{},
			server.RuntimeWine:   WineRuntime{},
			server.RuntimeProton: ProtonRuntime{},
		},
	}
}

func (m *Manager) Start(ctx context.Context, cfg server.Config) error {
	// A server process must outlive the HTTP request that started it.
	ctx = context.WithoutCancel(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.process[cfg.ID]; current != nil && (current.State == StateRunning || current.State == StateStopping) {
		return fmt.Errorf("server %q is already running", cfg.ID)
	}
	rt := m.runtime[cfg.Runtime]
	if rt == nil {
		return fmt.Errorf("unsupported runtime %q", cfg.Runtime)
	}
	cmd, err := rt.Command(ctx, cfg)
	if err != nil {
		return err
	}
	cmd.Dir = cfg.InstallDir
	configureProcessGroup(cmd)
	logPath := filepath.Join(cfg.InstallDir, "conan-manager-process.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("open process log: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start server: %w", err)
	}
	p := &Process{Cmd: cmd, StartedAt: time.Now().UTC(), State: StateRunning, done: make(chan struct{})}
	m.process[cfg.ID] = p
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		m.mu.Lock()
		if p := m.process[cfg.ID]; p != nil && p.Cmd == cmd {
			if p.stopping || err == nil {
				p.State = StateStopped
			} else {
				p.State = StateFailed
			}
			if cmd.ProcessState != nil {
				exit := exitFrom(cmd.ProcessState)
				p.exit = &exit
			}
			close(p.done)
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.process[id]
	if p == nil || p.State != StateRunning {
		return nil
	}
	if err := signalProcessGroup(p.Cmd.Process.Pid, false); err != nil && err != os.ErrProcessDone {
		return fmt.Errorf("stop server: %w", err)
	}
	p.stopping = true
	p.State = StateStopping
	go func() {
		timer := time.NewTimer(StopTimeout)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			m.mu.Lock()
			if p.State == StateStopping {
				if signalProcessGroup(p.Cmd.Process.Pid, true) == nil {
					p.forced = true
				}
			}
			m.mu.Unlock()
		}
	}()
	return nil
}

// Kill ends a server that ignored SIGTERM. It is the operator's escape hatch for
// a hung save; the world may lose whatever was not yet written.
func (m *Manager) Kill(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.process[id]
	if p == nil || (p.State != StateRunning && p.State != StateStopping) {
		return nil
	}
	p.stopping = true
	p.State = StateStopping
	if err := signalProcessGroup(p.Cmd.Process.Pid, true); err != nil && err != os.ErrProcessDone {
		return fmt.Errorf("kill server: %w", err)
	}
	p.forced = true
	return nil
}

func (m *Manager) Wait(ctx context.Context, id string) error {
	m.mu.Lock()
	p := m.process[id]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Status(id string) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.process[id]; p != nil {
		return p.State
	}
	return StateStopped
}

func (m *Manager) Snapshot(id string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.process[id]
	if p == nil {
		return Snapshot{State: StateStopped}
	}
	snapshot := Snapshot{State: p.State, StartedAt: p.StartedAt, Forced: p.forced, LastExit: p.exit}
	if (p.State == StateRunning || p.State == StateStopping) && p.Cmd != nil && p.Cmd.Process != nil {
		snapshot.PID = p.Cmd.Process.Pid
	}
	return snapshot
}
