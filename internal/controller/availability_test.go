package controller

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/watchdog"
)

func TestUnavailableObservationDoesNotStopLifecycle(t *testing.T) {
	c, create := testController(t)
	// Bound UDP socket deliberately never answers queries.
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := create("observe", filepath.Join(t.TempDir(), "game"), "")
	cfg.QueryPort = listener.LocalAddr().(*net.UDPAddr).Port
	c.AvailabilityPolicy = watchdog.AvailabilityPolicy{Interval: 10 * time.Millisecond, Timeout: 10 * time.Millisecond, StartupGrace: time.Nanosecond, UnavailableFor: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.acquire(ctx, cfg.ID, "running"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); c.observeAvailability(ctx, cfg) }()
	deadline := time.Now().Add(5 * time.Second)
	for !c.Health(cfg.ID).Availability.WouldRecover {
		if time.Now().After(deadline) {
			t.Fatal("outage not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c.Lifecycle(cfg.ID).State != "running" || c.Health(cfg.ID).RestartPending {
		t.Fatal("observation changed lifecycle")
	}
	c.transition(cfg.ID, "flushing", nil)
	deadline = time.Now().Add(time.Second)
	for c.Health(cfg.ID).Availability.State != "suspended" {
		if time.Now().After(deadline) {
			t.Fatal("observation not suspended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer did not exit")
	}
	if a := c.Health(cfg.ID).Availability; a.State != "inactive" || a.WouldRecover {
		t.Fatalf("stale state: %+v", a)
	}
}
