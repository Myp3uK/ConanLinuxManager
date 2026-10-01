package steamcmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	// Manifest identifies the exact content. It is the same value the Steam
	// API calls hcontent_file, so the two sources can be compared directly
	// when a timestamp is in doubt.
	Manifest string
	// LatestUpdated and LatestManifest are what SteamCMD last heard was
	// available, which it records alongside what it installed. They are only
	// as fresh as the last time SteamCMD spoke to Steam about the item, so
	// they serve as a cross-check and as a fallback when Steam's API cannot be
	// reached, not as the periodic check.
	LatestUpdated  time.Time
	LatestManifest string
}

// workshopArgs is the command line for a whole set of items. They go into one
// invocation because starting SteamCMD costs far more than most downloads do:
// it updates itself, connects to Steam and logs in every time, so forty mods
// fetched one at a time spend most of their time starting up.
//
// The platform override is not optional: Conan publishes mod paks in the
// Windows depot, and a SteamCMD running as Linux is told there is nothing to
// download.
func workshopArgs(contentDir string, ids []string, validate bool) []string {
	args := []string{
		"+@sSteamCmdForcePlatformType", "windows",
		"+force_install_dir", contentDir,
		"+login", "anonymous",
	}
	for _, id := range ids {
		args = append(args, "+workshop_download_item", gameAppID, id)
		if validate {
			args = append(args, "validate")
		}
	}
	return append(args, "+quit")
}

// downloadBatch bounds one invocation. A transient failure retries the whole
// command line, so an unbounded batch would mean re-running a hundred items
// because the last one stumbled.
const downloadBatch = 25

// WorkshopResult is what one run did with one item.
type WorkshopResult struct {
	// Dir holds the files when the item arrived.
	Dir string
	// Reason is SteamCMD's own words when it did not.
	Reason string
}

// DownloadWorkshopItems fetches a set of Workshop items into contentDir and
// reports each one separately. An item SteamCMD refuses does not fail the
// call: the others in the same run still arrive, and the caller is told which
// is which.
func (s Service) DownloadWorkshopItems(ctx context.Context, steamcmdDir, contentDir string, ids []string, validate bool, output io.Writer) (map[string]WorkshopResult, error) {
	binary, err := s.Ensure(ctx, steamcmdDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(contentDir, 0o750); err != nil {
		return nil, err
	}
	if output == nil {
		output = s.Output
	}
	defer lockDir(steamcmdDir)()
	results := make(map[string]WorkshopResult, len(ids))
	var lastErr error
	for start := 0; start < len(ids); start += downloadBatch {
		end := start + downloadBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		// The output is read as well as written: SteamCMD reports each item on
		// its own line, while its exit status reflects only the last one.
		seen := &downloadReader{}
		var writer io.Writer = seen
		if output != nil {
			writer = io.MultiWriter(output, seen)
		}
		runErr := runUpdate(ctx, binary, steamcmdDir, workshopArgs(contentDir, batch, validate), writer)
		seen.flush()
		for _, id := range batch {
			result, told := seen.results[id]
			switch {
			case told && result.Dir != "":
				// Trust the path SteamCMD printed, but look for the files if
				// it does not lead anywhere: its wording has changed before.
				if _, err := os.Stat(result.Dir); err != nil {
					if dir, err := workshopItemDir(contentDir, steamcmdDir, id); err == nil {
						result.Dir = dir
					}
				}
			case !told:
				dir, dirErr := workshopItemDir(contentDir, steamcmdDir, id)
				switch {
				case dirErr == nil && runErr == nil:
					result.Dir = dir
				case runErr != nil:
					result.Reason = runErr.Error()
				default:
					result.Reason = "SteamCMD said nothing about this mod"
				}
			}
			results[id] = result
		}
		if runErr != nil {
			lastErr = runErr
		}
	}
	// A run that produced nothing at all is a failure of the call; one that
	// produced something is reported item by item.
	for _, result := range results {
		if result.Dir != "" {
			return results, nil
		}
	}
	if lastErr != nil {
		return results, fmt.Errorf("SteamCMD workshop_download_item: %w", lastErr)
	}
	return results, nil
}

// downloadReader picks each item's outcome out of SteamCMD's output.
type downloadReader struct {
	tail    []byte
	results map[string]WorkshopResult
}

var (
	workshopDone   = regexp.MustCompile(`Success\. Downloaded item ([0-9]+) to "([^"]+)"`)
	workshopFailed = regexp.MustCompile(`(?i)ERROR!? Download item ([0-9]+) failed \(([^)]*)\)`)
)

// tailLimit bounds the partial line held between writes, so output without a
// line break cannot grow without end.
const tailLimit = 1 << 16

func (d *downloadReader) Write(p []byte) (int, error) {
	d.tail = append(d.tail, p...)
	// SteamCMD writes its progress with carriage returns, so a result can
	// arrive on either kind of line ending.
	text := strings.ReplaceAll(string(d.tail), "\r", "\n")
	cut := strings.LastIndexByte(text, '\n')
	if cut < 0 {
		// Hold a partial line back: a result split across two writes must not
		// be read as two broken ones.
		if len(d.tail) > tailLimit {
			d.tail = d.tail[len(d.tail)-tailLimit:]
		}
		return len(p), nil
	}
	d.read(text[:cut])
	d.tail = []byte(text[cut+1:])
	return len(p), nil
}

// flush reads the last line, which a process that ends without a line break
// would otherwise leave unread.
func (d *downloadReader) flush() {
	if len(d.tail) > 0 {
		d.read(string(d.tail))
		d.tail = nil
	}
}

func (d *downloadReader) read(text string) {
	if d.results == nil {
		d.results = map[string]WorkshopResult{}
	}
	for _, line := range strings.Split(stripANSI(text), "\n") {
		if match := workshopDone.FindStringSubmatch(line); match != nil {
			d.results[match[1]] = WorkshopResult{Dir: match[2]}
			continue
		}
		if match := workshopFailed.FindStringSubmatch(line); match != nil {
			reason := strings.TrimSpace(match[2])
			if reason == "" {
				reason = "SteamCMD gave no reason"
			}
			d.results[match[1]] = WorkshopResult{Reason: reason}
		}
	}
}

// ansiEscape matches the colour codes SteamCMD writes between its words; they
// sit in the middle of the lines this file reads.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

func stripANSI(text string) string {
	if !strings.ContainsRune(text, 0x1b) {
		return text
	}
	return strings.ReplaceAll(ansiEscape.ReplaceAllString(text, ""), "\x1b", "")
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
			if seconds, err := strconv.ParseInt(stringValue(entry["latest_timeupdated"]), 10, 64); err == nil && seconds > 0 {
				item.LatestUpdated = time.Unix(seconds, 0).UTC()
			}
			if manifest := stringValue(entry["latest_manifest"]); manifest != "" {
				item.LatestManifest = manifest
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
