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

const StopTimeout = 180 * time.Second

const (
	StateStopped  State = "stopped"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

type Runtime interface {
	Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error)
}

type NativeRuntime struct{}

func (NativeRuntime) Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error) {
	path := cfg.InstallDir + "/ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping"
	args := append([]string{"ConanSandbox"}, strings.Fields(cfg.LaunchArgs)...)
	return exec.CommandContext(ctx, path, args...), nil
}

type WineRuntime struct{ Binary string }

func (r WineRuntime) Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error) {
	if r.Binary == "" {
		r.Binary = "wine64"
	}
	path := cfg.InstallDir + "/ConanSandbox/Binaries/Win64/ConanSandboxServer-Win64-Shipping.exe"
	args := append([]string{path}, strings.Fields(cfg.LaunchArgs)...)
	return exec.CommandContext(ctx, r.Binary, args...), nil
}

type ProtonRuntime struct{ Binary string }

func (r ProtonRuntime) Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error) {
	if r.Binary == "" {
		r.Binary = "proton"
	}
	path := cfg.InstallDir + "/ConanSandbox/Binaries/Win64/ConanSandboxServer-Win64-Shipping.exe"
	args := append([]string{"run", path}, strings.Fields(cfg.LaunchArgs)...)
	return exec.CommandContext(ctx, r.Binary, args...), nil
}

type Process struct {
	Cmd       *exec.Cmd
	StartedAt time.Time
	State     State
	done      chan struct{}
	stopping  bool
	forced bool
}

type Snapshot struct {
	State     State     `json:"state"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Forced bool `json:"forced_stop"`
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
				if signalProcessGroup(p.Cmd.Process.Pid, true) == nil { p.forced = true }
			}
			m.mu.Unlock()
		}
	}()
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
	snapshot := Snapshot{State: p.State, StartedAt: p.StartedAt, Forced: p.forced}
	if (p.State == StateRunning || p.State == StateStopping) && p.Cmd != nil && p.Cmd.Process != nil {
		snapshot.PID = p.Cmd.Process.Pid
	}
	return snapshot
}
