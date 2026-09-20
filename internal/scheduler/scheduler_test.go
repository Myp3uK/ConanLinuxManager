package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeSchedules struct {
	mu      sync.Mutex
	items   []Schedule
	advance []time.Time
}

func (f *fakeSchedules) Due(context.Context, time.Time) ([]Schedule, error) { return f.items, nil }
func (f *fakeSchedules) Advance(_ context.Context, _ string, next time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advance = append(f.advance, next)
	return nil
}

func TestScheduleValidate(t *testing.T) {
	valid := Schedule{ServerID: "one", Action: "restart", IntervalSeconds: 60, NextRun: time.Now()}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.Action = "unknown"
	if err := valid.Validate(); err == nil {
		t.Fatal("invalid action accepted")
	}
}

func TestRunnerRecordsFailuresAndAdvances(t *testing.T) {
	store := &fakeSchedules{items: []Schedule{{ServerID: "one", Action: "stop", IntervalSeconds: 60, NextRun: time.Now()}}}
	recorded := make(chan error, 1)
	runner := &Runner{
		Schedules: store,
		Execute:   func(context.Context, Schedule) error { return context.DeadlineExceeded },
		Record: func(_ context.Context, _ Schedule, _, _ time.Time, err error) error {
			recorded <- err
			return nil
		},
	}
	if err := runner.RunOnce(context.Background(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := <-recorded; err != context.DeadlineExceeded {
		t.Fatalf("recorded error: %v", err)
	}
	store.mu.Lock()
	advanced := len(store.advance)
	next := store.advance[0]
	store.mu.Unlock()
	if advanced != 1 {
		t.Fatalf("advance count: %d", advanced)
	}
	if !next.Equal(time.Unix(160, 0)) {
		t.Fatalf("retry next run: %v", next)
	}
}

func TestRetryDelay(t *testing.T) {
	if got := retryDelay(time.Minute, 3); got != 4*time.Minute {
		t.Fatalf("delay: %v", got)
	}
}
