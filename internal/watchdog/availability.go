package watchdog

import "time"

type AvailabilityPolicy struct {
	Interval       time.Duration
	Timeout        time.Duration
	StartupGrace   time.Duration
	UnavailableFor time.Duration
}

func DefaultAvailabilityPolicy() AvailabilityPolicy {
	return AvailabilityPolicy{15 * time.Second, 3 * time.Second, 10 * time.Minute, 5 * time.Minute}
}

type Availability struct {
	Mode             string    `json:"mode"`
	State            string    `json:"state"`
	Endpoint         string    `json:"endpoint"`
	CheckedAt        time.Time `json:"checked_at,omitempty"`
	LastSuccess      time.Time `json:"last_success,omitempty"`
	FailureSince     time.Time `json:"failure_since,omitempty"`
	Failures         int       `json:"consecutive_failures"`
	WouldRecover     bool      `json:"would_recover"`
	Error            string    `json:"error,omitempty"`
	StartupUntil     time.Time `json:"startup_until"`
	ThresholdSeconds int64     `json:"threshold_seconds"`
}

// NewAvailability starts watching a server. A zero StartupGrace leaves the
// loading time unlimited (StartupUntil stays zero); a zero UnavailableFor
// never marks the server unavailable.
func NewAvailability(endpoint string, now time.Time, policy AvailabilityPolicy) Availability {
	a := Availability{Mode: "observe", State: "starting", Endpoint: endpoint,
		ThresholdSeconds: int64(policy.UnavailableFor / time.Second)}
	if policy.StartupGrace > 0 {
		a.StartupUntil = now.Add(policy.StartupGrace)
	}
	return a
}

func (a *Availability) Record(now time.Time, err error) {
	a.CheckedAt = now
	if err == nil {
		a.State, a.LastSuccess, a.Error = "responsive", now, ""
		a.FailureSince, a.Failures, a.WouldRecover = time.Time{}, 0, false
		return
	}
	a.Error = err.Error()
	if a.LastSuccess.IsZero() && (a.StartupUntil.IsZero() || now.Before(a.StartupUntil)) {
		a.State = "starting"
		return
	}
	if a.FailureSince.IsZero() {
		a.FailureSince = now
	}
	a.Failures++
	a.State = "suspect"
	a.WouldRecover = a.ThresholdSeconds > 0 && now.Sub(a.FailureSince) >= time.Duration(a.ThresholdSeconds)*time.Second
	if a.WouldRecover {
		a.State = "unavailable"
	}
}
