// Package mods owns the files a server's Steam Workshop mods occupy inside the
// install directory: where each item's .pak files live and the modlist.txt the
// game reads to load them.
package mods

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/example/conan-manager/internal/server"
)

// listName is the file the game reads. Its name is fixed by Conan.
const listName = "modlist.txt"

// join builds a path for the target host rather than the build host. The
// manager writes these paths into a file a Linux game server reads, so they
// must come out with forward slashes even when the package is compiled and
// tested on Windows.
func join(parts ...string) string {
	return strings.Join(parts, "/")
}

// Dir is the Mods directory of a server. The game looks for modlist.txt here.
func Dir(cfg server.Config) string {
	return join(strings.TrimRight(cfg.InstallDir, "/"), "ConanSandbox", "Mods")
}

// ItemDir is where one Workshop item's files are kept. Each item gets its own
// directory so that two mods shipping a pak of the same name cannot overwrite
// each other, and so removing a mod is removing one directory.
func ItemDir(cfg server.Config, workshopID string) string {
	return join(Dir(cfg), workshopID)
}

// ListPath is the modlist.txt of a server.
func ListPath(cfg server.Config) string { return join(Dir(cfg), listName) }

// Render builds the modlist.txt content: one absolute path to a .pak per line,
// in load order, for the enabled mods that have files on disk. Absolute paths
// are unambiguous whatever directory the game is started from, which matters
// because the manager writes this file and nobody is reading it by hand.
func Render(cfg server.Config, list []server.Mod) string {
	ordered := make([]server.Mod, len(list))
	copy(ordered, list)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })
	var b strings.Builder
	for _, m := range ordered {
		if !m.Enabled {
			continue
		}
		for _, pak := range m.Paks {
			b.WriteString(join(ItemDir(cfg, m.WorkshopID), pak))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// Entry is one line of an existing modlist.txt.
type Entry struct {
	// WorkshopID is the item the line belongs to, empty for a .pak that is not
	// from the Workshop or whose id cannot be told from the path.
	WorkshopID string
	// Pak is the file name the line points at.
	Pak string
	// Raw is the line as written, so a line the manager cannot attribute can
	// still be shown to the operator.
	Raw string
}

// workshopDirPattern matches a path segment that is a Workshop id. Six digits
// is the shortest published file id Steam ever issued, and the bound keeps a
// directory named for a date or a version from being read as an id.
var workshopDirPattern = regexp.MustCompile(`^[1-9][0-9]{5,19}$`)

// ParseList reads an existing modlist.txt, in the forms an operator may have
// written it: absolute paths, paths relative to the Mods directory, and the
// asterisk form the game accepts. It is used to import a server that was set
// up by hand before the manager took it over.
func ParseList(content string) []Entry {
	entries := make([]Entry, 0)
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		// The asterisk form resolves the rest against the Mods directory.
		path := strings.TrimPrefix(line, "*")
		path = strings.ReplaceAll(path, "\\", "/")
		segments := strings.Split(strings.Trim(path, "/"), "/")
		entry := Entry{Raw: line, Pak: segments[len(segments)-1]}
		// The id is the last directory that looks like one: both our own
		// Mods/<id>/x.pak and Steam's workshop/content/440900/<id>/x.pak end
		// that way.
		for i := len(segments) - 2; i >= 0; i-- {
			if workshopDirPattern.MatchString(segments[i]) {
				entry.WorkshopID = segments[i]
				break
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// Apply writes modlist.txt for the given mods and returns what the file held
// before. An unchanged file is left alone, so its modification time keeps
// meaning "when the mod list last changed".
func Apply(cfg server.Config, list []server.Mod) (previous string, changed bool, err error) {
	path := ListPath(cfg)
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", false, fmt.Errorf("read mod list: %w", err)
	}
	wanted := Render(cfg, list)
	if string(current) == wanted {
		return string(current), false, nil
	}
	if err := os.MkdirAll(Dir(cfg), 0o750); err != nil {
		return string(current), false, err
	}
	if err := os.WriteFile(path, []byte(wanted), 0o640); err != nil {
		return string(current), false, fmt.Errorf("write mod list: %w", err)
	}
	return string(current), true, nil
}

// PakNames lists the .pak files an item installed, which is what a modlist.txt
// line points at. The order is stable so that a rebuilt list does not reorder
// a mod's own files.
func PakNames(itemDir string) ([]string, error) {
	entries, err := os.ReadDir(itemDir)
	if err != nil {
		return nil, err
	}
	paks := make([]string, 0, 1)
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".pak") {
			continue
		}
		paks = append(paks, e.Name())
	}
	sort.Strings(paks)
	return paks, nil
}
