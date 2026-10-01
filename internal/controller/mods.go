package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/hooks"
	"github.com/example/conan-manager/internal/mods"
	"github.com/example/conan-manager/internal/server"
	"github.com/example/conan-manager/internal/steamcmd"
)

// ModsOptions choose how a mod install runs.
type ModsOptions struct {
	// Force downloads every mod again, which repairs files the manager cannot
	// tell are damaged.
	Force bool `json:"force"`
	// Warn tells players first, as a scheduled restart does.
	Warn bool `json:"warn"`
}

// ModsStatus is a server's mod list with what the manager knows about it.
type ModsStatus struct {
	Mods []server.Mod `json:"mods"`
	// Pending counts the mods a run would install or update.
	Pending int `json:"pending"`
	// Error is why Steam could not be asked. The list is returned regardless:
	// a list that empties itself when Steam is unreachable would be worse than
	// a stale one.
	Error string `json:"error,omitempty"`
}

// modListFile names the mod list in the config version history, beside the
// game's own INI files.
const modListFile = "modlist.txt"

// Mods returns a server's mod list in load order. A server that does not exist
// is an error rather than an empty list: the panel would otherwise show a
// deleted or mistyped server as one that simply has no mods.
func (c *Controller) Mods(ctx context.Context, id string) ([]server.Mod, error) {
	if _, err := c.Repo.Get(ctx, id); err != nil {
		return nil, err
	}
	return c.Repo.Mods(ctx, id)
}

// SetMods replaces the list an operator maintains. The file the game reads is
// rewritten at once, so what the panel shows and what the next start loads
// cannot drift apart, while the files themselves are left to ModsJob, which
// stops the server for them.
func (c *Controller) SetMods(ctx context.Context, id string, list []server.Mod) ([]server.Mod, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// The running server has its mods loaded: a list changed under it would
	// describe something other than what is in play, and the files cannot be
	// swapped anyway. Installing mods is the operation that stops the server
	// itself; editing the list is not.
	if state := c.Lifecycle(id).State; state != "idle" {
		return nil, fmt.Errorf("stop the server first: it is %s, and its mods are loaded until it stops", state)
	}
	for i := range list {
		parsed, err := server.ParseWorkshopID(list[i].WorkshopID)
		if err != nil {
			return nil, err
		}
		list[i].WorkshopID = parsed
	}
	if err := c.Repo.SetMods(ctx, id, list); err != nil {
		return nil, err
	}
	saved, err := c.Repo.Mods(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := c.writeModList(ctx, cfg, saved); err != nil {
		c.Logger.Warn("mod list not written", "server", id, "error", err)
	}
	return saved, nil
}

// AddCollection appends the items of a Workshop collection that the server
// does not have yet, keeping the collection's own order.
func (c *Controller) AddCollection(ctx context.Context, id, collection string) ([]server.Mod, error) {
	collectionID, err := server.ParseWorkshopID(collection)
	if err != nil {
		return nil, err
	}
	current, err := c.Repo.Mods(ctx, id)
	if err != nil {
		return nil, err
	}
	children, err := c.Workshop.Collection(ctx, collectionID)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(current))
	for _, m := range current {
		have[m.WorkshopID] = true
	}
	added := 0
	for _, child := range children {
		if have[child] || !server.ValidWorkshopID(child) {
			continue
		}
		have[child] = true
		current = append(current, server.Mod{WorkshopID: child, Enabled: true})
		added++
	}
	if added == 0 {
		return current, fmt.Errorf("every mod of collection %s is already in the list", collectionID)
	}
	return c.SetMods(ctx, id, current)
}

