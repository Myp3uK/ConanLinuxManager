package steamcmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// SteamCMD keeps state in its own directory and misbehaves when two copies run
// against it at once, so every invocation for a directory is serialised.
var (
	locksMu sync.Mutex
	locks   = map[string]*sync.Mutex{}
)

func lockDir(dir string) func() {
	locksMu.Lock()
	l := locks[dir]
	if l == nil {
		l = &sync.Mutex{}
		locks[dir] = l
	}
	locksMu.Unlock()
	l.Lock()
	return l.Unlock
}

// InstalledBuild reads the build id Steam recorded for the installation.
func InstalledBuild(installDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(installDir, "steamapps", "appmanifest_"+conanAppID+".acf"))
	if err != nil {
		return "", fmt.Errorf("read app manifest: %w", err)
	}
	tree, err := parseVDF(string(data))
	if err != nil {
		return "", err
	}
	state, _ := tree["AppState"].(map[string]any)
	build, _ := state["buildid"].(string)
	if build == "" {
		return "", fmt.Errorf("app manifest has no build id")
	}
	return build, nil
}

// LatestBuild asks Steam for the current build of a branch. app_info_update
// forces a refresh; without it SteamCMD answers from a cache that can be days
// old.
func (s Service) LatestBuild(ctx context.Context, steamcmdDir, branch string) (string, error) {
	if branch == "" {
		branch = "public"
	}
	binary, err := s.Ensure(ctx, steamcmdDir)
	if err != nil {
		return "", err
	}
	defer lockDir(steamcmdDir)()
	cmd := exec.CommandContext(ctx, binary, "+login", "anonymous", "+app_info_update", "1", "+app_info_print", conanAppID, "+quit")
	cmd.Dir = steamcmdDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("SteamCMD app_info_print: %w", err)
	}
	return buildFromAppInfo(string(out), branch)
}

func buildFromAppInfo(output, branch string) (string, error) {
	// The output starts with SteamCMD's own log lines; the VDF document begins
	// at the quoted app id.
	start := strings.Index(output, "\""+conanAppID+"\"")
	if start < 0 {
		return "", fmt.Errorf("SteamCMD returned no app info")
	}
	tree, err := parseVDF(output[start:])
	if err != nil {
		return "", err
	}
	app, _ := tree[conanAppID].(map[string]any)
	depots, _ := app["depots"].(map[string]any)
	branches, _ := depots["branches"].(map[string]any)
	entry, _ := branches[branch].(map[string]any)
	build, _ := entry["buildid"].(string)
	if build == "" {
		return "", fmt.Errorf("branch %q not found in app info", branch)
	}
	return build, nil
}

// UpdateServer installs or updates the server files. validate re-checks every
// file against Steam, which repairs a damaged install but reads all of it.
func (s Service) UpdateServer(ctx context.Context, steamcmdDir, installDir, branch string, validate bool, output io.Writer) error {
	binary, err := s.Ensure(ctx, steamcmdDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0o750); err != nil {
		return err
	}
	defer lockDir(steamcmdDir)()
	args := []string{"+force_install_dir", installDir, "+login", "anonymous", "+app_update", conanAppID}
	if branch != "" && branch != "public" {
		args = append(args, "-beta", branch)
	}
	if validate {
		args = append(args, "validate")
	}
	args = append(args, "+quit")
	if err := runUpdate(ctx, binary, steamcmdDir, args, output); err != nil {
		return fmt.Errorf("SteamCMD app_update: %w", err)
	}
	return nil
}

// parseVDF reads Valve's KeyValues text format: quoted keys followed by either
// a quoted value or a braced block.
func parseVDF(text string) (map[string]any, error) {
	tokens, err := tokenizeVDF(text)
	if err != nil {
		return nil, err
	}
	pos := 0
	var block func() (map[string]any, error)
	block = func() (map[string]any, error) {
		result := map[string]any{}
		for pos < len(tokens) {
			tok := tokens[pos]
			if tok == "}" {
				pos++
				return result, nil
			}
			if tok == "{" {
				return nil, fmt.Errorf("unexpected block in VDF")
			}
			key := tok
			pos++
			if pos >= len(tokens) {
				return nil, fmt.Errorf("VDF key %q has no value", key)
			}
			if tokens[pos] == "{" {
				pos++
				child, err := block()
				if err != nil {
					return nil, err
				}
				result[key] = child
				continue
			}
			result[key] = tokens[pos]
			pos++
		}
		return result, nil
	}
	return block()
}

// tokenizeVDF returns quoted strings (unquoted) and braces. Quoted strings keep
// the marker character stripped, so a value of "{" is not taken for a brace.
func tokenizeVDF(text string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '{', '}':
			tokens = append(tokens, string(c))
		case '"':
			var b strings.Builder
			i++
			for ; i < len(text) && text[i] != '"'; i++ {
				if text[i] == '\\' && i+1 < len(text) {
					i++
				}
				b.WriteByte(text[i])
			}
			if i >= len(text) {
				return nil, fmt.Errorf("unterminated string in VDF")
			}
			value := b.String()
			if value == "{" || value == "}" {
				value = " " + value
			}
			tokens = append(tokens, value)
		}
	}
	return tokens, nil
}
