// Package hooks runs operator scripts around server lifecycle events: before
// and after a start, a stop, a backup or an update, when the server is ready
// and when it crashes.
//
// Scripts live only in a root-owned directory the manager cannot write. The API
// can choose which of those scripts run for which event, but it can never
// supply a script: otherwise access to the API would be code execution as the
// service user.
package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/server"
)

// DefaultDir is where operators install hook scripts.
const DefaultDir = "/etc/conan-manager/hooks"

// outputLimit bounds what is kept of a script's output. The tail is kept,
// because that is where the error usually is.
const outputLimit = 16 << 10

// Mode says how a hook point treats its scripts.
type Mode int

const (
	// Gate runs scripts in order and stops at the first failure, which aborts
	// the action: a failing pre_start keeps the server down.
	Gate Mode = iota
	// Wait runs every script and waits for them, but a failure does not abort
	// the action: a stop must always be possible.
	Wait
	// Async runs every script in the background after the event.
	Async
)

// Script describes one file in the hook directory.
type Script struct {
	Name    string `json:"name"`
	Usable  bool   `json:"usable"`
	Problem string `json:"problem,omitempty"`
}

type Runner struct {
	Dir     string
	Timeout time.Duration
	// Record stores each run; nil keeps runs only in the returned results.
	Record func(context.Context, server.HookRun) error
	Logger *slog.Logger
	// OnFailure is called for each script that did not succeed, whatever the
	// mode, so the failure can be reported.
	OnFailure func(cfg server.Config, failure *Failure)
	// trustOwner is overridden in tests, which cannot create root-owned files.
	trustOwner func(os.FileInfo) error
	wg         sync.WaitGroup
}

func New(dir string, timeout time.Duration) *Runner {
	if dir == "" {
		dir = DefaultDir
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Runner{Dir: dir, Timeout: timeout, Logger: slog.New(slog.DiscardHandler), trustOwner: ownedByRoot}
}

// Available lists the directory so an operator can see which scripts can be
// used and why the others cannot.
func (r *Runner) Available() ([]Script, error) {
	entries, err := os.ReadDir(r.Dir)
	if os.IsNotExist(err) {
		return []Script{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Script, 0, len(entries))
	for _, entry := range entries {
		if !server.ValidHookName(entry.Name()) {
			continue
		}
		script := Script{Name: entry.Name(), Usable: true}
		if _, err := r.resolve(entry.Name()); err != nil {
			script.Usable, script.Problem = false, err.Error()
		}
		result = append(result, script)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// resolve returns the path of a script that is safe to execute: a regular,
// executable file owned by root that no one else can modify, in a directory no
// one else can modify.
func (r *Runner) resolve(name string) (string, error) {
	if !server.ValidHookName(name) {
		return "", fmt.Errorf("invalid script name %q", name)
	}
	dir, err := os.Lstat(r.Dir)
	if err != nil {
		return "", err
	}
	if !dir.IsDir() {
		return "", fmt.Errorf("%s is not a directory", r.Dir)
	}
	if err := r.trustOwner(dir); err != nil {
		return "", fmt.Errorf("hook directory: %w", err)
	}
	path := filepath.Join(r.Dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("not executable")
	}
	if err := r.trustOwner(info); err != nil {
		return "", err
	}
	return path, nil
}

// Run executes the scripts configured for event. It returns an error only in
// Gate mode, where the error aborts the action that was about to happen.
func (r *Runner) Run(ctx context.Context, mode Mode, event string, cfg server.Config, extra map[string]string) error {
	scripts := cfg.Hooks[event]
	if len(scripts) == 0 {
		return nil
	}
	if mode == Async {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			_ = r.runAll(context.WithoutCancel(ctx), false, event, cfg, extra, scripts)
		}()
		return nil
	}
	err := r.runAll(ctx, mode == Gate, event, cfg, extra, scripts)
	if mode == Gate {
		return err
	}
	return nil
}

// Drain blocks until background hooks have finished or the timeout passes.
// Shutdown uses it so a post_stop script is not cut off by the manager exiting.
func (r *Runner) Drain(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Check reports whether a script can be used, so a configuration naming a
// missing or unsafe script is refused when it is saved, not when it runs.
func (r *Runner) Check(name string) error {
	_, err := r.resolve(name)
	return err
}

// Failure is reported for every script that did not succeed.
type Failure struct {
	Event, Script string
	Err           error
}

func (f *Failure) Error() string { return fmt.Sprintf("%s hook %s: %v", f.Event, f.Script, f.Err) }
func (f *Failure) Unwrap() error { return f.Err }

func (r *Runner) runAll(ctx context.Context, stopOnFailure bool, event string, cfg server.Config, extra map[string]string, scripts []string) error {
	var failures []error
	for _, name := range scripts {
		run := r.runOne(ctx, event, cfg, extra, name)
		if r.Record != nil {
			if err := r.Record(context.WithoutCancel(ctx), run); err != nil {
				r.Logger.Error("record hook run", "server", cfg.ID, "event", event, "script", name, "error", err)
			}
		}
		if run.Error == "" {
			continue
		}
		failure := &Failure{Event: event, Script: name, Err: errors.New(run.Error)}
		r.Logger.Warn("hook failed", "server", cfg.ID, "event", event, "script", name, "error", run.Error)
		if r.OnFailure != nil {
			r.OnFailure(cfg, failure)
		}
		failures = append(failures, failure)
		if stopOnFailure {
			break
		}
	}
	return errors.Join(failures...)
}

func (r *Runner) runOne(ctx context.Context, event string, cfg server.Config, extra map[string]string, name string) server.HookRun {
	run := server.HookRun{ServerID: cfg.ID, Event: event, Script: name, StartedAt: time.Now().UTC(), ExitCode: -1}
	path, err := r.resolve(name)
	if err != nil {
		run.Error = "refused: " + err.Error()
		run.FinishedAt = time.Now().UTC()
		return run
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Dir = cfg.InstallDir
	if _, err := os.Stat(cmd.Dir); err != nil {
		cmd.Dir = r.Dir
	}
	cmd.Env = environment(event, cfg, extra)
	output := &tailBuffer{limit: outputLimit}
	cmd.Stdout, cmd.Stderr = output, output
	isolate(cmd)
	err = cmd.Run()
	run.Output = output.String()
	run.FinishedAt = time.Now().UTC()
	if cmd.ProcessState != nil {
		run.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		run.TimedOut = true
		run.Error = fmt.Sprintf("timed out after %s", r.Timeout)
	case err != nil:
		run.Error = err.Error()
	}
	return run
}

// environment gives a script what it needs about the server and nothing of
// the manager's own environment, which holds the admin password and secret key.
func environment(event string, cfg server.Config, extra map[string]string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"CONAN_HOOK_EVENT=" + event,
		"CONAN_SERVER_ID=" + cfg.ID,
		"CONAN_SERVER_NAME=" + cfg.Name,
		"CONAN_INSTALL_DIR=" + cfg.InstallDir,
		"CONAN_SAVED_DIR=" + filepath.Join(cfg.InstallDir, "ConanSandbox", "Saved"),
		"CONAN_GAME_PORT=" + strconv.Itoa(cfg.GamePort),
		"CONAN_QUERY_PORT=" + strconv.Itoa(cfg.QueryPort),
	}
	if home := os.Getenv("HOME"); home != "" {
		env = append(env, "HOME="+home)
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+extra[key])
	}
	return env
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if over := b.buf.Len() - b.limit; over > 0 {
		b.buf.Next(over)
		b.truncated = true
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return "[output truncated]\n" + b.buf.String()
	}
	return b.buf.String()
}