// CheckMods asks Steam what the listed items are and whether any changed. The
// answer is stored, so the panel and the automatic update see the same thing.
func (c *Controller) CheckMods(ctx context.Context, id string) (ModsStatus, error) {
	list, err := c.Mods(ctx, id)
	if err != nil {
		return ModsStatus{}, err
	}
	status := ModsStatus{Mods: list}
	if len(list) == 0 {
		return status, nil
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.WorkshopID)
	}
	details, err := c.Workshop.Details(ctx, ids)
	if err != nil {
		status.Error = err.Error()
		status.Pending = countPending(list)
		return status, nil
	}
	for _, m := range list {
		item, known := details[m.WorkshopID]
		switch {
		case !known:
			_ = c.Repo.MarkModChecked(ctx, id, m.WorkshopID, "", time.Time{}, "Steam did not answer about this mod")
		case item.Missing:
			_ = c.Repo.MarkModChecked(ctx, id, m.WorkshopID, item.Title, time.Time{}, "Steam has no such mod: it was removed or the id is wrong")
		case item.AppID != 0 && item.AppID != mods.GameAppID:
			_ = c.Repo.MarkModChecked(ctx, id, m.WorkshopID, item.Title, time.Time{},
				fmt.Sprintf("this mod is published for app %d, not Conan Exiles", item.AppID))
		default:
			_ = c.Repo.MarkModChecked(ctx, id, m.WorkshopID, item.Title, item.Updated, "")
		}
	}
	if status.Mods, err = c.Repo.Mods(ctx, id); err != nil {
		return status, err
	}
	status.Pending = countPending(status.Mods)
	return status, nil
}

func countPending(list []server.Mod) int { return len(pendingMods(list)) }

// ModsJob installs and updates a server's mods in the background, the way an
// update of the game files runs: hooks, a warning to the players, a stop, the
// swap, and a start again if it was running.
func (c *Controller) ModsJob(ctx context.Context, id string, opts ModsOptions) (Job, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	// Mods go in with the same reservation as a build, so an update and a mod
	// install cannot stop and start the server against each other.
	if !c.beginUpdate(id) {
		return Job{}, fmt.Errorf("an update of server %q is already running", id)
	}
	now := time.Now().UTC()
	job := Job{ID: fmt.Sprintf("mods-%d", now.UnixNano()), ServerID: id, Kind: "mods", Status: "running", CreatedAt: now, UpdatedAt: now}
	c.setJob(job)
	go func(job Job) {
		defer c.endUpdate(id)
		detail, err := c.runMods(context.Background(), cfg, opts)
		job.UpdatedAt = time.Now().UTC()
		job.Status, job.Error = "completed", detail
		if err != nil {
			job.Status, job.Error = "failed", err.Error()
		}
		c.setJob(job)
	}(job)
	return job, nil
}

