package hooks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/server"
)

type recorder struct {
	mu   sync.Mutex
	runs []server.HookRun
}

func (r *recorder) record(_ context.Context, run server.HookRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, run)
	return nil
}

func (r *recorder) all() []server.HookRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]server.HookRun(nil), r.runs...)
}

// testRunner uses a private directory. Tests that do not check ownership skip
// the root requirement, since they may run as any user.
func testRunner(t *testing.T, scripts map[string]string) (*Runner, *recorder) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := New(dir, 5*time.Second)
	r.trustOwner = func(os.FileInfo) error { return nil }
	rec := &recorder{}
	r.Record = rec.record
	return r, rec
}

func serverWith(t *testing.T, hooks map[string][]string) server.Config {
	return server.Config{ID: "alpha", Name: "Alpha", InstallDir: t.TempDir(), GamePort: 7777, QueryPort: 27015, Hooks: hooks}
}

func TestGateStopsAtTheFirstFailureAndAborts(t *testing.T) {
	r, rec := testRunner(t, map[string]string{
		"check-disk": "#!/bin/sh\necho 'disk full' >&2\nexit 2\n",
		"never":      "#!/bin/sh\necho ran\n",
	})
	var failures []*Failure
	r.OnFailure = func(_ server.Config, f *Failure) { failures = append(failures, f) }
	cfg := serverWith(t, map[string][]string{"pre_start": {"check-disk", "never"}})
	err := r.Run(context.Background(), Gate, "pre_start", cfg, nil)
	if err == nil {
		t.Fatal("a failing pre_start hook must abort the start")
	}
	runs := rec.all()
	if len(runs) != 1 || runs[0].Script != "check-disk" || runs[0].ExitCode != 2 || !strings.Contains(runs[0].Output, "disk full") {
		t.Fatalf("runs: %+v", runs)
	}
	if len(failures) != 1 {
		t.Fatalf("failure not reported: %+v", failures)
	}
}

func TestWaitRunsEveryScriptAndNeverAborts(t *testing.T) {
	r, rec := testRunner(t, map[string]string{
		"warn-players": "#!/bin/sh\nexit 1\n",
		"flush-cache":  "#!/bin/sh\nexit 0\n",
	})
	cfg := serverWith(t, map[string][]string{"pre_stop": {"warn-players", "flush-cache"}})
	if err := r.Run(context.Background(), Wait, "pre_stop", cfg, nil); err != nil {
		t.Fatalf("a failing pre_stop hook must not prevent the stop: %v", err)
	}
	if runs := rec.all(); len(runs) != 2 {
		t.Fatalf("not every script ran: %+v", runs)
	}
}

func TestAsyncRunsInTheBackground(t *testing.T) {
	r, rec := testRunner(t, map[string]string{"slow": "#!/bin/sh\nsleep 0.3\n"})
	cfg := serverWith(t, map[string][]string{"post_stop": {"slow"}})
	started := time.Now()
	if err := r.Run(context.Background(), Async, "post_stop", cfg, nil); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("async hook blocked the caller")
	}
	if !r.Drain(5 * time.Second) {
		t.Fatal("drain timed out")
	}
	if len(rec.all()) != 1 {
		t.Fatal("async hook did not run")
	}
}

// The manager's environment holds the admin password and the secret key; a
// hook must see the server, not those.
func TestScriptsGetServerFactsButNoManagerSecrets(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "hunter2")
	t.Setenv("CONAN_MANAGER_SECRET_KEY", "k3y")
	r, rec := testRunner(t, map[string]string{"env": "#!/bin/sh\nenv\n"})
	cfg := serverWith(t, map[string][]string{"on_crash": {"env"}})
	if err := r.Run(context.Background(), Wait, "on_crash", cfg, map[string]string{"CONAN_EXIT": "signal SIGSEGV"}); err != nil {
		t.Fatal(err)
	}
	output := rec.all()[0].Output
	for _, want := range []string{"CONAN_HOOK_EVENT=on_crash", "CONAN_SERVER_ID=alpha", "CONAN_QUERY_PORT=27015", "CONAN_EXIT=signal SIGSEGV", "CONAN_SAVED_DIR="} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %s in:\n%s", want, output)
		}
	}
	for _, secret := range []string{"hunter2", "k3y", "CONAN_MANAGER_"} {
		if strings.Contains(output, secret) {
			t.Errorf("hook saw %q", secret)
		}
	}
}

