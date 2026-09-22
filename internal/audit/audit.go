// Package audit records who did what through the API: logins, lifecycle
// actions, configuration, backups and recovery. The event history says what
// happened to a server; the audit log says which administrator asked for it.
package audit

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"
)

// Entry is one recorded request. Action names the route, such as
// "POST /servers/{id}/start"; Target is the server, administrator or
// setting it touched. Request bodies are never stored: they carry passwords
// and webhook URLs.
type Entry struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Username string    `json:"username"`
	IP       string    `json:"ip"`
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	Status   int       `json:"status"`
	Details  string    `json:"details,omitempty"`
}

type Log struct {
	db *sql.DB
	// Retention: whichever limit is reached first.
	KeepFor time.Duration
	MaxRows int

	mu    sync.Mutex
	since int
}

func New(db *sql.DB) *Log {
	return &Log{db: db, KeepFor: 365 * 24 * time.Hour, MaxRows: 100000}
}

const pruneEvery = 500

func (l *Log) Record(ctx context.Context, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if len(e.Details) > 1000 {
		e.Details = e.Details[:1000]
	}
	if _, err := l.db.ExecContext(ctx, `INSERT INTO audit_log (time, username, ip, action, target, status, details)
		VALUES (?,?,?,?,?,?,?)`, e.Time.UTC().Format(time.RFC3339Nano), e.Username, e.IP, e.Action, e.Target, e.Status, e.Details); err != nil {
		return err
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

func (l *Log) Prune(ctx context.Context) error {
	cutoff := time.Now().Add(-l.KeepFor).UTC().Format(time.RFC3339Nano)
	if _, err := l.db.ExecContext(ctx, `DELETE FROM audit_log WHERE time < ?`, cutoff); err != nil {
		return err
	}
	_, err := l.db.ExecContext(ctx, `DELETE FROM audit_log WHERE id <= (SELECT id FROM audit_log ORDER BY id DESC LIMIT 1 OFFSET ?)`, l.MaxRows)
	return err
}

// Query selects a page, newest first. Before pages back by id.
type Query struct {
	Username string
	Target   string
	// Failed keeps only refused and failed requests.
	Failed bool
	Before int64
	Limit  int
}

func (l *Log) Query(ctx context.Context, q Query) ([]Entry, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	where := []string{"1=1"}
	var args []any
	if q.Username != "" {
		where = append(where, "username = ?")
		args = append(args, q.Username)
	}
	if q.Target != "" {
		where = append(where, "target = ?")
		args = append(args, q.Target)
	}
	if q.Failed {
		where = append(where, "status >= 400")
	}
	if q.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, q.Before)
	}
	args = append(args, q.Limit)
	rows, err := l.db.QueryContext(ctx, `SELECT id, time, username, ip, action, target, status, details FROM audit_log
		WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Entry{}
	for rows.Next() {
		var e Entry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Username, &e.IP, &e.Action, &e.Target, &e.Status, &e.Details); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, at)
		list = append(list, e)
	}
	return list, rows.Err()
}
