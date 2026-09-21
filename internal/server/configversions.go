package server

import (
	"context"
	"fmt"
	"time"
)

// ConfigVersion is the content a game INI file had before a change. Keeping
// the previous content, not the new one, is what makes a change reversible.
type ConfigVersion struct {
	ID        int64     `json:"id"`
	ServerID  string    `json:"server_id"`
	File      string    `json:"file"`
	CreatedAt time.Time `json:"created_at"`
	Reason    string    `json:"reason"`
	Size      int       `json:"size"`
	Content   string    `json:"content,omitempty"`
}

const configVersionsKept = 50

func (r *Repository) SaveConfigVersion(ctx context.Context, v ConfigVersion) (int64, error) {
	result, err := r.db.ExecContext(ctx, `INSERT INTO config_versions(server_id,file,created_at,reason,content) VALUES (?,?,?,?,?)`,
		v.ServerID, v.File, time.Now().UTC().Format(time.RFC3339Nano), v.Reason, v.Content)
	if err != nil {
		return 0, fmt.Errorf("save config version: %w", err)
	}
	id, _ := result.LastInsertId()
	_, err = r.db.ExecContext(ctx, `DELETE FROM config_versions WHERE server_id=? AND file=? AND id NOT IN
        (SELECT id FROM config_versions WHERE server_id=? AND file=? ORDER BY id DESC LIMIT ?)`,
		v.ServerID, v.File, v.ServerID, v.File, configVersionsKept)
	return id, err
}

// ConfigVersions lists versions of one file without their content.
func (r *Repository) ConfigVersions(ctx context.Context, serverID, file string) ([]ConfigVersion, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,server_id,file,created_at,reason,length(content) FROM config_versions
        WHERE server_id=? AND file=? ORDER BY id DESC`, serverID, file)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ConfigVersion, 0)
	for rows.Next() {
		var v ConfigVersion
		var created any
		if err := rows.Scan(&v.ID, &v.ServerID, &v.File, &created, &v.Reason, &v.Size); err != nil {
			return nil, err
		}
		v.CreatedAt = parseDatabaseTime(created)
		result = append(result, v)
	}
	return result, rows.Err()
}

func (r *Repository) ConfigVersion(ctx context.Context, serverID, file string, id int64) (ConfigVersion, error) {
	var v ConfigVersion
	var created any
	err := r.db.QueryRowContext(ctx, `SELECT id,server_id,file,created_at,reason,content FROM config_versions
        WHERE server_id=? AND file=? AND id=?`, serverID, file, id).Scan(&v.ID, &v.ServerID, &v.File, &created, &v.Reason, &v.Content)
	if err != nil {
		return v, fmt.Errorf("config version %d not found", id)
	}
	v.CreatedAt, v.Size = parseDatabaseTime(created), len(v.Content)
	return v, nil
}
