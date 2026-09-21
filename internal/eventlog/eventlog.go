// Package eventlog stores lifecycle events in SQLite, so the history of crashes,
// kills and restarts survives a manager restart and can be shown later.
package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/events"
)

// Entry is a stored event with its row id, which pages the history.
type Entry struct {
	ID int64 `json:"id"`
	events.Event
}

type Log struct {
	db *sql.DB
	// Retention: whichever limit is reached first.
	KeepFor time.Duration
	MaxRows int
	mu      sync.Mutex
	since   int
}

func New(db *sql.DB) *Log {
	return &Log{db: db, KeepFor: 90 * 24 * time.Hour, MaxRows: 50000}
}

// pruneEvery bounds how often retention runs; pruning on every insert would
// rewrite the index for nothing.
const pruneEvery = 500

func (l *Log) Record(ctx context.Context, e events.Event) error {
	details, err := json.Marshal(e.Details)
	if err != nil {
		return err
	}
	if e.Details == nil {
		details = []byte("{}")
	}
	var serverID, serverName any
	if e.ServerID != "" {
		serverID, serverName = e.ServerID, e.ServerName
	}
	if _, err := l.db.ExecContext(ctx, `INSERT INTO events(time,type,severity,server_id,server_name,message,details)
        VALUES (?,?,?,?,?,?,?)`, e.Time.UTC().Format(time.RFC3339Nano), string(e.Type), string(e.Severity),
		serverID, serverName, e.Message, string(details)); err != nil {
		return fmt.Errorf("record event: %w", err)
	}
	l.mu.Lock()
	l.since++
	due := l.since >= pruneEvery
	if due {
		l.since = 0
	}
	l.mu.Unlock()
	if due {
		return l.Prune(ctx)
	}
	return nil
}

// Prune applies retention by age and by count.
func (l *Log) Prune(ctx context.Context) error {
	cutoff := time.Now().Add(-l.KeepFor).UTC().Format(time.RFC3339Nano)
	if _, err := l.db.ExecContext(ctx, `DELETE FROM events WHERE time < ?`, cutoff); err != nil {
		return err
	}
	_, err := l.db.ExecContext(ctx, `DELETE FROM events WHERE id <= (SELECT id FROM events ORDER BY id DESC LIMIT 1 OFFSET ?)`, l.MaxRows)
	return err
}

// Query selects a page of history, newest first.
type Query struct {
	ServerID string
	// Before returns only entries older than this id; zero means newest.
	Before int64
	Limit  int
}

func (l *Log) List(ctx context.Context, q Query) ([]Entry, error) {
	if q.Limit < 1 || q.Limit > 500 {
		q.Limit = 100
	}
	query := `SELECT id,time,type,severity,COALESCE(server_id,''),COALESCE(server_name,''),message,details FROM events WHERE 1=1`
	var args []any
	if q.ServerID != "" {
		query += ` AND server_id = ?`
		args = append(args, q.ServerID)
	}
	if q.Before > 0 {
		query += ` AND id < ?`
		args = append(args, q.Before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, q.Limit)
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Entry, 0)
	for rows.Next() {
		var entry Entry
		var at, typ, severity, details string
		if err := rows.Scan(&entry.ID, &at, &typ, &severity, &entry.ServerID, &entry.ServerName, &entry.Message, &details); err != nil {
			return nil, err
		}
		entry.Time, _ = time.Parse(time.RFC3339Nano, at)
		entry.Type, entry.Severity = events.Type(typ), events.Severity(severity)
		if details != "{}" && details != "null" {
			_ = json.Unmarshal([]byte(details), &entry.Details)
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}
