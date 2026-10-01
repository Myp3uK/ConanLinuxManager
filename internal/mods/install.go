package mods

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/example/conan-manager/internal/server"
)

// Install puts a downloaded item's .pak files where the game looks for them and
// returns their names. The download cache is shared between servers, so the
// files are hard-linked when the two directories are on one filesystem: a mod
// three servers run then costs its size once. A copy is the fallback, because
// a cache on another mount is a configuration an operator may well choose.
func Install(cfg server.Config, sourceDir, workshopID string) ([]string, error) {
	paks, err := PakNames(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("read downloaded mod %s: %w", workshopID, err)
	}
	if len(paks) == 0 {
		return nil, fmt.Errorf("mod %s downloaded no .pak file", workshopID)
	}
	target := ItemDir(cfg, workshopID)
	// The old copy goes first: a mod that shipped two paks and now ships one
	// would otherwise keep loading the file it no longer has.
	if err := os.RemoveAll(target); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(target, 0o750); err != nil {
		return nil, err
	}
	for _, pak := range paks {
		from := filepath.Join(sourceDir, pak)
		to := filepath.Join(target, pak)
		if err := os.Link(from, to); err == nil {
			continue
		}
		if err := copyFile(from, to); err != nil {
			return nil, fmt.Errorf("place %s of mod %s: %w", pak, workshopID, err)
		}
	}
	return paks, nil
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	return destination.Close()
}

// itemDirPattern matches the directories Install creates. Anything else in the
// Mods directory was put there by somebody else and is left alone.
var itemDirPattern = regexp.MustCompile(`^[1-9][0-9]{5,19}$`)

// RemoveOrphans deletes the files of mods that left the list. Disabled mods
// keep theirs: an operator who turns a mod off for a week should not pay for
// the download again.
func RemoveOrphans(cfg server.Config, list []server.Mod) error {
	entries, err := os.ReadDir(Dir(cfg))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(list))
	for _, m := range list {
		keep[m.WorkshopID] = true
	}
	var failures []string
	for _, entry := range entries {
		if !entry.IsDir() || keep[entry.Name()] || !itemDirPattern.MatchString(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(join(Dir(cfg), entry.Name())); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("remove leftover mod files: %s", strings.Join(failures, "; "))
	}
	return nil
}
