package gameconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/example/conan-manager/internal/server"
)

// Files are the game's own INI files, which the manager knows by a short name.
// They are not the only ones it will edit: mods bring their own, and an
// operator may add any .ini the game or a mod reads.
var Files = map[string]string{
	"ServerSettings": "ServerSettings.ini",
	"Game":           "Game.ini",
	"Engine":         "Engine.ini",
}

// configName accepts a file an operator adds: a plain name ending in .ini, with
// no directory part and no leading dot, so a name can only ever mean a file
// inside the server's own config directory.
var configName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}\.ini$`)

// ValidConfigName reports whether a name may be used for a config file.
func ValidConfigName(name string) bool {
	return configName.MatchString(name) && !strings.Contains(name, "..")
}

// FileName turns what the API was given into the file on disk it means: a
// short name for one of the game's own files, or the file name itself for one
// an operator added.
func FileName(file string) (string, error) {
	if name, ok := Files[file]; ok {
		return name, nil
	}
	if ValidConfigName(file) {
		return file, nil
	}
	return "", fmt.Errorf("invalid config file %q: use ServerSettings, Game, Engine or a file name ending in .ini", file)
}

// Dir is the directory the running server reads its INI files from.
func Dir(cfg server.Config) string { return filepath.Dir(Path(cfg)) }

// FilePath is where the running server reads the named file.
func FilePath(cfg server.Config, file string) (string, error) {
	name, err := FileName(file)
	if err != nil {
		return "", err
	}
	return filepath.Join(Dir(cfg), name), nil
}

// File is one INI file of a server as the panel lists it.
type File struct {
	// Name is what the API takes: a short name for the game's own files, the
	// file name for the rest.
	Name string `json:"name"`
	// File is the name on disk.
	File   string `json:"file"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size"`
	// Custom marks a file an operator or a mod added rather than one the game
	// writes itself. Only these may be removed.
	Custom bool `json:"custom"`
}

// List returns the INI files a server has: the game's own ones first, in a
// fixed order so the panel does not reshuffle its buttons, then whatever else
// the directory holds.
func List(cfg server.Config) ([]File, error) {
	known := []string{"ServerSettings", "Game", "Engine"}
	onDisk := map[string]int64{}
	entries, err := os.ReadDir(Dir(cfg))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !ValidConfigName(entry.Name()) {
			// Skips the manager's own ".conan-manager.tmp" among others.
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		onDisk[entry.Name()] = info.Size()
	}
	result := make([]File, 0, len(known)+len(onDisk))
	for _, name := range known {
		file := Files[name]
		size, exists := onDisk[file]
		delete(onDisk, file)
		result = append(result, File{Name: name, File: file, Exists: exists, Size: size})
	}
	rest := make([]string, 0, len(onDisk))
	for file := range onDisk {
		rest = append(rest, file)
	}
	sort.Strings(rest)
	for _, file := range rest {
		result = append(result, File{Name: file, File: file, Exists: true, Size: onDisk[file], Custom: true})
	}
	return result, nil
}

// Entry is one key=value line.
type Entry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Section groups the entries under one [header], in file order. A key listed
// more than once is an Unreal array and appears once per value.
type Section struct {
	Name    string  `json:"name"`
	Entries []Entry `json:"entries"`
}

type lineKind int

const (
	lineOther lineKind = iota
	lineBlank
	lineComment
	lineSection
	lineEntry
)

type iniLine struct {
	raw     string
	kind    lineKind
	section string
	key     string
	value   string
	cr      bool
}

func parseLines(content string) []iniLine {
	var lines []iniLine
	section := ""
	for _, raw := range strings.Split(content, "\n") {
		l := iniLine{raw: raw, section: section}
		text := raw
		if strings.HasSuffix(text, "\r") {
			l.cr = true
			text = strings.TrimSuffix(text, "\r")
			l.raw = text
		}
		trimmed := strings.TrimSpace(text)
		switch {
		case trimmed == "":
			l.kind = lineBlank
		case strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#"):
			l.kind = lineComment
		case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
			l.kind = lineSection
			section = trimmed[1 : len(trimmed)-1]
			l.section = section
		default:
			if key, value, ok := strings.Cut(trimmed, "="); ok {
				l.kind, l.key, l.value = lineEntry, strings.TrimSpace(key), value
			}
		}
		lines = append(lines, l)
	}
	return lines
}

func joinLines(lines []iniLine) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = l.raw
		if l.cr {
			parts[i] += "\r"
		}
	}
	return strings.Join(parts, "\n")
}

