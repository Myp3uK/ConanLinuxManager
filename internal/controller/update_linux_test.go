package controller

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
	"github.com/Myp3uK/ConanLinuxManager/internal/steamcmd"
)

// fakeSteam publishes a build and "installs" it by writing the manifest.
type fakeSteam struct {
	mu      sync.Mutex
	latest  string
	fail    error
	updates int
}

func writeManifest(t *testing.T, install, build string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(install, "steamapps"), 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := "\"AppState\"\n{\n\t\"buildid\"\t\t\"" + build + "\"\n}\n"
	if err := os.WriteFile(filepath.Join(install, "steamapps", "appmanifest_443030.acf"), []byte(manifest), 0o640); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeSteam) InstallServer(ctx context.Context, cfg server.Config, output io.Writer) error {
	return nil
}

func (f *fakeSteam) LatestBuild(ctx context.Context, dir, branch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latest, nil
}

func (f *fakeSteam) UpdateServer(ctx context.Context, dir, install, branch string, validate bool, out io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if f.fail != nil {
		return f.fail
	}
	manifest := "\"AppState\"\n{\n\t\"buildid\"\t\t\"" + f.latest + "\"\n}\n"
	return os.WriteFile(filepath.Join(install, "steamapps", "appmanifest_443030.acf"), []byte(manifest), 0o640)
}

func (f *fakeSteam) DownloadWorkshopItems(_ context.Context, _, contentDir string, ids []string, _ bool, _ io.Writer) (map[string]steamcmd.WorkshopResult, error) {
	results := map[string]steamcmd.WorkshopResult{}
	for _, id := range ids {
		results[id] = steamcmd.WorkshopResult{Dir: filepath.Join(contentDir, id)}
	}
	return results, nil
}

func waitJob(t *testing.T, c *Controller, id string) Job {
	t.Helper()
	var job Job
	waitFor(t, 20*time.Second, "update job", func() bool {
		job, _ = c.Job(id)
		return job.Status != "running"
	})
	return job
}

func TestUpdateStopsUpdatesAndStartsARunningServer(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	steam := &fakeSteam{latest: "200"}
	c.Steam = steam
	game := fakeGame(t, "#!/bin/sh\nexec sleep 60\n")
	writeManifest(t, game, "100")
	createServer(t, c, "a", game, true)
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	status, err := c.CheckUpdate(ctx, "a")
	if err != nil || !status.Available || status.Installed != "100" || status.Latest != "200" {
		t.Fatalf("status: %+v %v", status, err)
	}
	job, err := c.UpdateJob(ctx, "a", UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if done := waitJob(t, c, job.ID); done.Status != "completed" {
		t.Fatalf("job: %+v", done)
	}
	if c.Lifecycle("a").State != "running" {
		t.Fatalf("server not running after update: %+v", c.Lifecycle("a"))
	}
	t.Cleanup(func() { _ = c.Stop(ctx, "a"); _ = c.Processes.Wait(ctx, "a") })
	if countEvents(c.Events, "a", events.UpdateFinished) != 1 || countEvents(c.Events, "a", events.ServerStopped) != 1 {
		t.Fatalf("events: %+v", c.Events.Recent(0))
	}
	c.latest.entries = nil
	if status, _ := c.CheckUpdate(ctx, "a"); status.Available || status.Installed != "200" {
		t.Fatalf("after update: %+v", status)
	}
}

// A failed update must not leave a server down that was running before.
func TestFailedUpdateStillBringsTheServerBack(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	c.Steam = &fakeSteam{latest: "200", fail: errors.New("steam is down")}
	game := fakeGame(t, "#!/bin/sh\nexec sleep 60\n")
	writeManifest(t, game, "100")
	createServer(t, c, "a", game, true)
	ctx := context.Background()
	if err := c.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	job, err := c.UpdateJob(ctx, "a", UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if done := waitJob(t, c, job.ID); done.Status != "failed" {
		t.Fatalf("job: %+v", done)
	}
	if c.Lifecycle("a").State != "running" {
		t.Fatalf("server left down after a failed update: %+v", c.Lifecycle("a"))
	}
	t.Cleanup(func() { _ = c.Stop(ctx, "a"); _ = c.Processes.Wait(ctx, "a") })
	if countEvents(c.Events, "a", events.UpdateFailed) != 1 {
		t.Fatal("failure not reported")
	}
}

func TestUpToDateServerIsNotTouched(t *testing.T) {
	c, _ := testController(t)
	c.Events = events.NewBus()
	steam := &fakeSteam{latest: "100"}
	c.Steam = steam
	game := fakeGame(t, "#!/bin/sh\nexec sleep 60\n")
	writeManifest(t, game, "100")
	createServer(t, c, "a", game, true)
	job, err := c.UpdateJob(context.Background(), "a", UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if done := waitJob(t, c, job.ID); done.Status != "completed" {
		t.Fatalf("job: %+v", done)
	}
	if steam.updates != 0 || countEvents(c.Events, "a", events.UpdateStarted) != 0 {
		t.Fatal("an up-to-date server was updated")
	}
}

func TestStartWithAutoUpdateInstallsANewerBuildFirst(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fail    error
		started events.Type
		build   string
	}{
		{"newer build", nil, events.UpdateFinished, "200"},
		{"steam fails", errors.New("steam is down"), events.UpdateFailed, "100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testController(t)
			c.Events = events.NewBus()
			steam := &fakeSteam{latest: "200", fail: tc.fail}
			c.Steam = steam
			game := fakeGame(t, "#!/bin/sh\nexec sleep 60\n")
			writeManifest(t, game, "100")
			createServer(t, c, "a", game, true)
			ctx := context.Background()
			cfg, _ := c.Repo.Get(ctx, "a")
			cfg.AutoUpdate = true
			if err := c.Repo.Update(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if err := c.Start(ctx, "a"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Stop(ctx, "a"); _ = c.Processes.Wait(ctx, "a") })
			if c.Lifecycle("a").State != "running" || steam.updates != 1 || countEvents(c.Events, "a", tc.started) != 1 {
				t.Fatalf("state %+v, updates %d, events %+v", c.Lifecycle("a"), steam.updates, c.Events.Recent(0))
			}
			c.latest.entries = nil
			if status, _ := c.CheckUpdate(ctx, "a"); status.Installed != tc.build {
				t.Fatalf("installed %s, want %s", status.Installed, tc.build)
			}
		})
	}
}
