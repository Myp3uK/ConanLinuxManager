package watchdog

import (
	"errors"
	"testing"
	"time"
)

// Zero turns the loading allowance and the unavailable mark off.
func TestZeroTurnsTheLimitsOff(t *testing.T) {
	start := time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)
	silent := errors.New("timeout")
	a := NewAvailability("x", start, AvailabilityPolicy{StartupGrace: 0, UnavailableFor: 5 * time.Minute})
	a.Record(start.Add(3*time.Hour), silent)
	if a.State != "starting" {
		t.Fatalf("no loading limit, yet %s", a.State)
	}
	b := NewAvailability("x", start, AvailabilityPolicy{StartupGrace: time.Minute, UnavailableFor: 0})
	b.Record(start.Add(2*time.Minute), silent)
	b.Record(start.Add(3*time.Hour), silent)
	if b.State != "suspect" || b.WouldRecover {
		t.Fatalf("no unavailable mark, yet %s %v", b.State, b.WouldRecover)
	}
}
