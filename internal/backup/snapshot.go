package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Snapshot is one consistent copy of a game database.
type Snapshot struct {
	Path   string `json:"path"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}

// TakeSnapshot copies the game database into target with VACUUM INTO. The
// game keeps its world in WAL mode, so the copy reads one committed state
// while the game goes on writing: it needs no stop and does not block the
// game. The copy is checked before it takes its final name, so a file under
// that name is always a usable world.
func TakeSnapshot(ctx context.Context, source, target string) (Snapshot, error) {
	if _, err := os.Stat(source); err != nil {
		return Snapshot{}, fmt.Errorf("game database: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return Snapshot{}, err
	}
	temp := target + ".partial"
	_ = os.Remove(temp)
	defer os.Remove(temp)

	db, err := open(source, "")
	if err != nil {
		return Snapshot{}, err
	}
	// VACUUM INTO takes the path as an SQL string literal.
	_, err = db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(temp, "'", "''")+"'")
	db.Close()
	if err != nil {
		return Snapshot{}, fmt.Errorf("copy game database: %w", err)
	}
	if err := quickCheck(ctx, temp); err != nil {
		return Snapshot{}, err
	}
	if err := syncFile(temp); err != nil {
		return Snapshot{}, err
	}
	snap, err := digest(temp)
	if err != nil {
		return Snapshot{}, err
	}
	if err := os.Chmod(temp, 0o640); err != nil {
		return Snapshot{}, err
	}
	if err := os.Rename(temp, target); err != nil {
		return Snapshot{}, err
	}
	syncDir(filepath.Dir(target))
	snap.Path = target
	return snap, nil
}

func open(path, mode string) (*sql.DB, error) {
	// A Windows path needs a leading slash to form file:///C:/...
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(10000)")
	if mode != "" {
		query.Set("mode", mode)
	}
	u := url.URL{Scheme: "file", Path: slashed, RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// quickCheck is integrity_check without the index cross-checks: enough to
// catch a copy that did not come out whole, at a fraction of the cost.
func quickCheck(ctx context.Context, path string) error {
	db, err := open(path, "ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("check copy: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("copy failed its check: %s", result)
	}
	return nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// digest hashes the file.
func digest(path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// VerifySnapshot re-hashes a stored copy against its recorded checksum.
func VerifySnapshot(path, sum string) error {
	snap, err := digest(path)
	if err != nil {
		return err
	}
	if sum != "" && snap.SHA256 != sum {
		return fmt.Errorf("%s does not match its checksum", filepath.Base(path))
	}
	return nil
}

// RestoreSnapshot puts a copy back as the game database. The game must be
// stopped. The WAL and shared-memory files of the replaced world go with it:
// left behind, SQLite would replay them into the restored copy.
func RestoreSnapshot(snapshot, database string) error {
	temp := database + ".restoring"
	if err := copyFile(snapshot, temp); err != nil {
		_ = os.Remove(temp)
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(database + suffix); err != nil && !os.IsNotExist(err) {
			_ = os.Remove(temp)
			return err
		}
	}
	if err := os.Rename(temp, database); err != nil {
		_ = os.Remove(temp)
		return err
	}
	syncDir(filepath.Dir(database))
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