// A hung script and anything it started are killed at the timeout.
func TestTimeoutKillsTheScriptAndItsChildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child.pid")
	r, rec := testRunner(t, map[string]string{
		"hang": "#!/bin/sh\nsleep 30 &\necho $! > " + marker + "\nwait\n",
	})
	r.Timeout = 500 * time.Millisecond
	cfg := serverWith(t, map[string][]string{"pre_start": {"hang"}})
	started := time.Now()
	if err := r.Run(context.Background(), Gate, "pre_start", cfg, nil); err == nil {
		t.Fatal("a timed out pre_start hook must abort")
	}
	if time.Since(started) > 7*time.Second {
		t.Fatal("timeout not enforced")
	}
	if run := rec.all()[0]; !run.TimedOut {
		t.Fatalf("run not marked as timed out: %+v", run)
	}
	pid, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := exec.Command("kill", "-0", strings.TrimSpace(string(pid))).Run(); err == nil {
		t.Fatal("child of the timed out script is still running")
	}
}

func TestUnsafeOrMissingScriptsAreRefused(t *testing.T) {
	r, rec := testRunner(t, map[string]string{"real": "#!/bin/sh\nexit 0\n"})
	if err := os.WriteFile(filepath.Join(r.Dir, "not-executable"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/true", filepath.Join(r.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"not-executable", "link", "missing", "../escape", ".hidden"} {
		if err := r.Check(name); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	cfg := serverWith(t, map[string][]string{"pre_start": {"link"}})
	if err := r.Run(context.Background(), Gate, "pre_start", cfg, nil); err == nil {
		t.Fatal("refused script let the start through")
	}
	if runs := rec.all(); len(runs) != 1 || !strings.HasPrefix(runs[0].Error, "refused:") {
		t.Fatalf("refusal not recorded: %+v", runs)
	}
}

// The manager's own user must not be able to change what it executes.
func TestScriptsTheServiceUserCouldModifyAreRefused(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create root-owned and foreign-owned files")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(dir, time.Second)
	write := func(name string, mode os.FileMode, uid int) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, uid, uid); err != nil {
			t.Fatal(err)
		}
	}
	write("trusted", 0o755, 0)
	write("group-writable", 0o775, 0)
	write("foreign", 0o755, 65534)
	if err := r.Check("trusted"); err != nil {
		t.Fatalf("root-owned script refused: %v", err)
	}
	for _, name := range []string{"group-writable", "foreign"} {
		if err := r.Check(name); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := r.Check("trusted"); err == nil {
		t.Error("script in a world-writable directory accepted")
	}
}

func TestOutputKeepsTheTail(t *testing.T) {
	r, rec := testRunner(t, map[string]string{"noisy": "#!/bin/sh\ni=0\nwhile [ $i -lt 3000 ]; do echo line-$i; i=$((i+1)); done\necho LAST\n"})
	cfg := serverWith(t, map[string][]string{"post_start": {"noisy"}})
	if err := r.Run(context.Background(), Wait, "post_start", cfg, nil); err != nil {
		t.Fatal(err)
	}
	out := rec.all()[0].Output
	if len(out) > outputLimit+64 || !strings.HasSuffix(strings.TrimSpace(out), "LAST") || !strings.HasPrefix(out, "[output truncated]") {
		t.Fatalf("output not bounded to its tail: %d bytes", len(out))
	}
}