func (c *Controller) runMods(ctx context.Context, cfg server.Config, opts ModsOptions) (string, error) {
	status, err := c.CheckMods(ctx, cfg.ID)
	if err != nil {
		return "", err
	}
	wanted := make([]server.Mod, 0, len(status.Mods))
	for _, m := range status.Mods {
		if m.Enabled && (m.Pending() || opts.Force) {
			wanted = append(wanted, m)
		}
	}
	if len(wanted) == 0 {
		// There is still work without a download: a mod was disabled or
		// reordered, or one left the list and its files are still on disk.
		if err := c.tidyMods(ctx, cfg, status.Mods); err != nil {
			return "", err
		}
		return "mods are already up to date", nil
	}
	env := map[string]string{"CONAN_MODS": modIDList(wanted)}
	if err := c.runHooks(ctx, hooks.Gate, "pre_update", cfg, env); err != nil {
		c.emit(events.UpdateFailed, cfg, "A pre_update hook refused the mod update", modDetails(wanted, map[string]string{"error": err.Error()}))
		return "", fmt.Errorf("pre_update hook: %w", err)
	}
	c.emit(events.UpdateStarted, cfg, "Downloading mods", modDetails(wanted, nil))
	// Downloading first, while the server still serves players, is what keeps
	// the stop to the length of a file copy: SteamCMD writes into the shared
	// cache, which no running server reads.
	// Force is the repair: it re-checks every file against Steam, which is
	// what the game's own launcher does on each mod every time.
	arrived, failures := c.downloadMods(ctx, cfg, wanted, opts.Force)
	if len(arrived) == 0 {
		err := errors.New(strings.Join(failures, "; "))
		c.emit(events.UpdateFailed, cfg, "No mod could be downloaded", modDetails(wanted, map[string]string{"error": err.Error()}))
		return "", err
	}
	wasRunning := c.Lifecycle(cfg.ID).State == "running"
	if wasRunning {
		if err := c.warnPlayers(ctx, cfg, server.MsgUpdate, modUpdateVars(modsOf(arrived)), opts.Warn); err != nil {
			return "", err
		}
		if err := c.stopAndWait(ctx, cfg.ID); err != nil {
			c.emit(events.UpdateFailed, cfg, "Could not stop the server for the mod update", modDetails(wanted, map[string]string{"error": err.Error()}))
			return "", err
		}
	}
	installErr := c.installMods(ctx, cfg, arrived)
	if installErr == nil {
		c.emit(events.UpdateFinished, cfg, "Mods updated", modDetails(modsOf(arrived), nil))
		_ = c.runHooks(ctx, hooks.Async, "post_update", cfg, env)
	} else {
		c.emit(events.UpdateFailed, cfg, "Mods could not be installed", modDetails(wanted, map[string]string{"error": installErr.Error()}))
	}
	if wasRunning {
		if err := c.Start(ctx, cfg.ID); err != nil {
			return "", errors.Join(installErr, fmt.Errorf("start after the mod update: %w", err))
		}
	}
	if installErr != nil {
		return "", installErr
	}
	if len(failures) > 0 {
		return strconv.Itoa(len(failures)) + " of " + strconv.Itoa(len(wanted)) + " mods failed: " + strings.Join(failures, "; "), nil
	}
	return "", nil
}

// downloaded is a mod whose files arrived, with the directory they arrived in.
// The downloader reports that directory rather than the manager deriving it:
// whether SteamCMD honours force_install_dir for Workshop content varies
// between its own builds.
type downloaded struct {
	mod server.Mod
	dir string
}

func modsOf(arrived []downloaded) []server.Mod {
	list := make([]server.Mod, 0, len(arrived))
	for _, d := range arrived {
		list = append(list, d.mod)
	}
	return list
}

// downloadMods fetches the mods into the shared cache and returns those that
// arrived. They go to SteamCMD in one command line rather than one run each:
// starting SteamCMD costs more than most mods take to download, so forty mods
// run separately would spend most of their time starting up.
//
// One mod Steam will not serve must not stop the others: a modpack whose tenth
// item was taken down still has nine that work.
func (c *Controller) downloadMods(ctx context.Context, cfg server.Config, wanted []server.Mod, validate bool) ([]downloaded, []string) {
	out := openSteamLog(cfg, "mods")
	defer out.Close()
	ids := make([]string, 0, len(wanted))
	for _, m := range wanted {
		ids = append(ids, m.WorkshopID)
	}
	fmt.Fprintf(out, "\n=== conan-manager: %d mod(s): %s ===\n", len(ids), strings.Join(ids, " "))
	results, err := c.Steam.DownloadWorkshopItems(ctx, cfg.SteamCMDDir, c.workshopDir(cfg), ids, validate, out)
	if err != nil {
		c.Logger.Warn("mod download failed", "server", cfg.ID, "error", err)
	}
	done := make([]downloaded, 0, len(wanted))
	var failures []string
	for _, m := range wanted {
		result, known := results[m.WorkshopID]
		if !known || result.Dir == "" {
			reason := result.Reason
			if reason == "" && err != nil {
				reason = err.Error()
			}
			if reason == "" {
				reason = "SteamCMD did not download this mod"
			}
			_ = c.Repo.MarkModFailed(ctx, cfg.ID, m.WorkshopID, reason)
			failures = append(failures, modName(m)+": "+reason)
			c.Logger.Warn("mod not downloaded", "server", cfg.ID, "mod", m.WorkshopID, "reason", reason)
			continue
		}
		done = append(done, downloaded{mod: m, dir: result.Dir})
	}
	return done, failures
}

