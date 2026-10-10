package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/mods"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
	"github.com/Myp3uK/ConanLinuxManager/internal/steamcmd"
)

// fakeWorkshop answers for Steam's Workshop API.
type fakeWorkshop struct {
	mu          sync.Mutex
	details     map[string]mods.ItemDetails
	collections map[string][]string
	fail        error
	calls       int
}

func (f *fakeWorkshop) Details(_ context.Context, ids []string) (map[string]mods.ItemDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	out := map[string]mods.ItemDetails{}
	for _, id := range ids {
		if item, ok := f.details[id]; ok {
			out[id] = item
		}
	}
	return out, nil
}

func (f *fakeWorkshop) Collection(_ context.Context, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if children, ok := f.collections[id]; ok {
		return children, nil
	}
	return nil, fmt.Errorf("no collection %s", id)
}

// workshopSteam stands in for SteamCMD downloading a Workshop item: it writes
// the pak files it is told to and records the manifest the real one would.
type workshopSteam struct {
	scriptedSteam
	mu        sync.Mutex
	paks      map[string][]string
	updated   map[string]int64
	fail      map[string]error
	downloads []string
	runs      int
	validated bool
}

// runs counts the invocations, which is the whole point of batching: the
// number of mods must not decide how many times SteamCMD starts.
func (s *workshopSteam) DownloadWorkshopItems(_ context.Context, _, contentDir string, ids []string, validate bool, out io.Writer) (map[string]steamcmd.WorkshopResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs++
	s.validated = validate
	results := map[string]steamcmd.WorkshopResult{}
	for _, id := range ids {
		s.downloads = append(s.downloads, id)
		fmt.Fprintf(out, "Downloading item %s ...\n", id)
		if err := s.fail[id]; err != nil {
			results[id] = steamcmd.WorkshopResult{Reason: err.Error()}
			continue
		}
		dir := filepath.Join(contentDir, "steamapps", "workshop", "content", "440900", id)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return results, err
		}
		for _, pak := range s.paks[id] {
			if err := os.WriteFile(filepath.Join(dir, pak), []byte(pak), 0o640); err != nil {
				return results, err
			}
		}
		results[id] = steamcmd.WorkshopResult{Dir: dir}
	}
	return results, s.writeManifest(contentDir)
}

// writeManifest writes the appworkshop_440900.acf SteamCMD keeps, which is
// where the installed version comes from.
func (s *workshopSteam) writeManifest(contentDir string) error {
	var b strings.Builder
	b.WriteString("\"AppWorkshop\"\n{\n\t\"appid\"\t\t\"440900\"\n\t\"WorkshopItemsInstalled\"\n\t{\n")
	for id, seconds := range s.updated {
		fmt.Fprintf(&b, "\t\t\"%s\"\n\t\t{\n\t\t\t\"size\"\t\t\"%d\"\n\t\t\t\"timeupdated\"\t\t\"%d\"\n\t\t}\n", id, 100+seconds, seconds)
	}
	b.WriteString("\t}\n}\n")
	dir := filepath.Join(contentDir, "steamapps", "workshop")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "appworkshop_440900.acf"), []byte(b.String()), 0o640)
}

func modsController(t *testing.T) (*Controller, server.Config, *workshopSteam, *fakeWorkshop) {
	t.Helper()
	c, create := testController(t)
	cfg := create("alpha", filepath.Join(t.TempDir(), "game"), "")
	steam := &workshopSteam{
		paks:    map[string][]string{"111111": {"First.pak"}, "222222": {"Second.pak", "SecondExtra.pak"}},
		updated: map[string]int64{"111111": 1700000000, "222222": 1700000000},
		fail:    map[string]error{},
	}
	shop := &fakeWorkshop{details: map[string]mods.ItemDetails{
		"111111": {WorkshopID: "111111", Title: "First", AppID: mods.GameAppID, Updated: time.Unix(1700000000, 0).UTC()},
		"222222": {WorkshopID: "222222", Title: "Second", AppID: mods.GameAppID, Updated: time.Unix(1700000000, 0).UTC()},
	}}
	c.Steam, c.Workshop = steam, shop
	c.WorkshopDir = filepath.Join(t.TempDir(), "workshop")
	return c, cfg, steam, shop
}

