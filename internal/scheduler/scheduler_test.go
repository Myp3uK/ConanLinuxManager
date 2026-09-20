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

func TestDailyOccurrence(t *testing.T) {
	s := Schedule{Mode: "daily", DailyTime: "03:00"}
	for _, tc := range []struct{ now, want string }{
		{"2026-09-20T02:59:59Z", "2026-09-20T03:00:00Z"},
		{"2026-09-20T03:00:00Z", "2026-09-21T03:00:00Z"},
		{"2026-12-31T23:00:00Z", "2027-01-01T03:00:00Z"},
		{"2026-09-20T05:30:00+03:00", "2026-09-20T03:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := s.NextOccurrence(now).Format(time.RFC3339); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.now, got, tc.want)
		}
	}
}

func TestCronParsingAndNext(t *testing.T) {
	cron, err := ParseCron("0 3 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-09-18T01:00:00Z")
	if got := cron.Next(now, location); got.Format(time.RFC3339) != "2026-09-21T01:00:00Z" {
		t.Fatalf("next: %s", got)
	}
	if _, err := ParseCron("61 * * * *"); err == nil {
		t.Fatal("invalid minute accepted")
	}
	if _, err := ParseCron("0 3 * *"); err == nil {
		t.Fatal("wrong field count accepted")
	}
}

func TestCronScheduleValidation(t *testing.T) {
	s := Schedule{ServerID: "one", Action: "restart", Mode: "cron", CronExpression: "0 3 * * *", Timezone: "UTC", IntervalSeconds: 60, NextRun: time.Now()}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Timezone = "Not/AZone"
	if err := s.Validate(); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}

func TestRunnerSkipsMissedIntervalsAfterDowntime(t *testing.T) {
	store := &fakeSchedules{items: []Schedule{{ServerID: "one", Action: "start", IntervalSeconds: 60, NextRun: time.Unix(1, 0)}}}
	executions := 0
	runner := &Runner{
		Schedules: store,
		Execute: func(context.Context, Schedule) error {
			executions++
			return nil
		},
	}
	if err := runner.RunOnce(context.Background(), time.Unix(86400, 0)); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("missed intervals replayed: %d", executions)
	}
	store.mu.Lock()
	next := store.advance[0]
	store.mu.Unlock()
	if !next.Equal(time.Unix(86460, 0)) {
		t.Fatalf("next run was not anchored to current time: %v", next)
	}
}