// installMods puts the downloaded files where the game looks for them, drops
// the directories of mods that left the list, and rewrites modlist.txt. It
// holds the install reservation, so no start can race the file swap.
func (c *Controller) installMods(ctx context.Context, cfg server.Config, arrived []downloaded) error {
	if _, err := c.acquire(ctx, cfg.ID, "installing"); err != nil {
		return err
	}
	defer c.release(cfg.ID)
	state, stateErr := steamcmd.WorkshopState(c.workshopDir(cfg))
	for _, d := range arrived {
		paks, err := mods.Install(cfg, d.dir, d.mod.WorkshopID)
		if err != nil {
			_ = c.Repo.MarkModFailed(ctx, cfg.ID, d.mod.WorkshopID, err.Error())
			return fmt.Errorf("install mod %s: %w", d.mod.WorkshopID, err)
		}
		var size int64
		updated := time.Time{}
		if stateErr == nil {
			if item, ok := state[d.mod.WorkshopID]; ok {
				size, updated = item.Size, item.Updated
			}
		}
		if updated.IsZero() {
			// Without the manifest the mod would look out of date forever, so
			// the time Steam reported at the check stands in for it.
			updated = d.mod.LatestUpdated
		}
		if err := c.Repo.MarkModInstalled(ctx, cfg.ID, d.mod.WorkshopID, paks, size, updated); err != nil {
			return err
		}
	}
	list, err := c.Repo.Mods(ctx, cfg.ID)
	if err != nil {
		return err
	}
	if err := mods.RemoveOrphans(cfg, list); err != nil {
		c.Logger.Warn("leftover mod files not removed", "server", cfg.ID, "error", err)
	}
	return c.writeModList(ctx, cfg, list)
}

// watchMods handles the mods the periodic check found changed on Steam, the
// way a new build is handled: with auto_update they go in at once on a stopped
// server and at a delayed restart on a running one, and without it they are
// only announced.
func (c *Controller) watchMods(ctx context.Context, cfg server.Config, running bool) {
	status, err := c.CheckMods(ctx, cfg.ID)
	if err != nil {
		c.Logger.Warn("mod check failed", "server", cfg.ID, "error", err)
		return
	}
	if status.Error != "" {
		c.Logger.Warn("mod check failed", "server", cfg.ID, "error", status.Error)
		return
	}
	pending := pendingMods(status.Mods)
	if len(pending) == 0 {
		return
	}
	switch {
	case cfg.AutoUpdate && running:
		c.modsSoon(ctx, cfg, pending)
	case cfg.AutoUpdate:
		if c.Lifecycle(cfg.ID).State == "idle" {
			if _, err := c.ModsJob(ctx, cfg.ID, ModsOptions{}); err != nil {
				c.Logger.Info("automatic mod update not started", "server", cfg.ID, "error", err)
			}
		}
	case c.noteUpdate(modsNoteKey(cfg.ID), modsNote(pending)):
		c.announceUpdate(ctx, cfg, modUpdateVars(pending), running)
	}
}

// modsSoon announces the mods waiting on a running server with auto_update and
// sets the restart they go in at. The set is handled once, so a delayed restart
// the operator cancelled is not set again and the mods go in at the next one.
func (c *Controller) modsSoon(ctx context.Context, cfg server.Config, pending []server.Mod) {
	if !c.noteUpdate(modsNoteKey(cfg.ID), modsNote(pending)) {
		return
	}
	c.announceSoon(ctx, cfg, modUpdateVars(pending), modIDList(pending))
}

// modsNoteKey keeps the mod announcement from overwriting the build one:
// noteUpdate holds a single value per key, and a server has both to announce.
func modsNoteKey(id string) string { return id + "\x00mods" }

