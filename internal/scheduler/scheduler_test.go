package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeStore mirrors the repository's claim semantics in memory.
type fakeStore struct {
	mu          sync.Mutex
	schedules   map[string]*Schedule
	runs        map[int64]Run
	lastID      int64
	interrupted int
	claimErr    error
}

func newFakeStore(items ...Schedule) *fakeStore {
	f := &fakeStore{schedules: map[string]*Schedule{}, runs: map[int64]Run{}}
	for i := range items {
		item := items[i]
		f.schedules[item.ServerID] = &item
	}
	return f
}

func (f *fakeStore) ClaimDue(_ context.Context, now time.Time) ([]Claim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	var claims []Claim
	for _, item := range f.schedules {
		if !item.Enabled || item.NextRun.After(now) {
			continue
		}
		next, err := item.NextOccurrence(now)
		if err != nil {
			item.Enabled = false
			continue
		}
		snapshot := *item
		item.NextRun = next
		f.lastID++
		f.runs[f.lastID] = Run{ID: f.lastID, ServerID: item.ServerID, Action: item.Action, Status: "running", StartedAt: now}
		claims = append(claims, Claim{Schedule: snapshot, RunID: f.lastID})
	}
	return claims, nil
}

func (f *fakeStore) CompleteRun(_ context.Context, runID int64, finished time.Time, runErr error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.runs[runID]
	run.Status = "succeeded"
	if runErr != nil {
		run.Status = "failed"
		run.Error = runErr.Error()
	}
	run.FinishedAt = finished
	f.runs[runID] = run
	return nil
}

func (f *fakeStore) Retry(_ context.Context, serverID string, at time.Time, failures int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if item := f.schedules[serverID]; item != nil {
		item.NextRun = at
		item.FailureCount = failures
	}
	return nil
}

func (f *fakeStore) ClearFailures(_ context.Context, serverID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if item := f.schedules[serverID]; item != nil {
		item.FailureCount = 0
	}
	return nil
}

func (f *fakeStore) MarkInterrupted(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.interrupted, nil
}

func (f *fakeStore) snapshot(id string) Schedule {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.schedules[id]
}

func (f *fakeStore) history() []Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]Run, 0, len(f.runs))
	for _, run := range f.runs {
		result = append(result, run)
	}
	return result
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

// A cron or daily schedule has no interval to give, so demanding one was a bug.
func TestNonIntervalModesDoNotRequireInterval(t *testing.T) {
	cron := Schedule{ServerID: "one", Action: "restart", Mode: "cron", CronExpression: "0 5 * * *", NextRun: time.Now()}
	if err := cron.Validate(); err != nil {
		t.Fatalf("cron without interval rejected: %v", err)
	}
	if cron.Timezone != "UTC" {
		t.Fatalf("timezone default not applied: %q", cron.Timezone)
	}
	daily := Schedule{ServerID: "one", Action: "stop", Mode: "daily", DailyTime: "04:30", NextRun: time.Now()}
	if err := daily.Validate(); err != nil {
		t.Fatalf("daily without interval rejected: %v", err)
	}
	// Validate normalizes in place, so a caller storing the value keeps defaults.
	if daily.IntervalSeconds != 0 {
		t.Fatalf("stale interval retained: %d", daily.IntervalSeconds)
	}
	interval := Schedule{ServerID: "one", Action: "stop", Mode: "interval", IntervalSeconds: 30, NextRun: time.Now()}
	if err := interval.Validate(); err == nil {
		t.Fatal("interval below minimum accepted")
	}
}

func TestValidateRejectsImpossibleCron(t *testing.T) {
	s := Schedule{ServerID: "one", Action: "stop", Mode: "cron", CronExpression: "0 0 30 2 *", NextRun: time.Now()}
	if err := s.Validate(); err == nil {
		t.Fatal("30 February accepted")
	}
}

