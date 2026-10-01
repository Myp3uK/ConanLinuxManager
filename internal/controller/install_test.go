package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/steamcmd"
)

// scriptedSteam prints like SteamCMD and fails when told to.
type scriptedSteam struct{ fail error }

func (s scriptedSteam) InstallServer(ctx context.Context, cfg server.Config, output io.Writer) error {
	fmt.Fprint(output, " Update state (0x61) downloading, progress: 10.00\r Update state (0x61) downloading, progress: 90.00\n")
	if s.fail != nil {
		fmt.Fprintln(output, "ERROR! Failed to install app '443030' (Disk write failure)")
		return s.fail
	}
	fmt.Fprintln(output, "Success! App '443030' fully installed.")
	return nil
}

func (scriptedSteam) LatestBuild(context.Context, string, string) (string, error) { return "", nil }

func (scriptedSteam) UpdateServer(context.Context, string, string, string, bool, io.Writer) error {
	return nil
}

func (scriptedSteam) DownloadWorkshopItems(_ context.Context, _, contentDir string, ids []string, _ bool, _ io.Writer) (map[string]steamcmd.WorkshopResult, error) {
	results := map[string]steamcmd.WorkshopResult{}
	for _, id := range ids {
		results[id] = steamcmd.WorkshopResult{Dir: contentDir + "/" + id}
	}
	return results, nil
}

func waitInstall(t *testing.T, c *Controller, id string) Job {
	t.Helper()
	var job Job
	waitFor(t, 5*time.Second, "install job", func() bool {
		job, _ = c.Job(id)
		return job.Status == "completed" || job.Status == "failed"
	})
	return job
}

func TestInstallLeavesATrace(t *testing.T) {
	c, create := testController(t)
	c.Events = events.NewBus()
	install := filepath.Join(t.TempDir(), "game")
	create("a", install, "")
	ctx := context.Background()

	// Before anything ran there is no SteamCMD log, and saying so is not an
	// error.
	if _, err := c.TailLog(ctx, "a", "steamcmd", 50, ""); !errors.Is(err, ErrLogMissing) {
		t.Fatalf("missing log: %v", err)
	}
	if c.LastJob("a", "install") != nil {
		t.Fatal("a job before any install")
	}

	c.Steam = scriptedSteam{fail: errors.New("SteamCMD install: exit status 8: ERROR! Failed to install app '443030' (Disk write failure)")}
	job, err := c.Install(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if job = waitInstall(t, c, job.ID); job.Status != "failed" {
		t.Fatalf("job %+v", job)
	}
	if last := c.LastJob("a", "install"); last == nil || last.ID != job.ID || last.Status != "failed" {
		t.Fatalf("last job %+v", last)
	}
	waitFor(t, 2*time.Second, "install events", func() bool {
		return countEvents(c.Events, "a", events.InstallStarted) == 1 && countEvents(c.Events, "a", events.InstallFailed) == 1
	})

	c.Steam = scriptedSteam{}
	job, err = c.Install(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if job = waitInstall(t, c, job.ID); job.Status != "completed" {
		t.Fatalf("job %+v", job)
	}
	waitFor(t, 2*time.Second, "finished event", func() bool { return countEvents(c.Events, "a", events.InstallFinished) == 1 })

	lines, err := c.TailLog(ctx, "a", "steamcmd", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{"=== conan-manager: install at", "progress: 10.00", "progress: 90.00", "Disk write failure", "fully installed"} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	// Progress updates end in a carriage return; each is a line of its own.
	for _, line := range lines {
		if strings.Contains(line, "10.00") && strings.Contains(line, "90.00") {
			t.Errorf("progress not split: %q", line)
		}
	}
}

func TestConfigOfAnUnwrittenFileHasNoSections(t *testing.T) {
	c, create := testController(t)
	install := filepath.Join(t.TempDir(), "game")
	if err := os.MkdirAll(install, 0o750); err != nil {
		t.Fatal(err)
	}
	create("a", install, "")
	view, err := c.ReadConfig(context.Background(), "a", "Engine")
	if err != nil {
		t.Fatal(err)
	}
	if view.Exists || view.Sections == nil || len(view.Sections) != 0 {
		t.Fatalf("view %+v", view)
	}
}
