package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Mod is one Steam Workshop item a server loads. The order matters: when two
// mods change the same asset, the one further down modlist.txt wins, so
// Position is part of the configuration rather than a display detail.
type Mod struct {
	ServerID string `json:"server_id"`
	// WorkshopID is the item's published file id on Steam.
	WorkshopID string `json:"workshop_id"`
	Position   int    `json:"position"`
	Enabled    bool   `json:"enabled"`
	// Title is what Steam calls the item; empty until it was looked up.
	Title string `json:"title"`
	// Paks are the .pak file names the item installed, in load order. Most
	// mods ship a single one, some several.
	Paks []string `json:"paks"`
	Size int64    `json:"size_bytes"`
	// InstalledUpdated is the Steam update time of the copy on disk and
	// LatestUpdated the newest time Steam reported, so an older installed time
	// is exactly what "an update is waiting" means.
	InstalledUpdated time.Time `json:"installed_updated,omitempty"`
	LatestUpdated    time.Time `json:"latest_updated,omitempty"`
	InstalledAt      time.Time `json:"installed_at,omitempty"`
	CheckedAt        time.Time `json:"checked_at,omitempty"`
	// LastError is why the last download of this item failed. It is kept so one
	// broken mod is visible in the list instead of only in a job log.
	LastError string    `json:"last_error,omitempty"`
	AddedAt   time.Time `json:"added_at"`
}

// NeedsInstall reports whether the item has no files on disk yet.
func (m Mod) NeedsInstall() bool { return len(m.Paks) == 0 }

// UpdateAvailable reports whether Steam has a newer copy than the installed
// one. An item that was never installed is not "out of date"; it is missing,
// which NeedsInstall answers.
func (m Mod) UpdateAvailable() bool {
	return !m.NeedsInstall() && m.LatestUpdated.After(m.InstalledUpdated)
}

// Pending reports whether a download would do anything for this item.
func (m Mod) Pending() bool { return m.NeedsInstall() || m.UpdateAvailable() }

// maxModsPerServer bounds a list that is written into modlist.txt and walked on
// every start. Conan itself slows to a crawl long before this.
const maxModsPerServer = 200

var workshopIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// ValidWorkshopID accepts a published file id: digits, no leading zero.
func ValidWorkshopID(id string) bool { return workshopIDPattern.MatchString(id) }

// ParseWorkshopID takes what an operator can copy out of Steam — the bare id,
// a browser URL or a steam:// link — and returns the id. Asking for the number
// alone would mean explaining where to find it.
func ParseWorkshopID(input string) (string, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return "", ErrInvalidConfig("workshop id is required")
	}
	if ValidWorkshopID(text) {
		return text, nil
	}
	if parsed, err := url.Parse(text); err == nil {
		if id := parsed.Query().Get("id"); ValidWorkshopID(id) {
			return id, nil
		}
		// steam://url/CommunityFilePage/<id> and .../filedetails/<id> keep it
		// in the path instead. The marker is required: the last segment of
		// https://steamcommunity.com/app/440900 is digits too, and that is the
		// game, not a mod.
		if isWorkshopPage(parsed.Path) {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if last := parts[len(parts)-1]; ValidWorkshopID(last) {
				return last, nil
			}
		}
	}
	return "", ErrInvalidConfig(fmt.Sprintf("%q is not a Workshop id or link", input))
}

