package events

import (
	"sync"
	"testing"
	"time"
)

func TestPublishDeliversInOrderAndKeepsHistory(t *testing.T) {
	bus := NewBus()
	var mu sync.Mutex
	var got []Type
	done := make(chan struct{})
	bus.Subscribe("test", func(e Event) {
		mu.Lock()
		got = append(got, e.Type)
		if len(got) == 2 {
			close(done)
		}
		mu.Unlock()
	})
	bus.Publish(Event{Type: ServerStarted, ServerID: "a"})
	bus.Publish(Event{Type: ServerCrashed, ServerID: "a"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("events not delivered")
	}
	mu.Lock()
	defer mu.Unlock()
	if got[0] != ServerStarted || got[1] != ServerCrashed {
		t.Fatalf("order: %v", got)
	}
	recent := bus.Recent(10)
	if len(recent) != 2 || recent[0].Type != ServerCrashed {
		t.Fatalf("recent must be newest first: %+v", recent)
	}
	if recent[0].Severity != Error || recent[1].Severity != Info {
		t.Fatalf("severity defaults: %+v", recent)
	}
	if recent[0].Time.IsZero() {
		t.Fatal("event not stamped")
	}
}

// A webhook that hangs must never stall a lifecycle operation, so a full
// subscriber queue drops events instead of blocking Publish.
func TestPublishNeverBlocksOnASlowSubscriber(t *testing.T) {
	bus := NewBus()
	release := make(chan struct{})
	bus.Subscribe("stuck", func(Event) { <-release })
	defer close(release)
	finished := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*3; i++ {
			bus.Publish(Event{Type: ServerStarted})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a stuck subscriber")
	}
	if bus.Dropped()["stuck"] == 0 {
		t.Fatal("loss was not counted")
	}
}

func TestNilBusIsSafe(t *testing.T) {
	var bus *Bus
	bus.Publish(Event{Type: ServerStarted})
	if bus.Recent(5) != nil {
		t.Fatal("nil bus returned events")
	}
}
