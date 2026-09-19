// Package launchservice manages preconfigured root-owned systemd launcher units.
package launchservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/process"
	"github.com/example/conan-manager/internal/server"
)

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type Manager struct {
	IDs    map[string]bool
	mu     sync.Mutex
	states map[string]process.Snapshot
}

func New(ids string) *Manager {
	m := &Manager{IDs: map[string]bool{}, states: map[string]process.Snapshot{}}
	for _, id := range strings.Split(ids, ",") {
		id = strings.TrimSpace(id)
		if validID.MatchString(id) {
			m.IDs[id] = true
		}
	}
	return m
}

func (m *Manager) Enabled(id string) bool { return m != nil && m.IDs[id] }
func unit(id string) string               { return "conan-launch@" + id + ".service" }

func command(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "/usr/bin/systemctl", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("systemctl: %w: %s", err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

// Validate binds the API record to the independently root-controlled launch
// configuration. Neither API paths nor args are sent to the privileged unit.
func (m *Manager) Validate(cfg server.Config) error {
	if !m.Enabled(cfg.ID) {
		return fmt.Errorf("launcher ID is not enabled")
	}
	b, err := os.ReadFile(filepath.Join("/etc/conan-manager/launch", cfg.ID+".json"))
	if err != nil {
		return err
	}
	var c struct {
		ID         string   `json:"server_id"`
		Install    string   `json:"install_dir"`
		RAM        string   `json:"ram_dir"`
		Executable string   `json:"executable"`
		Args       []string `json:"args"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	args := append([]string{"ConanSandbox"}, strings.Fields(cfg.LaunchArgs)...)
	if c.ID != cfg.ID || c.Install != cfg.InstallDir || c.RAM != cfg.RamdiskPath || !cfg.RamdiskEnabled || cfg.Runtime != server.RuntimeNative || c.Executable != filepath.Join(cfg.InstallDir, "ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping") || strings.Join(c.Args, "\x00") != strings.Join(args, "\x00") {
		return fmt.Errorf("API configuration differs from trusted launcher configuration")
	}
	return nil
}

func (m *Manager) Start(ctx context.Context, cfg server.Config) error {
	if err := m.Validate(cfg); err != nil {
		return err
	}
	s, err := m.inspect(ctx, cfg.ID)
	if err != nil {
		return err
	}
	if s.State == process.StateRunning || s.State == process.StateStopping {
		return fmt.Errorf("launcher unit already active")
	}
	_, err = command(ctx, "start", unit(cfg.ID))
	return err
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	_, err := command(ctx, "stop", "--no-block", unit(id))
	return err
}

func (m *Manager) inspect(ctx context.Context, id string) (process.Snapshot, error) {
	b, err := command(ctx, "show", unit(id), "--property=LoadState,ActiveState,MainPID,Result")
	if err != nil {
		return process.Snapshot{}, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(b, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			values[k] = v
		}
	}
	if values["LoadState"] != "loaded" {
		return process.Snapshot{}, fmt.Errorf("launcher unit not loaded")
	}
	s := process.Snapshot{State: process.StateStopped}
	switch values["ActiveState"] {
	case "active", "activating":
		s.State = process.StateRunning
	case "deactivating":
		s.State = process.StateStopping
	case "failed":
		s.State = process.StateFailed
	case "inactive":
	default:
		return s, fmt.Errorf("unknown unit state %q", values["ActiveState"])
	}
	if s.State == process.StateRunning || s.State == process.StateStopping {
		s.PID, _ = strconv.Atoi(values["MainPID"])
	}
	s.Forced = values["Result"] == "timeout"
	m.mu.Lock()
	m.states[id] = s
	m.mu.Unlock()
	return s, nil
}

func (m *Manager) Wait(ctx context.Context, id string) error {
	for {
		s, err := m.inspect(ctx, id)
		// A failed status query is NOT proof the process exited. Keep the
		// lifecycle reservation until systemd confirms a terminal state.
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

func (m *Manager) Snapshot(id string) process.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.states[id]; ok {
		return s
	}
	return process.Snapshot{State: process.StateStopped}
}

func (m *Manager) Inspect(ctx context.Context, id string) (process.Snapshot, error) {
	return m.inspect(ctx, id)
}