// modsNote identifies the versions waiting, so a further change on Steam is
// announced again while the same one is not.
func modsNote(pending []server.Mod) string {
	parts := make([]string, 0, len(pending))
	for _, m := range pending {
		parts = append(parts, m.WorkshopID+"@"+strconv.FormatInt(m.LatestUpdated.Unix(), 10))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func pendingMods(list []server.Mod) []server.Mod {
	pending := make([]server.Mod, 0, len(list))
	for _, m := range list {
		if m.Enabled && m.Pending() {
			pending = append(pending, m)
		}
	}
	return pending
}

// tidyMods brings the files in line with a list that needs no download: a mod
// dropped from the list still has files to remove. Removing them needs the
// install reservation, so on a running server only the list is rewritten and
// the files wait for a run that stops the server anyway.
func (c *Controller) tidyMods(ctx context.Context, cfg server.Config, list []server.Mod) error {
	if _, err := c.acquire(ctx, cfg.ID, "installing"); err == nil {
		if err := mods.RemoveOrphans(cfg, list); err != nil {
			c.Logger.Warn("leftover mod files not removed", "server", cfg.ID, "error", err)
		}
		c.release(cfg.ID)
	}
	return c.writeModList(ctx, cfg, list)
}

// writeModList rewrites the file the game reads, keeping what it held as a
// version an operator can return to.
func (c *Controller) writeModList(ctx context.Context, cfg server.Config, list []server.Mod) error {
	previous, changed, err := mods.Apply(cfg, list)
	if err != nil || !changed {
		return err
	}
	if _, err := c.Repo.SaveConfigVersion(ctx, server.ConfigVersion{ServerID: cfg.ID, File: modListFile,
		Reason: "before the mod list changed", Content: previous}); err != nil {
		return err
	}
	c.emit(events.ConfigChanged, cfg, modListFile+": mod list rewritten", map[string]string{"file": modListFile})
	return nil
}

// workshopDir is the shared download cache. Sharing it across servers means a
// mod two servers run is downloaded once; without a configured directory it
// falls back beside the server's own SteamCMD.
func (c *Controller) workshopDir(cfg server.Config) string {
	if c.WorkshopDir != "" {
		return c.WorkshopDir
	}
	return filepath.Join(cfg.SteamCMDDir, "workshop")
}

// modName is what to call a mod in a message: its title if Steam gave one.
func modName(m server.Mod) string {
	if m.Title != "" {
		return m.Title
	}
	return m.WorkshopID
}

func modIDList(list []server.Mod) string {
	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.WorkshopID)
	}
	return strings.Join(ids, " ")
}

// modUpdateVars fill the update message for mods. Its {target} was written for
// exactly this: "игры" for a build, a mod for the rest.
func modUpdateVars(list []server.Mod) map[string]string {
	target := "мода"
	if len(list) > 1 {
		target = "модов"
	}
	names := make([]string, 0, len(list))
	for _, m := range list {
		names = append(names, modName(m))
	}
	sort.Strings(names)
	version := strings.Join(names, ", ")
	// A long list would be cut off in the game's chat; the count says as much.
	if len(version) > 120 {
		version = strconv.Itoa(len(names)) + " шт."
	}
	return map[string]string{"target": target, "version": version}
}

// modDetails describe the mods an event is about, bounded so one event cannot
// carry a hundred keys.
func modDetails(list []server.Mod, extra map[string]string) map[string]string {
	details := map[string]string{"target": "mods", "count": strconv.Itoa(len(list))}
	names := make([]string, 0, len(list))
	for i, m := range list {
		if i == 10 {
			details["more"] = strconv.Itoa(len(list) - i)
			break
		}
		names = append(names, modName(m))
	}
	details["mods"] = strings.Join(names, ", ")
	for k, v := range extra {
		details[k] = v
	}
	return details
}

// ModFiles reports the .pak files a server has on disk for one mod, for the
// panel to show what is installed rather than what was asked for.
func ModFiles(cfg server.Config, workshopID string) ([]string, error) {
	paks, err := mods.PakNames(mods.ItemDir(cfg, workshopID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return paks, err
}
