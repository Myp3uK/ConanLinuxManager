package notify

import (
	"context"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/events"
)

// makeServers puts rows in the servers table, which the channel's servers
// reference.
func makeServers(t *testing.T, repo *Repository, ids ...string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		_, err := repo.db.Exec(`INSERT INTO servers
            (id,name,engine,runtime,install_dir,steamcmd_dir,branch,launch_args,game_port,query_port,
             ramdisk_enabled,ramdisk_path,ramdisk_size_mb,sync_interval_seconds,periodic_backup_interval_seconds,
             restart_on_crash,desired_state,hooks,auto_update,warnings,options,created_at,updated_at)
            VALUES (?,?,'ue5','native','/srv/`+id+`','/srv/steamcmd','public','',7777,27015,0,'',0,900,0,1,'stopped','{}',0,'{}','{}',?,?)`,
			id, id, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestChannelWatchesSeveralServers(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	makeServers(t, repo, "a", "b", "c")
	created, err := repo.Create(ctx, Input{
		Name: str("Two of three"), Kind: str(KindDiscord), WebhookURL: str(webhook),
		ServerIDs: &[]string{"a", "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ServerIDs) != 2 || got.ServerIDs[0] != "a" || got.ServerIDs[1] != "c" {
		t.Fatalf("server set not kept: %+v", got.ServerIDs)
	}
	// Narrowing the set is a plain edit, not an add.
	if _, err := repo.Update(ctx, created.ID, Input{ServerIDs: &[]string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if got, err = repo.Get(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if len(got.ServerIDs) != 1 || got.ServerIDs[0] != "b" {
		t.Fatalf("server set not replaced: %+v", got.ServerIDs)
	}
	// An empty set is every server, which is also the default.
	if _, err := repo.Update(ctx, created.ID, Input{ServerIDs: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	if got, err = repo.Get(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if len(got.ServerIDs) != 0 {
		t.Fatalf("an empty set should mean every server: %+v", got.ServerIDs)
	}
}

func TestDeletingAServerLeavesTheChannelWatchingTheRest(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	makeServers(t, repo, "a", "b")
	created, err := repo.Create(ctx, Input{
		Name: str("Both"), Kind: str(KindDiscord), WebhookURL: str(webhook),
		ServerIDs: &[]string{"a", "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`DELETE FROM servers WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	// Losing one server must not take the channel with it, which is what the
	// old single-column form did.
	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("the channel went with the server: %v", err)
	}
	if len(got.ServerIDs) != 1 || got.ServerIDs[0] != "b" {
		t.Fatalf("expected only b: %+v", got.ServerIDs)
	}
}

func TestChannelInputAcceptsTheOlderSingleServerForm(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	makeServers(t, repo, "a")
	created, err := repo.Create(ctx, Input{
		Name: str("One"), Kind: str(KindDiscord), WebhookURL: str(webhook), ServerID: str("a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ServerIDs) != 1 || created.ServerIDs[0] != "a" {
		t.Fatalf("server_id should still name one server: %+v", created.ServerIDs)
	}
	// And the blank form still means every server.
	all, err := repo.Create(ctx, Input{Name: str("All"), Kind: str(KindDiscord), WebhookURL: str(webhook), ServerID: str("")})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.ServerIDs) != 0 {
		t.Fatalf("an empty server_id is every server: %+v", all.ServerIDs)
	}
}

func TestDuplicatesAndBlanksAreDroppedFromTheSet(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	makeServers(t, repo, "a", "b")
	created, err := repo.Create(ctx, Input{
		Name: str("Messy"), Kind: str(KindDiscord), WebhookURL: str(webhook),
		ServerIDs: &[]string{"b", "", "a", "b", "  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ServerIDs) != 2 || created.ServerIDs[0] != "a" || created.ServerIDs[1] != "b" {
		t.Fatalf("set not cleaned: %+v", created.ServerIDs)
	}
}

func TestEventsReachOnlyTheWatchedServers(t *testing.T) {
	c := Channel{Enabled: true, Kind: KindDiscord, MinSeverity: "info", ServerIDs: []string{"a", "c"}}
	c.secret.URL = webhook
	for _, id := range []string{"a", "c"} {
		if !c.wants(events.Event{Type: events.ServerCrashed, Severity: events.Error, ServerID: id}) {
			t.Errorf("server %s is watched and should be taken", id)
		}
	}
	if c.wants(events.Event{Type: events.ServerCrashed, Severity: events.Error, ServerID: "b"}) {
		t.Error("server b is not watched")
	}
	// A channel watching nothing in particular takes every server.
	c.ServerIDs = nil
	for _, id := range []string{"a", "b", "zzz"} {
		if !c.wants(events.Event{Type: events.ServerCrashed, Severity: events.Error, ServerID: id}) {
			t.Errorf("an unrestricted channel should take %s", id)
		}
	}
}
