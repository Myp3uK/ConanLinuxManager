package controller

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownWaitsForPersistenceAndSealsAdmission(t *testing.T) {
	c, create := testController(t)
	create("a", t.TempDir(), "")
	if _, err := c.acquire(context.Background(), "a", "flushing"); err != nil {
		t.Fatal(err)
	}
	c.health["a"] = Health{RestartPending: true}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Shutdown(ctx) }()
	for {
		c.lifecycleMu.Lock()
		closing := c.closing
		c.lifecycleMu.Unlock()
		if closing {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("shutdown did not seal admission")
		case <-time.After(time.Millisecond):
		}
	}
	if c.Health("a").RestartPending {
		t.Fatal("restart still pending")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before flush completed: %v", err)
	default:
	}
	c.release("a")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "a"); err == nil {
		t.Fatal("start admitted after shutdown")
	}
	if _, err := c.Install(ctx, "a"); err == nil {
		t.Fatal("install admitted after shutdown")
	}
}

func TestShutdownReportsRecoveryAndTimeout(t *testing.T) {
	c, create := testController(t)
	create("a", t.TempDir(), "")
	if _, err := c.acquire(context.Background(), "a", "flushing"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
	c.transition("a", "recovery_required", errors.New("disk full"))
	if err := c.Shutdown(context.Background()); err == nil {
		t.Fatal("recovery failure hidden")
	}
	if c.Lifecycle("a").State != "recovery_required" {
		t.Fatal("recovery lease lost")
	}
}
