// Package dbcheck verifies a game database after the game ended without a
// clean shutdown: killed after the stop timeout, or crashed.
//
// SQLite transactions are atomic, so a kill almost never damages the file; the
// usual cost is the progress since the last commit, which no check can see.
// What this does catch is real damage, and a damaged world must not be started
// and then written over by a game that trusts it.
package dbcheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrNoDatabase means there is nothing to check: the game never created one.
var ErrNoDatabase = errors.New("game database does not exist")

// CorruptError carries what SQLite reported, so it can be shown verbatim.
type CorruptError struct{ Report []string }

func (e *CorruptError) Error() string {
	if len(e.Report) == 0 {
		return "integrity check returned no result"
	}
	return fmt.Sprintf("integrity check failed: %s", e.Report[0])
}

// Verify runs PRAGMA integrity_check. The database is opened read-write
// because SQLite must replay a WAL left by the ended process before it can
// check the pages; that replay is exactly what the game would do on its next
// start.
func Verify(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return ErrNoDatabase
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	// A Windows path needs a leading slash to form file:///C:/..., otherwise
	// SQLite cannot open it and reports a misleading error.
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	u := url.URL{Scheme: "file", Path: slashed, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return classify(err)
	}
	defer rows.Close()
	var report []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if len(report) < 20 {
			report = append(report, line)
		}
	}
	if err := rows.Err(); err != nil {
		return classify(err)
	}
	if len(report) == 1 && report[0] == "ok" {
		return nil
	}
	return &CorruptError{Report: report}
}

// classify separates damage from failing to look. Only SQLite saying the file
// is corrupt or not a database is damage; a permission or I/O problem means
// the check did not happen, which callers must not report as a verdict.
func classify(err error) error {
	var e *sqlite.Error
	if errors.As(err, &e) {
		switch e.Code() & 0xff {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return &CorruptError{Report: []string{err.Error()}}
		}
	}
	return fmt.Errorf("could not check database: %w", err)
}