func TestRunnerRecordsFailureAndSchedulesRetry(t *testing.T) {
	store := newFakeStore(Schedule{ServerID: "one", Action: "stop", Mode: "interval", Enabled: true, IntervalSeconds: 60, NextRun: time.Unix(100, 0)})
	runner := &Runner{
		Schedules: store,
		Execute:   func(context.Context, Schedule) error { return context.DeadlineExceeded },
	}
	if err := runner.RunOnce(context.Background(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	runner.Wait()

	runs := store.history()
	if len(runs) != 1 || runs[0].Status != "failed" || runs[0].Error != context.DeadlineExceeded.Error() {
		t.Fatalf("history: %+v", runs)
	}
	item := store.snapshot("one")
	if item.FailureCount != 1 {
		t.Fatalf("failure count: %d", item.FailureCount)
	}
	// First retry uses the base delay rather than the natural interval.
	if !item.NextRun.Equal(time.Unix(100, 0).Add(time.Minute)) {
		t.Fatalf("retry next run: %v", item.NextRun)
	}
}

func TestRunnerDoesNotRetryPermanentFailures(t *testing.T) {
	store := newFakeStore(Schedule{ServerID: "one", Action: "stop", Mode: "interval", Enabled: true, IntervalSeconds: 60, NextRun: time.Unix(100, 0)})
	runner := &Runner{
		Schedules: store,
		Execute:   func(context.Context, Schedule) error { return Permanent(errors.New("server deleted")) },
	}
	if err := runner.RunOnce(context.Background(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	item := store.snapshot("one")
	if item.FailureCount != 0 {
		t.Fatalf("permanent failure counted for retry: %d", item.FailureCount)
	}
	// next_run stays on the natural occurrence claimed before the action ran.
	if !item.NextRun.Equal(time.Unix(160, 0)) {
		t.Fatalf("next run: %v", item.NextRun)
	}
}

func TestRunnerStopsRetryingAfterMaxAttempts(t *testing.T) {
	store := newFakeStore(Schedule{ServerID: "one", Action: "stop", Mode: "interval", Enabled: true, IntervalSeconds: 60, FailureCount: 3, NextRun: time.Unix(100, 0)})
	runner := &Runner{
		Schedules:  store,
		MaxRetries: 3,
		Execute:    func(context.Context, Schedule) error { return errors.New("still failing") },
	}
	if err := runner.RunOnce(context.Background(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	item := store.snapshot("one")
	if item.FailureCount != 0 {
		t.Fatalf("retry counter not reset after exhaustion: %d", item.FailureCount)
	}
	if !item.NextRun.Equal(time.Unix(160, 0)) {
		t.Fatalf("next run not back on schedule: %v", item.NextRun)
	}
}

func TestRunnerClearsFailureCountAfterSuccess(t *testing.T) {
	store := newFakeStore(Schedule{ServerID: "one", Action: "start", Mode: "interval", Enabled: true, IntervalSeconds: 60, FailureCount: 2, NextRun: time.Unix(100, 0)})
	runner := &Runner{Schedules: store, Execute: func(context.Context, Schedule) error { return nil }}
	if err := runner.RunOnce(context.Background(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	if got := store.snapshot("one").FailureCount; got != 0 {
		t.Fatalf("failure count after success: %d", got)
	}
}

// A slow action must not hold back the other servers' schedules.
func TestRunnerExecutesSchedulesConcurrently(t *testing.T) {
	store := newFakeStore(
		Schedule{ServerID: "slow", Action: "restart", Mode: "interval", Enabled: true, IntervalSeconds: 60, NextRun: time.Unix(100, 0)},
		Schedule{ServerID: "fast", Action: "start", Mode: "interval", Enabled: true, IntervalSeconds: 60, NextRun: time.Unix(100, 0)},
	)
	release := make(chan struct{})
	started := make(chan string, 2)
	runner := &Runner{
		Schedules: store,
		Execute: func(_ context.Context, item Schedule) error {
			started <- item.ServerID
			if item.ServerID == "slow" {
				<-release
			}
			return nil
		},
	}
	if err := runner.RunOnce(context.Background(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	// Both actions must reach Execute before the slow one is released.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("second schedule blocked behind the slow one")
		}
	}
	close(release)
	runner.Wait()
}

// The same server must not get a second action while the first is running.
func TestRunnerSkipsOverlappingRunForSameServer(t *testing.T) {
	store := newFakeStore(Schedule{ServerID: "one", Action: "restart", Mode: "interval", Enabled: true, IntervalSeconds: 60, NextRun: time.Unix(100, 0)})
	release := make(chan struct{})
	var executions int
	var mu sync.Mutex
	runner := &Runner{
		Schedules: store,
		Execute: func(context.Context, Schedule) error {
			mu.Lock()
			executions++
			mu.Unlock()
			<-release
			return nil
		},
	}
	if err := runner.RunOnce(context.Background(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	// Force the schedule due again while the first action is still running.
	store.mu.Lock()
	store.schedules["one"].NextRun = time.Unix(160, 0)
	store.mu.Unlock()
	if err := runner.RunOnce(context.Background(), time.Unix(160, 0)); err != nil {
		t.Fatal(err)
	}
	close(release)
	runner.Wait()

	mu.Lock()
	got := executions
	mu.Unlock()
	if got != 1 {
		t.Fatalf("overlapping execution: %d", got)
	}
	var overlapRecorded bool
	for _, run := range store.history() {
		if run.Status == "failed" && run.Error != "" {
			overlapRecorded = true
		}
	}
	if !overlapRecorded {
		t.Fatal("overlap was not recorded in history")
	}
}

func TestRunWaitsForInFlightActionOnShutdown(t *testing.T) {
	store := newFakeStore(Schedule{ServerID: "one", Action: "stop", Mode: "interval", Enabled: true, IntervalSeconds: 60, NextRun: time.Unix(0, 0)})
	finished := make(chan struct{})
	runner := &Runner{
		Schedules: store,
		Execute: func(context.Context, Schedule) error {
			time.Sleep(200 * time.Millisecond)
			close(finished)
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()

	// Let the first tick claim the schedule, then shut down mid-action.
	<-time.After(1100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	select {
	case <-finished:
	default:
		t.Fatal("Run returned before the in-flight action finished")
	}
}

func TestRetryDelay(t *testing.T) {
	if got := RetryDelay(time.Minute, 3); got != 4*time.Minute {
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
		got, err := s.NextOccurrence(now)
		if err != nil {
			t.Fatal(err)
		}
		if got.Format(time.RFC3339) != tc.want {
			t.Fatalf("%s: got %s want %s", tc.now, got.Format(time.RFC3339), tc.want)
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
	got, err := cron.Next(now, location)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format(time.RFC3339) != "2026-09-21T01:00:00Z" {
		t.Fatalf("next: %s", got.Format(time.RFC3339))
	}
	if _, err := ParseCron("61 * * * *"); err == nil {
		t.Fatal("invalid minute accepted")
	}
	if _, err := ParseCron("0 3 * *"); err == nil {
		t.Fatal("wrong field count accepted")
	}
}

func TestCronScheduleValidation(t *testing.T) {
	s := Schedule{ServerID: "one", Action: "restart", Mode: "cron", CronExpression: "0 3 * * *", Timezone: "UTC", NextRun: time.Now()}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Timezone = "Not/AZone"
	if err := s.Validate(); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}

// Across a spring forward the named local time does not exist. The job must
// still run that day instead of silently skipping it.
func TestCronSurvivesSpringForward(t *testing.T) {
	cron, err := ParseCron("30 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	// 2027-03-28 02:30 Berlin does not exist: the clock jumps 02:00 to 03:00.
	now, _ := time.Parse(time.RFC3339, "2027-03-27T12:00:00Z")
	got, err := cron.Next(now, berlin)
	if err != nil {
		t.Fatal(err)
	}
	local := got.In(berlin)
	if local.Day() != 28 || local.Month() != time.March {
		t.Fatalf("spring forward skipped the day: %s", local.Format(time.RFC3339))
	}
	if !got.After(now) {
		t.Fatalf("next occurrence is not in the future: %s", got)
	}
}

// Across a fall back the local hour repeats. The job must run once, not twice.
func TestCronRunsOnceAcrossFallBack(t *testing.T) {
	cron, err := ParseCron("30 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-10-24T12:00:00Z")
	first, err := cron.Next(now, berlin)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cron.Next(first, berlin)
	if err != nil {
		t.Fatal(err)
	}
	if gap := second.Sub(first); gap < 23*time.Hour {
		t.Fatalf("fired twice within the repeated hour: %s then %s", first, second)
	}
}

func TestCronLeapDay(t *testing.T) {
	cron, err := ParseCron("0 4 29 2 *")
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2027-03-01T00:00:00Z")
	got, err := cron.Next(now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format(time.RFC3339) != "2028-02-29T04:00:00Z" {
		t.Fatalf("leap day: %s", got.Format(time.RFC3339))
	}
}

func TestCronImpossibleDateReportsError(t *testing.T) {
	cron, err := ParseCron("0 0 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cron.Next(time.Now().UTC(), time.UTC); err == nil {
		t.Fatal("31 February returned an occurrence")
	}
}

// With both day-of-month and day-of-week restricted, cron matches either.
func TestCronDayOfMonthOrWeekday(t *testing.T) {
	cron, err := ParseCron("0 0 13 * 5")
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-11-01T00:00:00Z")
	got, err := cron.Next(now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// The 6th is the first Friday in November 2026, ahead of the 13th.
	if got.Format(time.RFC3339) != "2026-11-06T00:00:00Z" {
		t.Fatalf("day-of-month/weekday union: %s", got.Format(time.RFC3339))
	}
}

func TestCronSteps(t *testing.T) {
	cron, err := ParseCron("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-09-20T10:01:00Z")
	got, err := cron.Next(now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format(time.RFC3339) != "2026-09-20T10:15:00Z" {
		t.Fatalf("step: %s", got.Format(time.RFC3339))
	}
}

func TestRunnerSkipsMissedIntervalsAfterDowntime(t *testing.T) {
	store := newFakeStore(Schedule{ServerID: "one", Action: "start", Mode: "interval", Enabled: true, IntervalSeconds: 60, NextRun: time.Unix(1, 0)})
	var executions int
	var mu sync.Mutex
	runner := &Runner{
		Schedules: store,
		Execute: func(context.Context, Schedule) error {
			mu.Lock()
			executions++
			mu.Unlock()
			return nil
		},
	}
	if err := runner.RunOnce(context.Background(), time.Unix(86400, 0)); err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	mu.Lock()
	got := executions
	mu.Unlock()
	if got != 1 {
		t.Fatalf("missed intervals replayed: %d", got)
	}
	if next := store.snapshot("one").NextRun; !next.Equal(time.Unix(86460, 0)) {
		t.Fatalf("next run was not anchored to current time: %v", next)
	}
}

func TestRunOnceReportsStoreFailure(t *testing.T) {
	store := newFakeStore()
	store.claimErr = fmt.Errorf("database is locked")
	runner := &Runner{Schedules: store, Execute: func(context.Context, Schedule) error { return nil }}
	if err := runner.RunOnce(context.Background(), time.Now()); err == nil {
		t.Fatal("claim failure was swallowed")
	}
}