// Parse lists the sections and entries of an INI file for display.
func Parse(content string) []Section {
	var result []Section
	index := map[string]int{}
	for _, l := range parseLines(content) {
		if l.kind != lineSection && l.kind != lineEntry {
			continue
		}
		i, ok := index[l.section]
		if !ok {
			i = len(result)
			index[l.section] = i
			result = append(result, Section{Name: l.section, Entries: []Entry{}})
		}
		if l.kind == lineEntry {
			result[i].Entries = append(result[i].Entries, Entry{Key: l.key, Value: l.value})
		}
	}
	return result
}

// Change is one key an edit set or removed, for history and events.
type Change struct {
	Section string `json:"section"`
	Key     string `json:"key"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

var (
	validKey     = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)
	validSection = regexp.MustCompile(`^[^\[\]\r\n]{1,200}$`)
)

// Edit sets and removes keys and leaves every other line of the file exactly
// as it was: comments, order, the game's own metadata and line endings. Keys
// are matched without regard to case, as Unreal does.
func Edit(content string, set map[string]map[string]string, remove map[string][]string) (string, []Change, error) {
	for section, keys := range set {
		if !validSection.MatchString(section) {
			return "", nil, fmt.Errorf("invalid section name %q", section)
		}
		for key, value := range keys {
			if !validKey.MatchString(key) {
				return "", nil, fmt.Errorf("invalid key %q", key)
			}
			if strings.ContainsAny(value, "\r\n") {
				return "", nil, fmt.Errorf("value of %s may not span lines", key)
			}
		}
	}
	for section, keys := range remove {
		if !validSection.MatchString(section) {
			return "", nil, fmt.Errorf("invalid section name %q", section)
		}
		for _, key := range keys {
			if !validKey.MatchString(key) {
				return "", nil, fmt.Errorf("invalid key %q", key)
			}
		}
	}
	lines := parseLines(content)
	var changes []Change

	for _, section := range sortedKeys(remove) {
		for _, key := range remove[section] {
			kept := lines[:0:0]
			for _, l := range lines {
				if l.kind == lineEntry && l.section == section && strings.EqualFold(l.key, key) {
					changes = append(changes, Change{Section: section, Key: l.key, Old: l.value, Removed: true})
					continue
				}
				kept = append(kept, l)
			}
			lines = kept
		}
	}

	for _, section := range sortedKeys(set) {
		for _, key := range sortedKeys(set[section]) {
			value := set[section][key]
			var matches []int
			for i, l := range lines {
				if l.kind == lineEntry && l.section == section && strings.EqualFold(l.key, key) {
					matches = append(matches, i)
				}
			}
			switch len(matches) {
			case 0:
				lines = insertEntry(lines, section, key, value)
				changes = append(changes, Change{Section: section, Key: key, New: value})
			case 1:
				l := &lines[matches[0]]
				if l.value == value {
					continue
				}
				changes = append(changes, Change{Section: section, Key: l.key, Old: l.value, New: value})
				l.value = value
				l.raw = l.key + "=" + value
			default:
				// An Unreal array: replacing one of its values would be a guess.
				return "", nil, fmt.Errorf("%s in [%s] has %d values; edit the file as text", key, section, len(matches))
			}
		}
	}
	return joinLines(lines), changes, nil
}

// insertEntry adds a key after the last entry of its section, or appends the
// section when the file does not have it yet.
func insertEntry(lines []iniLine, section, key, value string) []iniLine {
	entry := iniLine{raw: key + "=" + value, kind: lineEntry, section: section, key: key, value: value}
	last := -1
	for i, l := range lines {
		if l.section == section && (l.kind == lineEntry || l.kind == lineSection) {
			last = i
		}
	}
	if last >= 0 {
		entry.cr = lines[last].cr
		out := append([]iniLine{}, lines[:last+1]...)
		out = append(out, entry)
		return append(out, lines[last+1:]...)
	}
	for len(lines) > 0 && lines[len(lines)-1].kind == lineBlank {
		lines = lines[:len(lines)-1]
	}
	cr := len(lines) > 0 && lines[0].cr
	if len(lines) > 0 {
		lines = append(lines, iniLine{kind: lineBlank, cr: cr})
	}
	header := iniLine{raw: "[" + section + "]", kind: lineSection, section: section, cr: cr}
	entry.cr = cr
	return append(lines, header, entry, iniLine{kind: lineBlank})
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
