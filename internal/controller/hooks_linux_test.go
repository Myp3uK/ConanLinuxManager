package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/hooks"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

// hookDir writes root-owned scripts; the runner refuses anything else, so
// these tests need root, as they have on the Linux test host.
func hookDir(t *testing.T, scripts map[string]string) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("hook scripts must be root-owned")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFailingPreStartHookKeepsTheServerDown(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.Hooks = hooks.New(hookDir(t, map[string]string{"refuse": "#!/bin/sh\necho 'maintenance window' >&2\nexit 1\n"}), 5*time.Second)
	c.Hooks.Record = c.Repo.RecordHookRun
	game := fakeGame(t, "#!/bin/sh\nexec sleep 60\n")
	createServer(t, c, "a", game, true)
	ctx := context.Background()
	if _, err := c.UpdateConfig(ctx, "a", func(cfg *server.Config) {
		cfg.Hooks = map[string][]string{"pre_start": {"refuse"}}
	}); err != nil {
		t.Fatal(err)
	}
	err := c.Start(ctx, "a")
	if err == nil || !strings.Contains(err.Error(), "pre_start") {
		t.Fatalf("start went ahead: %v", err)
	}
	if c.Lifecycle("a").State != "idle" || c.Snapshot("a").PID != 0 {
		t.Fatalf("server started anyway: %+v %+v", c.Lifecycle("a"), c.Snapshot("a"))
	}
	runs, err := c.Repo.HookRuns(ctx, "a", 10)
	if err != nil || len(runs) != 1 || !strings.Contains(runs[0].Output, "maintenance window") {
		t.Fatalf("hook run not recorded: %+v %v", runs, err)
	}
}

func TestPostStopHookSeesHowTheServerExited(t *testing.T) {
	out := filepath.Join(t.TempDir(), "post_stop.env")
	c, _ := testController(t)
	c.Hooks = hooks.New(hookDir(t, map[string]string{"record": "#!/bin/sh\nenv > " + out + "\n"}), 5*time.Second)
	game := fakeGame(t, "#!/bin/sh\nexec sleep 60\n")
	createServer(t, c, "a", game, true)
	ctx := context.Background()
	if _, err := c.UpdateConfig(ctx, "a", func(cfg *server.Config) {
		cfg.Hooks = map[string][]string{"post_stop": {"record"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "post_stop hook", func() bool {
		data, err := os.ReadFile(out)
		return err == nil && strings.Contains(string(data), "CONAN_EXIT=")
	})
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "CONAN_HOOK_EVENT=post_stop") || !strings.Contains(string(data), "CONAN_EXIT_SIGNAL=SIGTERM") {
		t.Fatalf("post_stop environment:\n%s", data)
	}
}

// Saving a configuration that names a script which cannot run is refused at
// once, instead of surfacing at the next start.
func TestConfigurationNamingAMissingScriptIsRefused(t *testing.T) {
	c, _ := testController(t)
	c.Hooks = hooks.New(hookDir(t, map[string]string{}), time.Second)
	createServer(t, c, "a", t.TempDir(), true)
	_, err := c.UpdateConfig(context.Background(), "a", func(cfg *server.Config) {
		cfg.Hooks = map[string][]string{"pre_start": {"does-not-exist"}}
	})
	if err == nil {
		t.Fatal("missing script accepted")
	}
}
