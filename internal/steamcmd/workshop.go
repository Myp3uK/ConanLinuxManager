package steamcmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// gameAppID owns the Workshop, while conanAppID is the dedicated server. Mods
// are published against the game, so a server-only install still downloads
// them from the game's Workshop.
const gameAppID = "440900"

// WorkshopItem is what Steam has stored for one downloaded item.
type WorkshopItem struct {
	// Updated is the time Steam last changed the item, which is how an
	// installed copy is told apart from a newer one.
	Updated time.Time
	Size    int64
	// Manifest identifies the exact content; it is kept for diagnosing a
	// download that produced the wrong files.
	Manifest string
}

// workshopArgs is the command line for one item. The platform override is not
// optional: Conan publishes mod paks in the Windows depot, and a SteamCMD
// running as Linux is told there is nothing to download.
func workshopArgs(contentDir, workshopID string) []string {
	return []string{
		"+@sSteamCmdForcePlatformType", "windows",
		"+force_install_dir", contentDir,
		"+login", "anonymous",
		"+workshop_download_item", gameAppID, workshopID,
		"+quit",
	}
}

// DownloadWorkshopItem fetches one Workshop item into contentDir and returns
// the directory its files landed in.
func (s Service) DownloadWorkshopItem(ctx context.Context, steamcmdDir, contentDir, workshopID string, output io.Writer) (string, error) {
	binary, err := s.Ensure(ctx, steamcmdDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(contentDir, 0o750); err != nil {
		return "", err
	}
	if output == nil {
		output = s.Output
	}
	defer lockDir(steamcmdDir)()
	if err := runUpdate(ctx, binary, steamcmdDir, workshopArgs(contentDir, workshopID), output); err != nil {
		return "", fmt.Errorf("SteamCMD workshop_download_item %s: %w", workshopID, err)
	}
	dir, err := workshopItemDir(contentDir, steamcmdDir, workshopID)
	if err != nil {
		return "", err
	}
	return dir, nil
}

// workshopContentDir is where SteamCMD puts an item under a given root.
func workshopContentDir(root, workshopID string) string {
	return filepath.Join(root, "steamapps", "workshop", "content", gameAppID, workshopID)
}

// workshopItemDir finds the downloaded files. Whether SteamCMD honours
// force_install_dir for Workshop content varies between its own builds, so both
// the requested directory and SteamCMD's own are checked rather than trusted.
func workshopItemDir(contentDir, steamcmdDir, workshopID string) (string, error) {
	for _, root := range []string{contentDir, steamcmdDir} {
		dir := workshopContentDir(root, workshopID)
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) > 0 {
			return dir, nil
		}
	}
	return "", fmt.Errorf("SteamCMD reported success but item %s has no files", workshopID)
}

// WorkshopState reads what Steam recorded for the items downloaded under a
// root, so the manager can tell an installed copy from a newer one without
// asking Steam again.
func WorkshopState(root string) (map[string]WorkshopItem, error) {
	path := filepath.Join(root, "steamapps", "workshop", "appworkshop_"+gameAppID+".acf")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workshop manifest: %w", err)
	}
	return parseWorkshopState(string(data))
}

func parseWorkshopState(text string) (map[string]WorkshopItem, error) {
	tree, err := parseVDF(text)
	if err != nil {
		return nil, err
	}
	app, _ := tree["AppWorkshop"].(map[string]any)
	if app == nil {
		return nil, fmt.Errorf("workshop manifest has no AppWorkshop block")
	}
	items := map[string]WorkshopItem{}
	// Installed items carry the size on disk; the details block carries the
	// same update time for items Steam knows about but has not stored yet.
	for _, name := range []string{"WorkshopItemsInstalled", "WorkshopItemDetails"} {
		block, _ := app[name].(map[string]any)
		for id, raw := range block {
			entry, _ := raw.(map[string]any)
			if entry == nil {
				continue
			}
			item := items[id]
			if seconds, err := strconv.ParseInt(stringValue(entry["timeupdated"]), 10, 64); err == nil && seconds > 0 {
				item.Updated = time.Unix(seconds, 0).UTC()
			}
			if size, err := strconv.ParseInt(stringValue(entry["size"]), 10, 64); err == nil && size > 0 {
				item.Size = size
			}
			if manifest := stringValue(entry["manifest"]); manifest != "" {
				item.Manifest = manifest
			}
			items[id] = item
		}
	}
	return items, nil
}

func stringValue(raw any) string {
	value, _ := raw.(string)
	return value
}
