package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/unitservice"
)

// unitHost pretends to be systemd for one server unit that survives the
// manager, which is the whole point of running servers as units.
type unitHost struct {
	mu     sync.Mutex
	active string
	calls  []string
}

func (h *unitHost) run(ctx context.Context, args ...string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, strings.Join(args, " "))
	switch args[0] {
	case "show":
		return fmt.Sprintf("LoadState=loaded\nActiveState=%s\nResult=success\nMainPID=777\nExecMainCode=1\nExecMainStatus=0\n", h.active), nil
	case "start":
		h.active = "active"
	case "stop":
		h.active = "inactive"
	case "kill":
		h.active = "failed"
	}
	return "", nil
}

func (h *unitHost) count(verb string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, call := range h.calls {
		if strings.HasPrefix(call, verb+" ") {
			n++
		}
	}
	return n
}

func (h *unitHost) stopped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, call := range h.calls {
		if strings.HasPrefix(call, "stop") || strings.HasPrefix(call, "kill") {
			return true
		}
	}
	return false
}

func unitController(t *testing.T, repo *server.Repository, host *unitHost, specDir string) *Controller {
	t.Helper()
	c := New(repo)
	c.Units = unitservice.New(specDir)
	c.Units.Run = host.run
	return c
}

func TestUnitServerSurvivesManagerShutdownAndIsAdopted(t *testing.T) {
	first, _ := testController(t)
	host := &unitHost{active: "inactive"}
	specDir := t.TempDir()
	first.Units = unitservice.New(specDir)
	first.Units.Run = host.run
	install := filepath.Join(t.TempDir(), "game")
	if err := os.MkdirAll(install, 0o750); err != nil {
		t.Fatal(err)
	}
	createServer(t, first, "a", install, true)
	ctx := context.Background()
	if err := first.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if first.Lifecycle("a").State != "running" {
		t.Fatalf("not running: %+v", first.Lifecycle("a"))
	}

	shutdown, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := first.Shutdown(shutdown); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if host.stopped() {
		t.Fatalf("manager shutdown stopped the game unit: %v", host.calls)
	}

	// The next manager finds the unit still running and takes it over rather
	// than starting a second copy.
	second := unitController(t, first.Repo, host, specDir)
	second.AdoptUnits(ctx)
	if second.Lifecycle("a").State != "running" {
		t.Fatalf("running unit not adopted: %+v", second.Lifecycle("a"))
	}
	second.Reconcile(ctx)
	if starts := host.count("start"); starts != 1 {
		t.Fatalf("unit started %d times, want exactly once: %v", starts, host.calls)
	}
	if err := second.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "adopted unit to stop", func() bool { return second.Lifecycle("a").State == "idle" })
}

// With units enabled, RAM-disk servers keep the in-process backend: the RAM
// workflow is deferred and was never built for units.
func TestRAMServersKeepTheProcessBackend(t *testing.T) {
	c, _ := testController(t)
	c.Units = unitservice.New(t.TempDir())
	if got := c.backendFor(server.Config{ID: "a", RamdiskEnabled: true}); got != backendProcess {
		t.Fatalf("RAM server backend = %s", got)
	}
	if got := c.backendFor(server.Config{ID: "a"}); got != backendUnit {
		t.Fatalf("plain server backend = %s", got)
	}
}

func TestStartRefusesWithoutTheGame(t *testing.T) {
	c, _ := testController(t)
	host := &unitHost{active: "inactive"}
	c.Units = unitservice.New(t.TempDir())
	c.Units.Run = host.run
	install := filepath.Join(t.TempDir(), "game")
	createServer(t, c, "a", install, true)
	if err := os.RemoveAll(filepath.Join(install, "ConanSandbox")); err != nil {
		t.Fatal(err)
	}
	err := c.Start(context.Background(), "a")
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("start without the game: %v", err)
	}
	if host.count("start") != 0 || c.Lifecycle("a").State != "idle" {
		t.Fatalf("unit started or lifecycle held: %v %+v", host.calls, c.Lifecycle("a"))
	}
}