func TestSetModsWritesTheListAtOnce(t *testing.T) {
	c, cfg, _, _ := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{
		{WorkshopID: "https://steamcommunity.com/sharedfiles/filedetails/?id=111111", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	list, err := c.Mods(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A link is accepted where an id is: that is what an operator copies.
	if len(list) != 1 || list[0].WorkshopID != "111111" {
		t.Fatalf("unexpected list: %+v", list)
	}
	// Nothing is installed yet, so the file the game reads stays empty rather
	// than naming paks that are not there.
	if content, err := os.ReadFile(mods.ListPath(cfg)); err == nil && strings.TrimSpace(string(content)) != "" {
		t.Errorf("modlist.txt names uninstalled mods: %q", content)
	}
}

func TestModsJobDownloadsInstallsAndWritesTheList(t *testing.T) {
	c, cfg, steam, _ := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{
		{WorkshopID: "222222", Enabled: true},
		{WorkshopID: "111111", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	job, err := c.ModsJob(ctx, cfg.ID, ModsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job = waitModsJob(t, c, job.ID)
	if job.Status != "completed" {
		t.Fatalf("job: %+v", job)
	}
	if len(steam.downloads) != 2 {
		t.Fatalf("downloads: %v", steam.downloads)
	}
	// The order of the list, not the order of the download, decides the file.
	want := strings.Join([]string{
		mods.ItemDir(cfg, "222222") + "/Second.pak",
		mods.ItemDir(cfg, "222222") + "/SecondExtra.pak",
		mods.ItemDir(cfg, "111111") + "/First.pak",
	}, "\n") + "\n"
	content, err := os.ReadFile(mods.ListPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Errorf("modlist.txt =\n%q\nwant\n%q", content, want)
	}
	list, err := c.Mods(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.NeedsInstall() || m.UpdateAvailable() {
			t.Errorf("mod %s still pending after the job: %+v", m.WorkshopID, m)
		}
	}
	// A second run has nothing to do, so it must not download again.
	job, err = c.ModsJob(ctx, cfg.ID, ModsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job = waitModsJob(t, c, job.ID); job.Status != "completed" || !strings.Contains(job.Error, "already up to date") {
		t.Fatalf("second run: %+v", job)
	}
	if len(steam.downloads) != 2 {
		t.Errorf("downloaded again: %v", steam.downloads)
	}
}

func TestModsJobUpdatesOnlyWhatSteamChanged(t *testing.T) {
	c, cfg, steam, shop := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}, {WorkshopID: "222222", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if job := waitModsJob(t, c, startMods(t, c, cfg.ID)); job.Status != "completed" {
		t.Fatalf("first run: %+v", job)
	}
	steam.downloads = nil
	// Only one mod changed on Steam.
	newer := time.Unix(1760000000, 0).UTC()
	shop.details["222222"] = mods.ItemDetails{WorkshopID: "222222", Title: "Second", AppID: mods.GameAppID, Updated: newer}
	steam.updated["222222"] = newer.Unix()
	status, err := c.CheckMods(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Pending != 1 {
		t.Fatalf("pending = %d: %+v", status.Pending, status.Mods)
	}
	if job := waitModsJob(t, c, startMods(t, c, cfg.ID)); job.Status != "completed" {
		t.Fatalf("second run: %+v", job)
	}
	if len(steam.downloads) != 1 || steam.downloads[0] != "222222" {
		t.Errorf("downloads = %v; want only 222222", steam.downloads)
	}
}

func TestModsJobKeepsGoingWhenOneModFails(t *testing.T) {
	c, cfg, steam, _ := modsController(t)
	ctx := context.Background()
	steam.fail["111111"] = errors.New("ERROR! Download item 111111 failed (File Not Found)")
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}, {WorkshopID: "222222", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	job := waitModsJob(t, c, startMods(t, c, cfg.ID))
	// A modpack whose one item was taken down still has the rest, so the run
	// completes and says what failed.
	if job.Status != "completed" || !strings.Contains(job.Error, "File Not Found") {
		t.Fatalf("job: %+v", job)
	}
	content, err := os.ReadFile(mods.ListPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Second.pak") || strings.Contains(string(content), "First.pak") {
		t.Errorf("modlist.txt should hold only the mod that arrived: %q", content)
	}
	list, err := c.Mods(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.WorkshopID == "111111" && !strings.Contains(m.LastError, "File Not Found") {
			t.Errorf("the failure should be on the mod: %+v", m)
		}
	}
}

func TestModsJobFailsWhenNothingCouldBeDownloaded(t *testing.T) {
	c, cfg, steam, _ := modsController(t)
	steam.fail["111111"] = errors.New("ERROR! Download item 111111 failed (Access Denied)")
	if _, err := c.SetMods(context.Background(), cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if job := waitModsJob(t, c, startMods(t, c, cfg.ID)); job.Status != "failed" {
		t.Fatalf("job: %+v", job)
	}
}

func TestDisabledAndRemovedModsLeaveTheList(t *testing.T) {
	c, cfg, _, _ := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}, {WorkshopID: "222222", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	waitModsJob(t, c, startMods(t, c, cfg.ID))
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: false}, {WorkshopID: "222222", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(mods.ListPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "First.pak") {
		t.Errorf("a disabled mod is still listed: %q", content)
	}
	// Its files stay: turning a mod off for a week should not mean paying for
	// the download again.
	if _, err := os.Stat(filepath.Join(mods.ItemDir(cfg, "111111"), "First.pak")); err != nil {
		t.Errorf("a disabled mod lost its files: %v", err)
	}
	// Dropping it from the list is what removes them, at the next run.
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "222222", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	waitModsJob(t, c, startMods(t, c, cfg.ID))
	if _, err := os.Stat(mods.ItemDir(cfg, "111111")); !os.IsNotExist(err) {
		t.Errorf("leftover mod files: %v", err)
	}
}

func TestCheckModsReportsWhatStoppedIt(t *testing.T) {
	c, cfg, _, shop := modsController(t)
	ctx := context.Background()
	shop.details["333333"] = mods.ItemDetails{WorkshopID: "333333", Missing: true}
	shop.details["444444"] = mods.ItemDetails{WorkshopID: "444444", Title: "Other game", AppID: 107410, Updated: time.Unix(1700000000, 0).UTC()}
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{
		{WorkshopID: "333333", Enabled: true}, {WorkshopID: "444444", Enabled: true}, {WorkshopID: "555555", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := c.CheckMods(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, m := range status.Mods {
		reasons[m.WorkshopID] = m.LastError
	}
	if !strings.Contains(reasons["333333"], "no such mod") {
		t.Errorf("missing item: %q", reasons["333333"])
	}
	if !strings.Contains(reasons["444444"], "not Conan Exiles") {
		t.Errorf("mod for another game: %q", reasons["444444"])
	}
	if !strings.Contains(reasons["555555"], "did not answer") {
		t.Errorf("item Steam said nothing about: %q", reasons["555555"])
	}
}

func TestCheckModsSurvivesSteamBeingDown(t *testing.T) {
	c, cfg, _, shop := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CheckMods(ctx, cfg.ID); err != nil {
		t.Fatal(err)
	}
	shop.fail = errors.New("dial tcp: connection refused")
	status, err := c.CheckMods(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("an unreachable Steam is not an error of the panel: %v", err)
	}
	if status.Error == "" || len(status.Mods) != 1 || status.Mods[0].Title != "First" {
		t.Fatalf("the known list should survive: %+v", status)
	}
}

func TestAddCollectionAppendsWhatIsMissing(t *testing.T) {
	c, cfg, _, shop := modsController(t)
	ctx := context.Background()
	shop.collections = map[string][]string{"999999": {"111111", "222222", "333333"}}
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "222222", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	list, err := c.AddCollection(ctx, cfg.ID, "https://steamcommunity.com/sharedfiles/filedetails/?id=999999")
	if err != nil {
		t.Fatal(err)
	}
	// What was already there keeps its place; the rest is appended in the
	// collection's order.
	if len(list) != 3 || list[0].WorkshopID != "222222" || list[1].WorkshopID != "111111" || list[2].WorkshopID != "333333" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if _, err := c.AddCollection(ctx, cfg.ID, "999999"); err == nil {
		t.Error("adding the same collection twice should say so")
	}
}

func TestModUpdateVarsNameTheMods(t *testing.T) {
	one := modUpdateVars([]server.Mod{{WorkshopID: "111111", Title: "Pippi"}})
	if one["target"] != "мода" || one["version"] != "Pippi" {
		t.Errorf("one mod: %v", one)
	}
	two := modUpdateVars([]server.Mod{{WorkshopID: "111111", Title: "Pippi"}, {WorkshopID: "222222"}})
	if two["target"] != "модов" || two["version"] != "222222, Pippi" {
		t.Errorf("two mods: %v", two)
	}
	many := make([]server.Mod, 30)
	for i := range many {
		many[i] = server.Mod{WorkshopID: "111111", Title: strings.Repeat("m", 10)}
	}
	// The game's chat would cut a long list off; the count says as much.
	if got := modUpdateVars(many)["version"]; got != "30 шт." {
		t.Errorf("many mods: %q", got)
	}
}

func startMods(t *testing.T, c *Controller, id string) string {
	t.Helper()
	job, err := c.ModsJob(context.Background(), id, ModsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return job.ID
}

func waitModsJob(t *testing.T, c *Controller, id string) Job {
	t.Helper()
	var job Job
	waitFor(t, 20*time.Second, "mods job", func() bool {
		job, _ = c.Job(id)
		return job.Status != "running"
	})
	return job
}

func TestWatchModsInstallsOnAStoppedServerWithAutoUpdate(t *testing.T) {
	c, cfg, steam, _ := modsController(t)
	ctx := context.Background()
	cfg.AutoUpdate = true
	if err := c.Repo.Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	c.watchMods(ctx, cfg, false)
	// The job runs in the background; it is the download that proves it ran.
	waitFor(t, 20*time.Second, "mod download", func() bool {
		steam.mu.Lock()
		defer steam.mu.Unlock()
		return len(steam.downloads) == 1
	})
	waitFor(t, 20*time.Second, "mods installed", func() bool {
		list, err := c.Mods(ctx, cfg.ID)
		return err == nil && len(list) == 1 && !list[0].NeedsInstall()
	})
}

func TestWatchModsOnlyAnnouncesWithoutAutoUpdate(t *testing.T) {
	c, cfg, steam, _ := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	c.watchMods(ctx, cfg, false)
	time.Sleep(100 * time.Millisecond)
	steam.mu.Lock()
	downloads := len(steam.downloads)
	steam.mu.Unlock()
	if downloads != 0 {
		t.Errorf("a server without auto_update was updated anyway: %d downloads", downloads)
	}
}

func TestModsNoteDoesNotClobberTheBuildNote(t *testing.T) {
	c, cfg, _, _ := modsController(t)
	ctx := context.Background()
	pending := []server.Mod{{WorkshopID: "111111", LatestUpdated: time.Unix(1700000000, 0).UTC()}}
	// Both are announced once, and neither silences the other: a note holds
	// one value per kind, so the two must not share a kind.
	if !c.noteUpdate(ctx, cfg.ID, server.NoticeBuild, "200") {
		t.Fatal("the build should be new")
	}
	if !c.noteUpdate(ctx, cfg.ID, server.NoticeMods, modsNote(pending)) {
		t.Fatal("the mods should be new")
	}
	if c.noteUpdate(ctx, cfg.ID, server.NoticeBuild, "200") {
		t.Error("the build was announced twice")
	}
	if c.noteUpdate(ctx, cfg.ID, server.NoticeMods, modsNote(pending)) {
		t.Error("the mods were announced twice")
	}
	// A further change on Steam is a new announcement.
	pending[0].LatestUpdated = time.Unix(1760000000, 0).UTC()
	if !c.noteUpdate(ctx, cfg.ID, server.NoticeMods, modsNote(pending)) {
		t.Error("a newer mod version should be announced")
	}
}

// TestNoteSurvivesTheManagerRestart is the point of storing the note: the
// operator saw the same waiting build announced after every deploy.
func TestNoteSurvivesTheManagerRestart(t *testing.T) {
	c, cfg, _, _ := modsController(t)
	ctx := context.Background()
	if !c.noteUpdate(ctx, cfg.ID, server.NoticeBuild, "25639945") {
		t.Fatal("the build should be new")
	}
	// A manager that started again has an empty memory but the same database.
	restarted := &Controller{Repo: c.Repo, Logger: c.Logger}
	if restarted.noteUpdate(ctx, cfg.ID, server.NoticeBuild, "25639945") {
		t.Error("the same build was announced again after a restart")
	}
	if !restarted.noteUpdate(ctx, cfg.ID, server.NoticeBuild, "25700000") {
		t.Error("a genuinely newer build should still be announced")
	}
}

func TestNoModsLeavesNoModList(t *testing.T) {
	c, cfg, _, _ := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, nil); err != nil {
		t.Fatal(err)
	}
	if job := waitModsJob(t, c, startMods(t, c, cfg.ID)); job.Status != "completed" {
		t.Fatalf("job: %+v", job)
	}
	// An empty modlist.txt where there was none would be the manager saying
	// something about mods on a server that has none.
	if _, err := os.Stat(mods.ListPath(cfg)); !os.IsNotExist(err) {
		t.Errorf("modlist.txt created for a server without mods: %v", err)
	}
}

func TestModsJobStartsSteamCMDOnceForTheWholeList(t *testing.T) {
	c, cfg, steam, shop := modsController(t)
	ctx := context.Background()
	list := make([]server.Mod, 0, 6)
	for i := 0; i < 6; i++ {
		id := fmt.Sprint(111111 + i)
		steam.paks[id] = []string{"Mod" + id + ".pak"}
		steam.updated[id] = 1700000000
		shop.details[id] = mods.ItemDetails{WorkshopID: id, Title: "Mod " + id, AppID: mods.GameAppID,
			Updated: time.Unix(1700000000, 0).UTC()}
		list = append(list, server.Mod{WorkshopID: id, Enabled: true})
	}
	if _, err := c.SetMods(ctx, cfg.ID, list); err != nil {
		t.Fatal(err)
	}
	if job := waitModsJob(t, c, startMods(t, c, cfg.ID)); job.Status != "completed" {
		t.Fatalf("job: %+v", job)
	}
	steam.mu.Lock()
	runs, downloads, validated := steam.runs, len(steam.downloads), steam.validated
	steam.mu.Unlock()
	// Starting SteamCMD costs more than most mods take to download, so six
	// mods must still be one run.
	if runs != 1 {
		t.Errorf("SteamCMD started %d times for %d mods; want 1", runs, downloads)
	}
	if downloads != 6 {
		t.Errorf("downloaded %d of 6 mods", downloads)
	}
	if validated {
		t.Error("a plain run should not re-validate every file")
	}
}

func TestForcedModsRunValidatesFiles(t *testing.T) {
	c, cfg, steam, _ := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	job, err := c.ModsJob(ctx, cfg.ID, ModsOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if job = waitModsJob(t, c, job.ID); job.Status != "completed" {
		t.Fatalf("job: %+v", job)
	}
	steam.mu.Lock()
	defer steam.mu.Unlock()
	// Force is the repair, so it re-checks the files against Steam.
	if !steam.validated {
		t.Error("a forced run should validate")
	}
}

func TestSetModsRefusesARunningServer(t *testing.T) {
	c, cfg, _, _ := modsController(t)
	ctx := context.Background()
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "111111", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.acquire(ctx, cfg.ID, "running"); err != nil {
		t.Fatal(err)
	}
	defer c.release(cfg.ID)
	// A running server has its mods loaded: a list changed under it would
	// describe something other than what is in play.
	if _, err := c.SetMods(ctx, cfg.ID, []server.Mod{{WorkshopID: "222222", Enabled: true}}); err == nil {
		t.Fatal("the list was changed on a running server")
	}
	list, err := c.Mods(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].WorkshopID != "111111" {
		t.Fatalf("the list changed anyway: %+v", list)
	}
}
