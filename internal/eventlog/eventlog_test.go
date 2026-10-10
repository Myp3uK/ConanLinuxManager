package eventlog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/store"
)

func openLog(t *testing.T) *Log {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db.DB())
}

func TestHistoryIsStoredAndPaged(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()
	for i, typ := range []events.Type{events.ServerStarted, events.ServerCrashed, events.ServerRestarting} {
		e := events.Event{Type: typ, Severity: events.Info, ServerID: "a", ServerName: "Alpha", Time: time.Now().Add(time.Duration(i) * time.Second), Message: string(typ),
			Details: map[string]string{"attempt": "1/3"}}
		if err := log.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Record(ctx, events.Event{Type: events.ServerStarted, Severity: events.Info, ServerID: "b", Time: time.Now(), Message: "other"}); err != nil {
		t.Fatal(err)
	}
	page, err := log.List(ctx, Query{ServerID: "a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Type != events.ServerRestarting || page[1].Type != events.ServerCrashed || page[0].Details["attempt"] != "1/3" {
		t.Fatalf("first page: %+v", page)
	}
	next, err := log.List(ctx, Query{ServerID: "a", Before: page[1].ID})
	if err != nil || len(next) != 1 || next[0].Type != events.ServerStarted {
		t.Fatalf("second page: %+v %v", next, err)
	}
}

func TestRetentionKeepsTheNewest(t *testing.T) {
	log := openLog(t)
	log.MaxRows = 3
	ctx := context.Background()
	old := events.Event{Type: events.ServerStarted, Severity: events.Info, Time: time.Now().Add(-200 * 24 * time.Hour), Message: "ancient"}
	if err := log.Record(ctx, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := log.Record(ctx, events.Event{Type: events.ServerStarted, Severity: events.Info, Time: time.Now(), Message: "recent"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := log.List(ctx, Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("kept %d entries, want 3", len(all))
	}
	for _, e := range all {
		if e.Message == "ancient" {
			t.Fatal("expired entry kept")
		}
	}
}
