package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// HookEvents are the points at which operator scripts can run.
var HookEvents = []string{
	"pre_start", "post_start", "on_ready",
	"pre_stop", "post_stop", "on_crash",
	"pre_backup", "post_backup",
	"pre_update", "post_update",
}

var hookName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidHookName accepts a plain file name: no directory, no leading dot, so a
// configured script can only ever be a file inside the hook directory.
func ValidHookName(name string) bool { return hookName.MatchString(name) }

const maxScriptsPerEvent = 10

func validateHooks(hooks map[string][]string) error {
	known := make(map[string]bool, len(HookEvents))
	for _, event := range HookEvents {
		known[event] = true
	}
	for event, scripts := range hooks {
		if !known[event] {
			return ErrInvalidConfig(fmt.Sprintf("unknown hook event %q", event))
		}
		if len(scripts) > maxScriptsPerEvent {
			return ErrInvalidConfig(fmt.Sprintf("at most %d scripts per hook event", maxScriptsPerEvent))
		}
		for _, name := range scripts {
			if !ValidHookName(name) {
				return ErrInvalidConfig(fmt.Sprintf("invalid hook script name %q", name))
			}
		}
	}
	return nil
}

// HookRun is one execution of one script.
type HookRun struct {
	ID         int64     `json:"id"`
	ServerID   string    `json:"server_id"`
	Event      string    `json:"event"`
	Script     string    `json:"script"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	ExitCode   int       `json:"exit_code"`
	TimedOut   bool      `json:"timed_out"`
	Output     string    `json:"output"`
	Error      string    `json:"error,omitempty"`
}

// hookRunsKept is how many runs are kept per server.
const hookRunsKept = 500

// RecordHookRun stores a run and prunes the oldest beyond the retention.
func (r *Repository) RecordHookRun(ctx context.Context, run HookRun) error {
	var errText any
	if run.Error != "" {
		errText = run.Error
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO hook_runs
        (server_id,event,script,started_at,finished_at,exit_code,timed_out,output,error)
        VALUES (?,?,?,?,?,?,?,?,?)`,
		run.ServerID, run.Event, run.Script, run.StartedAt.Format(time.RFC3339Nano),
		run.FinishedAt.Format(time.RFC3339Nano), run.ExitCode, run.TimedOut, run.Output, errText); err != nil {
		return fmt.Errorf("record hook run: %w", err)
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM hook_runs WHERE server_id=? AND id NOT IN
        (SELECT id FROM hook_runs WHERE server_id=? ORDER BY id DESC LIMIT ?)`, run.ServerID, run.ServerID, hookRunsKept)
	return err
}

// HookRuns returns the newest runs of a server, newest first.
func (r *Repository) HookRuns(ctx context.Context, serverID string, limit int) ([]HookRun, error) {
	if limit < 1 || limit > hookRunsKept {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,server_id,event,script,started_at,finished_at,exit_code,timed_out,output,error
        FROM hook_runs WHERE server_id=? ORDER BY id DESC LIMIT ?`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]HookRun, 0)
	for rows.Next() {
		var run HookRun
		var started, finished any
		var timedOut int
		var errText sql.NullString
		if err := rows.Scan(&run.ID, &run.ServerID, &run.Event, &run.Script, &started, &finished, &run.ExitCode, &timedOut, &run.Output, &errText); err != nil {
			return nil, err
		}
		run.StartedAt, run.FinishedAt = parseDatabaseTime(started), parseDatabaseTime(finished)
		run.TimedOut, run.Error = timedOut != 0, errText.String
		result = append(result, run)
	}
	return result, rows.Err()
}

func encodeHooks(hooks map[string][]string) (string, error) {
	if len(hooks) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(hooks)
	return string(data), err
}

func decodeHooks(raw string) map[string][]string {
	hooks := map[string][]string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &hooks)
	}
	return hooks
}
