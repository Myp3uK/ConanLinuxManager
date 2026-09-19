package watchdog

import (
	"errors"
	"testing"
	"time"
)

func TestAvailabilityGraceFailuresAndRecovery(t *testing.T) {
	start := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	a := NewAvailability("127.0.0.1:27015", start, DefaultAvailabilityPolicy())
	fail := errors.New("timeout")
	a.Record(start.Add(9*time.Minute), fail)
	if a.State != "starting" || a.Failures != 0 {
		t.Fatalf("startup: %+v", a)
	}
	a.Record(start.Add(10*time.Minute), fail)
	a.Record(start.Add(14*time.Minute+59*time.Second), fail)
	if a.WouldRecover || a.State != "suspect" {
		t.Fatalf("premature recovery: %+v", a)
	}
	a.Record(start.Add(15*time.Minute), fail)
	if !a.WouldRecover || a.State != "unavailable" || a.Mode != "observe" {
		t.Fatalf("outage: %+v", a)
	}
	a.Record(start.Add(16*time.Minute), nil)
	if a.WouldRecover || a.Failures != 0 || !a.FailureSince.IsZero() || a.Error != "" {
		t.Fatalf("not reset: %+v", a)
	}
	a.Record(start.Add(17*time.Minute), fail)
	if a.WouldRecover || a.Failures != 1 {
		t.Fatalf("old failures reused: %+v", a)
	}
}

func TestFirstSuccessEndsStartupGrace(t *testing.T) {
	now := time.Now()
	a := NewAvailability("localhost:27015", now, DefaultAvailabilityPolicy())
	a.Record(now.Add(time.Minute), nil)
	a.Record(now.Add(2*time.Minute), errors.New("timeout"))
	if a.State != "suspect" {
		t.Fatalf("unexpected grace after success: %+v", a)
	}
}
