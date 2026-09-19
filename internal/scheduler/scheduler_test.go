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