// isWorkshopPage reports whether a URL path is one of the pages that end in a
// published file id.
func isWorkshopPage(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range []string{"filedetails", "sharedfiles", "communityfilepage"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// Mods returns a server's mods in load order.
func (r *Repository) Mods(ctx context.Context, serverID string) ([]Mod, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT server_id,workshop_id,position,enabled,title,paks,size_bytes,
        installed_updated,latest_updated,installed_at,checked_at,last_error,added_at
        FROM server_mods WHERE server_id=? ORDER BY position, workshop_id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list mods: %w", err)
	}
	defer rows.Close()
	result := make([]Mod, 0)
	for rows.Next() {
		var m Mod
		var enabled int
		var paks string
		var installedUpdated, latestUpdated, installedAt, checkedAt, addedAt any
		if err := rows.Scan(&m.ServerID, &m.WorkshopID, &m.Position, &enabled, &m.Title, &paks, &m.Size,
			&installedUpdated, &latestUpdated, &installedAt, &checkedAt, &m.LastError, &addedAt); err != nil {
			return nil, err
		}
		m.Enabled = enabled != 0
		m.Paks = decodePaks(paks)
		m.InstalledUpdated, m.LatestUpdated = parseDatabaseTime(installedUpdated), parseDatabaseTime(latestUpdated)
		m.InstalledAt, m.CheckedAt = parseDatabaseTime(installedAt), parseDatabaseTime(checkedAt)
		m.AddedAt = parseDatabaseTime(addedAt)
		result = append(result, m)
	}
	return result, rows.Err()
}

// SetMods replaces the list an operator maintains — which items, in which
// order, enabled or not — while keeping what the manager learned about the
// items that stay. Re-adding a mod must not mean downloading it again.
func (r *Repository) SetMods(ctx context.Context, serverID string, mods []Mod) error {
	if len(mods) > maxModsPerServer {
		return ErrInvalidConfig(fmt.Sprintf("at most %d mods per server", maxModsPerServer))
	}
	seen := make(map[string]bool, len(mods))
	for _, m := range mods {
		if !ValidWorkshopID(m.WorkshopID) {
			return ErrInvalidConfig(fmt.Sprintf("invalid Workshop id %q", m.WorkshopID))
		}
		if seen[m.WorkshopID] {
			return ErrInvalidConfig(fmt.Sprintf("mod %s is listed twice", m.WorkshopID))
		}
		seen[m.WorkshopID] = true
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM servers WHERE id=?`, serverID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for position, m := range mods {
		enabled := 0
		if m.Enabled {
			enabled = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO server_mods
            (server_id,workshop_id,position,enabled,title,paks,size_bytes,added_at)
            VALUES (?,?,?,?,?,'[]',0,?)
            ON CONFLICT(server_id,workshop_id) DO UPDATE SET position=excluded.position,
                enabled=excluded.enabled, title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE server_mods.title END`,
			serverID, m.WorkshopID, position, enabled, m.Title, now); err != nil {
			return fmt.Errorf("save mod %s: %w", m.WorkshopID, err)
		}
	}
	// Whatever the operator left out is gone; the files are removed by the
	// caller, which owns the install directory.
	ids := make([]any, 0, len(mods)+1)
	ids = append(ids, serverID)
	placeholders := make([]string, 0, len(mods))
	for _, m := range mods {
		ids = append(ids, m.WorkshopID)
		placeholders = append(placeholders, "?")
	}
	query := `DELETE FROM server_mods WHERE server_id=?`
	if len(placeholders) > 0 {
		query += ` AND workshop_id NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	if _, err := tx.ExecContext(ctx, query, ids...); err != nil {
		return fmt.Errorf("remove mods: %w", err)
	}
	return tx.Commit()
}

// MarkModInstalled records the copy that now sits in the install directory.
func (r *Repository) MarkModInstalled(ctx context.Context, serverID, workshopID string, paks []string, size int64, updated time.Time) error {
	encoded, err := encodePaks(paks)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = r.db.ExecContext(ctx, `UPDATE server_mods SET paks=?,size_bytes=?,installed_updated=?,installed_at=?,last_error=''
        WHERE server_id=? AND workshop_id=?`, encoded, size, databaseTime(updated), now, serverID, workshopID)
	if err != nil {
		return fmt.Errorf("record mod %s: %w", workshopID, err)
	}
	return nil
}

// MarkModChecked records what Steam says about an item, including a failure to
// ask. Keeping the previous title and timestamps on failure leaves the list
// readable when Steam is unreachable.
func (r *Repository) MarkModChecked(ctx context.Context, serverID, workshopID, title string, latest time.Time, failure string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := r.db.ExecContext(ctx, `UPDATE server_mods SET
        title=CASE WHEN ?<>'' THEN ? ELSE title END,
        latest_updated=CASE WHEN ?<>'' THEN ? ELSE latest_updated END,
        checked_at=?, last_error=? WHERE server_id=? AND workshop_id=?`,
		title, title, databaseTime(latest), databaseTime(latest), now, failure, serverID, workshopID)
	if err != nil {
		return fmt.Errorf("record mod check %s: %w", workshopID, err)
	}
	return nil
}

// MarkModFailed records why a download did not finish, leaving the installed
// copy as it was.
func (r *Repository) MarkModFailed(ctx context.Context, serverID, workshopID, failure string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE server_mods SET last_error=? WHERE server_id=? AND workshop_id=?`,
		failure, serverID, workshopID)
	return err
}

// databaseTime renders a time for storage, and a zero time as NULL so that
// "never" is distinguishable from the Unix epoch.
func databaseTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func encodePaks(paks []string) (string, error) {
	if len(paks) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(paks)
	return string(data), err
}

func decodePaks(raw string) []string {
	var paks []string
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &paks)
	}
	return paks
}
