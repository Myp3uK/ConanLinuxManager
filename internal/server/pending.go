package server

import (
	"context"
	"time"
)

// Reasons for a delayed restart.
const (
	PendingManual = "manual"
	PendingUpdate = "update"
)

// PendingRestart is a restart set for a fixed time ahead: by an operator, or
// to install an update. It is stored so a restart of the manager keeps it.
type PendingRestart struct {
	ServerID  string    `json:"-"`
	Reason    string    `json:"reason"`
	Detail    string    `json:"detail,omitempty"`
	DueAt     time.Time `json:"due_at"`
	CreatedAt time.Time `json:"created_at"`
}

// MarkReady records that the run of a server started at started was
// announced as ready, so the manager does not announce it again after its
// own restart.
func (r *Repository) MarkReady(ctx context.Context, id string, started time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO server_ready(server_id,started_at) VALUES(?,?)
		ON CONFLICT(server_id) DO UPDATE SET started_at=excluded.started_at`, id, started.UTC().Truncate(time.Second).Format(time.RFC3339))
	return err
}

// WasReady reports whether the run started at started was announced ready.
// The start time the manager records itself and the one systemd reports
// differ by up to a second.
func (r *Repository) WasReady(ctx context.Context, id string, started time.Time) bool {
	var raw string
	if err := r.db.QueryRowContext(ctx, `SELECT started_at FROM server_ready WHERE server_id=?`, id).Scan(&raw); err != nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return (at.Sub(started) < 3*time.Second) && (started.Sub(at) < 3*time.Second)
}

func (r *Repository) SetPendingRestart(ctx context.Context, p PendingRestart) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO pending_restarts(server_id,reason,detail,due_at,created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET reason=excluded.reason,detail=excluded.detail,due_at=excluded.due_at,created_at=excluded.created_at`,
		p.ServerID, p.Reason, p.Detail, p.DueAt.UTC().Format(time.RFC3339Nano), p.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (r *Repository) DeletePendingRestart(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pending_restarts WHERE server_id=?`, id)
	return err
}

func (r *Repository) PendingRestarts(ctx context.Context) ([]PendingRestart, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT server_id,reason,detail,due_at,created_at FROM pending_restarts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []PendingRestart
	for rows.Next() {
		var p PendingRestart
		var due, created string
		if err := rows.Scan(&p.ServerID, &p.Reason, &p.Detail, &due, &created); err != nil {
			return nil, err
		}
		p.DueAt, _ = time.Parse(time.RFC3339Nano, due)
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		list = append(list, p)
	}
	return list, rows.Err()
}
