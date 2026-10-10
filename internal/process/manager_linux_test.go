package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

type testRuntime struct{}

func (testRuntime) Command(ctx context.Context, cfg server.Config) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "/bin/sh", "-c", "trap 'sleep 1; exit 0' TERM; touch ready; while :; do sleep 0.1; done"), nil
}

func TestConcurrentStartAndStopping(t *testing.T) {
	m := NewManager()
	m.runtime[server.RuntimeNative] = testRuntime{}
	cfg := server.Config{ID: "test", Runtime: server.RuntimeNative, InstallDir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- m.Start(ctx, cfg) }()
	}
	wg.Wait()
	close(results)
	count := 0
	for err := range results {
		if err == nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("started %d processes", count)
	}
	defer func() {
		_ = m.Stop(context.Background(), cfg.ID)
		wait, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = m.Wait(wait, cfg.ID)
	}()
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(cfg.InstallDir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if m.Status(cfg.ID) != StateRunning {
		t.Fatal("request cancellation killed server")
	}
	if err := m.Stop(context.Background(), cfg.ID); err != nil {
		t.Fatal(err)
	}
	if m.Status(cfg.ID) != StateStopping {
		t.Fatal("not stopping")
	}
	if err := m.Start(context.Background(), cfg); err == nil {
		t.Fatal("start accepted while stopping")
	}
	if err := m.Stop(context.Background(), cfg.ID); err != nil {
		t.Fatal(err)
	}
	wait, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if err := m.Wait(wait, cfg.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := m.Snapshot(cfg.ID)
	if snapshot.State != StateStopped || snapshot.PID != 0 || snapshot.Forced {
		t.Fatalf("bad final state: %+v", snapshot)
	}
	if err := m.Stop(context.Background(), cfg.ID); err != nil {
		t.Fatal(err)
	}
}
