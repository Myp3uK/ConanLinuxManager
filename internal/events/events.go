// Package events carries lifecycle notices from the controller to whatever
// reports them: the event history, Discord, the web UI. Publishing never waits
// for a subscriber, because a slow webhook must not delay a server stop.
package events

import (
	"sync"
	"sync/atomic"
	"time"
)

type Type string

const (
	ServerStarting         Type = "server.starting"
	ServerStarted          Type = "server.started"
	ServerReady            Type = "server.ready"
	ServerStartFailed      Type = "server.start_failed"
	ServerStopping         Type = "server.stopping"
	ServerStopped          Type = "server.stopped"
	ServerKilled           Type = "server.killed"
	ServerCrashed          Type = "server.crashed"
	ServerRestarting       Type = "server.restarting"
	ServerRestartAbandoned Type = "server.restart_abandoned"
	ServerHang             Type = "server.hang"
	ServerWatchdogStop     Type = "server.watchdog_stop"
	ServerUnavailable      Type = "server.unavailable"
	ServerRecovery         Type = "server.recovery_required"
	ScheduleFailed         Type = "schedule.failed"
	HookFailed             Type = "hook.failed"
	UpdateAvailable        Type = "update.available"
	UpdateStarted          Type = "update.started"
	UpdateFinished         Type = "update.finished"
	UpdateFailed           Type = "update.failed"
	BackupStarted          Type = "backup.started"
	BackupFinished         Type = "backup.finished"
	BackupFailed           Type = "backup.failed"
	BackupRestored         Type = "backup.restored"
	ConfigChanged          Type = "config.changed"
	DatabaseVerified       Type = "database.verified"
	DatabaseCorrupt        Type = "database.corrupt"
	PlayersWarned          Type = "players.warned"
	RconFailed             Type = "rcon.failed"
)

// Severity lets a subscriber filter without knowing every event type.
type Severity string

const (
	Info    Severity = "info"
	Warning Severity = "warning"
	Error   Severity = "error"
)

type Event struct {
	Type       Type              `json:"type"`
	Severity   Severity          `json:"severity"`
	ServerID   string            `json:"server_id,omitempty"`
	ServerName string            `json:"server_name,omitempty"`
	Time       time.Time         `json:"time"`
	Message    string            `json:"message"`
	Details    map[string]string `json:"details,omitempty"`
}

const subscriberBuffer = 256

type subscriber struct {
	name    string
	queue   chan Event
	dropped atomic.Int64
}

// Bus fans events out to subscribers, each on its own goroutine with a bounded
// queue. A subscriber that falls behind loses events rather than blocking the
// publisher; the loss is counted so it can be reported.
type Bus struct {
	mu          sync.Mutex
	subscribers []*subscriber
	recent      []Event
	keep        int
	now         func() time.Time
}

func NewBus() *Bus { return &Bus{keep: 500, now: time.Now} }

// Subscribe delivers every later event to handle, in order, on a dedicated
// goroutine.
func (b *Bus) Subscribe(name string, handle func(Event)) {
	s := &subscriber{name: name, queue: make(chan Event, subscriberBuffer)}
	b.mu.Lock()
	b.subscribers = append(b.subscribers, s)
	b.mu.Unlock()
	go func() {
		for event := range s.queue {
			handle(event)
		}
	}()
}

// Publish stamps and distributes an event. It is safe on a nil bus, so code
// paths that run without notifications need no guard.
func (b *Bus) Publish(event Event) {
	if b == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = b.now().UTC()
	}
	if event.Severity == "" {
		event.Severity = defaultSeverity(event.Type)
	}
	b.mu.Lock()
	b.recent = append(b.recent, event)
	if len(b.recent) > b.keep {
		b.recent = append([]Event(nil), b.recent[len(b.recent)-b.keep:]...)
	}
	subscribers := append([]*subscriber(nil), b.subscribers...)
	b.mu.Unlock()
	for _, s := range subscribers {
		select {
		case s.queue <- event:
		default:
			s.dropped.Add(1)
		}
	}
}

// Recent returns up to limit of the newest events, newest first.
func (b *Bus) Recent(limit int) []Event {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit <= 0 || limit > len(b.recent) {
		limit = len(b.recent)
	}
	result := make([]Event, 0, limit)
	for i := len(b.recent) - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, b.recent[i])
	}
	return result
}

// Dropped reports how many events each subscriber lost to a full queue.
func (b *Bus) Dropped() map[string]int64 {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make(map[string]int64, len(b.subscribers))
	for _, s := range b.subscribers {
		result[s.name] = s.dropped.Load()
	}
	return result
}

func defaultSeverity(t Type) Severity {
	switch t {
	case ServerCrashed, ServerRestartAbandoned, ServerRecovery, ServerStartFailed,
		ScheduleFailed, HookFailed, UpdateFailed, BackupFailed, DatabaseCorrupt:
		return Error
	case ServerHang, ServerWatchdogStop, ServerUnavailable, ServerKilled, ServerRestarting, UpdateAvailable, RconFailed:
		return Warning
	}
	return Info
}
