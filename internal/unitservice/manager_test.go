package unitservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/process"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

// fakeSystemd answers systemctl calls from a scripted unit state.
type fakeSystemd struct {
	mu     sync.Mutex
	active string
	result string
	code   string
	status int
	calls  []string
}

func (f *fakeSystemd) run(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "show":
		return fmt.Sprintf("LoadState=loaded\nActiveState=%s\nResult=%s\nMainPID=4242\nExecMainCode=%s\nExecMainStatus=%d\n",
			f.active, f.result, f.code, f.status), nil
	case "start":
		f.active = "active"
	case "stop":
		f.active = "deactivating"
	}
	return "", nil
}

func (f *fakeSystemd) set(active, code string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.code, f.status = active, code, status
}

func testConfig(t *testing.T) server.Config {
	t.Helper()
	return server.Config{ID: "alpha", Runtime: server.RuntimeNative, InstallDir: filepath.Join(t.TempDir(), "game"), LaunchArgs: "-log -port=7777"}
}

func TestStartWritesSpecAndStartsUnit(t *testing.T) {
	fake := &fakeSystemd{active: "inactive", result: "success"}
	m := New(t.TempDir())
	m.Run = fake.run
	cfg := testConfig(t)
	if err := m.Start(context.Background(), cfg); err != nil {
		t.Fatalf("start: %v", err)
	}
	info, err := os.Stat(m.SpecPath("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	// The runner refuses a writable specification; on Windows modes are not
	// enforced, so only the content is checked there.
	if os.PathSeparator == '/' && info.Mode().Perm() != 0o640 {
		t.Errorf("spec mode %v", info.Mode().Perm())
	}
	data, err := os.ReadFile(m.SpecPath("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	wantExe := cfg.InstallDir + "/ConanSandbox/Binaries/Linux/ConanSandboxServer-Linux-Shipping"
	if spec.Executable != wantExe || strings.Join(spec.Args, " ") != "ConanSandbox -log -port=7777" || spec.Dir != cfg.InstallDir {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	if got := m.Snapshot("alpha"); got.State != process.StateRunning || got.PID != 4242 {
		t.Fatalf("snapshot after start: %+v", got)
	}
	if !contains(fake.calls, "start conan-server@alpha.service") {
		t.Fatalf("unit not started: %v", fake.calls)
	}
}

// A unit that is still active belongs to a server this manager is not
// tracking. Starting it again would run two copies against one world.
func TestStartRefusesAnActiveUnit(t *testing.T) {
	fake := &fakeSystemd{active: "active"}
	m := New(t.TempDir())
	m.Run = fake.run
	if err := m.Start(context.Background(), testConfig(t)); err == nil {
		t.Fatal("expected refusal")
	}
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "start") {
			t.Fatalf("started over an active unit: %v", fake.calls)
		}
	}
}

func TestInspectRecordsTheExitItWatched(t *testing.T) {
	fake := &fakeSystemd{active: "inactive"}
	m := New(t.TempDir())
	m.Run = fake.run
	if err := m.Start(context.Background(), testConfig(t)); err != nil {
		t.Fatal(err)
	}
	fake.set("failed", "dumped", 11)
	s, err := m.Inspect(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if s.State != process.StateFailed || s.LastExit == nil || s.LastExit.Signal != process.SignalName(11) {
		t.Fatalf("crash not recorded: %+v %+v", s, s.LastExit)
	}

	// An old unit status seen by a freshly started manager is not an exit it
	// watched, and must not be reported as one.
	fresh := New(t.TempDir())
	fresh.Run = fake.run
	s, err = fresh.Inspect(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if s.LastExit != nil {
		t.Fatalf("stale exit reported as fresh: %+v", s.LastExit)
	}
}

func TestWaitKeepsPollingThroughQueryErrors(t *testing.T) {
	fake := &fakeSystemd{active: "active"}
	m := New(t.TempDir())
	failures := 2
	m.Run = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "show" && failures > 0 {
			failures--
			return "", fmt.Errorf("dbus unavailable")
		}
		if args[0] == "show" {
			fake.set("inactive", "exited", 0)
		}
		return fake.run(ctx, args...)
	}
	if err := m.Wait(context.Background(), "alpha"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if failures != 0 {
		t.Fatal("wait returned before systemd confirmed the unit stopped")
	}
}

func TestSpecRejectsNamesSystemdCannotCarry(t *testing.T) {
	for _, id := range []string{"", "../etc", "a b", "-lead", strings.Repeat("x", 65)} {
		cfg := server.Config{ID: id, Runtime: server.RuntimeNative, InstallDir: "/srv/game"}
		if _, err := SpecFor(cfg); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// The real game produced a systemctl kill error while its main process was in
// fact killed. That must not be reported as a failed kill.
func TestKillIgnoresAnErrorWhenTheMainProcessIsGone(t *testing.T) {
	fake := &fakeSystemd{active: "deactivating"}
	m := New(t.TempDir())
	m.Run = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "kill" {
			fake.set("failed", "killed", 9)
			return "", fmt.Errorf("Failed to send signal SIGKILL to auxiliary processes: Invalid argument")
		}
		out, err := fake.run(ctx, args...)
		if args[0] == "show" && strings.Contains(out, "ActiveState=failed") {
			out = strings.Replace(out, "MainPID=4242", "MainPID=0", 1)
		}
		return out, err
	}
	if err := m.Kill(context.Background(), "alpha"); err != nil {
		t.Fatalf("kill reported failure although the process is gone: %v", err)
	}
}

func TestKillReportsAnErrorWhenTheProcessSurvives(t *testing.T) {
	fake := &fakeSystemd{active: "deactivating"}
	m := New(t.TempDir())
	m.Run = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "kill" {
			return "", fmt.Errorf("permission denied")
		}
		return fake.run(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Kill(ctx, "alpha"); err == nil {
		t.Fatal("kill reported success while the main process is still running")
	}
}

// systemd clears the exit status after a successful stop. The run still ended,
// and hooks and events should say it ended cleanly.
func TestInspectReportsACleanExitWhenSystemdClearedTheStatus(t *testing.T) {
	fake := &fakeSystemd{active: "inactive", result: "success"}
	m := New(t.TempDir())
	m.Run = fake.run
	if err := m.Start(context.Background(), testConfig(t)); err != nil {
		t.Fatal(err)
	}
	fake.set("inactive", "0", 0)
	s, err := m.Inspect(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if s.LastExit == nil || s.LastExit.Code != 0 || s.LastExit.Signal != "" {
		t.Fatalf("clean exit not reported: %+v", s.LastExit)
	}
}
