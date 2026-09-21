package server

import (
	"context"
	"fmt"
	"time"
)

// Backup is one world copy the manager took.
type Backup struct {
	ID        int64     `json:"id"`
	ServerID  string    `json:"server_id"`
	Path      string    `json:"path"`
	Source    string    `json:"source"`
	Reason    string    `json:"reason"`
	Size      int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

const backupColumns = `id,server_id,destination_path,source_path,reason,size_bytes,sha256,created_at`

func scanBackup(scan func(...any) error) (Backup, error) {
	var b Backup
	var created any
	err := scan(&b.ID, &b.ServerID, &b.Path, &b.Source, &b.Reason, &b.Size, &b.SHA256, &created)
	b.CreatedAt = parseDatabaseTime(created)
	return b, err
}

func (r *Repository) AddBackup(ctx context.Context, b Backup) (Backup, error) {
	b.CreatedAt = time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `INSERT INTO backup_events(server_id,source_path,destination_path,reason,size_bytes,sha256,created_at)
        VALUES (?,?,?,?,?,?,?)`, b.ServerID, b.Source, b.Path, b.Reason, b.Size, b.SHA256, b.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return b, fmt.Errorf("record backup: %w", err)
	}
	b.ID, _ = result.LastInsertId()
	return b, nil
}

// Backups lists a server's backups, newest first.
func (r *Repository) Backups(ctx context.Context, serverID string) ([]Backup, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+backupColumns+` FROM backup_events WHERE server_id=? ORDER BY id DESC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Backup, 0)
	for rows.Next() {
		b, err := scanBackup(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func (r *Repository) Backup(ctx context.Context, serverID string, id int64) (Backup, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+backupColumns+` FROM backup_events WHERE server_id=? AND id=?`, serverID, id)
	b, err := scanBackup(row.Scan)
	if err != nil {
		return b, fmt.Errorf("backup %d of server %q not found", id, serverID)
	}
	return b, nil
}

func (r *Repository) DeleteBackup(ctx context.Context, serverID string, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM backup_events WHERE server_id=? AND id=?`, serverID, id)
	return err
}
