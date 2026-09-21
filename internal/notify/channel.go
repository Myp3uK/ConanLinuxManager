// Package notify sends lifecycle events to Discord.
//
// A webhook URL is a credential: anyone holding it can post to the channel. It
// is stored encrypted with the manager's secret key and never returned by the
// API, only a masked form of it.
package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/security"
)

type Channel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	WebhookMask string   `json:"webhook"`
	ServerID    string   `json:"server_id,omitempty"`
	MinSeverity string   `json:"min_severity"`
	EventTypes  []string `json:"event_types"`
	Language    string   `json:"language"`
	Mention     string   `json:"mention,omitempty"`
	Enabled     bool     `json:"enabled"`
	LastSentAt  string   `json:"last_sent_at,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
	webhook     string
}

// Input is what an operator submits. WebhookURL is only read, never echoed.
type Input struct {
	Name        *string   `json:"name"`
	WebhookURL  *string   `json:"webhook_url"`
	ServerID    *string   `json:"server_id"`
	MinSeverity *string   `json:"min_severity"`
	EventTypes  *[]string `json:"event_types"`
	Language    *string   `json:"language"`
	Mention     *string   `json:"mention"`
	Enabled     *bool     `json:"enabled"`
}

var (
	webhookPath = regexp.MustCompile(`^/api/webhooks/\d{5,30}/[A-Za-z0-9_-]{20,200}$`)
	mentionForm = regexp.MustCompile(`^(<@&?\d{5,30}>|@here|@everyone)?$`)
	webhookHost = map[string]bool{
		"discord.com": true, "discordapp.com": true,
		"ptb.discord.com": true, "canary.discord.com": true,
	}
)

// validWebhook accepts only a Discord webhook. Accepting any URL would let
// whoever reaches the API make the manager send requests to internal hosts.
func validWebhook(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !webhookHost[strings.ToLower(u.Hostname())] || u.Port() != "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || !webhookPath.MatchString(u.Path) {
		return fmt.Errorf("webhook_url must be a Discord webhook URL (https://discord.com/api/webhooks/ID/TOKEN)")
	}
	return nil
}

// mask keeps the webhook id so an operator can tell channels apart, and drops
// the token, which is the secret part.
func mask(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid)"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 3 {
		return "https://" + u.Host + "/api/webhooks/" + parts[2] + "/…"
	}
	return "https://" + u.Host + "/…"
}

func (c *Channel) apply(in Input) error {
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.WebhookURL != nil {
		if err := validWebhook(strings.TrimSpace(*in.WebhookURL)); err != nil {
			return err
		}
		c.webhook = strings.TrimSpace(*in.WebhookURL)
		c.WebhookMask = mask(c.webhook)
	}
	if in.ServerID != nil {
		c.ServerID = *in.ServerID
	}
	if in.MinSeverity != nil {
		c.MinSeverity = *in.MinSeverity
	}
	if in.EventTypes != nil {
		c.EventTypes = *in.EventTypes
	}
	if in.Language != nil {
		c.Language = *in.Language
	}
	if in.Mention != nil {
		c.Mention = strings.TrimSpace(*in.Mention)
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	switch {
	case c.Name == "" || len(c.Name) > 100:
		return fmt.Errorf("name is required and at most 100 characters")
	case c.webhook == "":
		return fmt.Errorf("webhook_url is required")
	case c.MinSeverity != string(events.Info) && c.MinSeverity != string(events.Warning) && c.MinSeverity != string(events.Error):
		return fmt.Errorf("min_severity must be info, warning or error")
	case c.Language != "ru" && c.Language != "en":
		return fmt.Errorf("language must be ru or en")
	case !mentionForm.MatchString(c.Mention):
		return fmt.Errorf("mention must be <@USER_ID>, <@&ROLE_ID>, @here or @everyone")
	case len(c.EventTypes) > 64:
		return fmt.Errorf("too many event types")
	}
	for _, t := range c.EventTypes {
		if !strings.Contains(t, ".") || len(t) > 64 {
			return fmt.Errorf("invalid event type %q", t)
		}
	}
	return nil
}

type Repository struct {
	db  *sql.DB
	box *security.SecretBox
}

// NewRepository needs the secret box; without a secret key there is no safe
// place for webhook URLs, and notifications stay unavailable.
func NewRepository(db *sql.DB, box *security.SecretBox) *Repository {
	return &Repository{db: db, box: box}
}

var errNoKey = fmt.Errorf("notifications need CONAN_MANAGER_SECRET_KEY to store webhook URLs encrypted")

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Repository) Create(ctx context.Context, in Input) (Channel, error) {
	if r.box == nil {
		return Channel{}, errNoKey
	}
	c := Channel{ID: newID(), Kind: "discord", MinSeverity: string(events.Info), Language: "ru", Enabled: true, EventTypes: []string{}}
	if err := c.apply(in); err != nil {
		return Channel{}, err
	}
	return c, r.save(ctx, c, true)
}

func (r *Repository) Update(ctx context.Context, id string, in Input) (Channel, error) {
	if r.box == nil {
		return Channel{}, errNoKey
	}
	c, err := r.get(ctx, id)
	if err != nil {
		return Channel{}, err
	}
	if err := c.apply(in); err != nil {
		return Channel{}, err
	}
	return c, r.save(ctx, c, false)
}

func (r *Repository) save(ctx context.Context, c Channel, create bool) error {
	sealed, err := r.box.Seal(c.webhook)
	if err != nil {
		return err
	}
	types, err := json.Marshal(c.EventTypes)
	if err != nil {
		return err
	}
	var server any
	if c.ServerID != "" {
		server = c.ServerID
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if create {
		_, err = r.db.ExecContext(ctx, `INSERT INTO notification_channels
            (id,name,kind,secret,server_id,min_severity,event_types,language,mention,enabled,created_at,updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Kind, sealed, server, c.MinSeverity, string(types), c.Language, c.Mention, c.Enabled, now, now)
	} else {
		_, err = r.db.ExecContext(ctx, `UPDATE notification_channels SET name=?,secret=?,server_id=?,min_severity=?,
            event_types=?,language=?,mention=?,enabled=?,updated_at=? WHERE id=?`,
			c.Name, sealed, server, c.MinSeverity, string(types), c.Language, c.Mention, c.Enabled, now, c.ID)
	}
	if err != nil {
		return fmt.Errorf("save notification channel: %w", err)
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("notification channel %q not found", id)
	}
	return nil
}

// List returns channels with masked webhooks. The unsealed URL stays inside
// the package.
func (r *Repository) List(ctx context.Context) ([]Channel, error) {
	return r.query(ctx, `ORDER BY name`)
}

func (r *Repository) get(ctx context.Context, id string) (Channel, error) {
	items, err := r.query(ctx, `WHERE id=?`, id)
	if err != nil {
		return Channel{}, err
	}
	if len(items) == 0 {
		return Channel{}, fmt.Errorf("notification channel %q not found", id)
	}
	return items[0], nil
}

func (r *Repository) query(ctx context.Context, where string, args ...any) ([]Channel, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,kind,secret,COALESCE(server_id,''),min_severity,event_types,language,mention,enabled,
        COALESCE(last_sent_at,''),COALESCE(last_error,'') FROM notification_channels `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Channel, 0)
	for rows.Next() {
		var c Channel
		var sealed, types string
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &sealed, &c.ServerID, &c.MinSeverity, &types, &c.Language, &c.Mention, &c.Enabled, &c.LastSentAt, &c.LastError); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(types), &c.EventTypes)
		if c.EventTypes == nil {
			c.EventTypes = []string{}
		}
		if r.box != nil {
			if plain, err := r.box.Open(sealed); err == nil {
				c.webhook = plain
				c.WebhookMask = mask(plain)
			} else {
				c.LastError = "cannot decrypt webhook: was CONAN_MANAGER_SECRET_KEY changed?"
			}
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (r *Repository) markResult(ctx context.Context, id string, sendErr error) {
	if sendErr == nil {
		_, _ = r.db.ExecContext(ctx, `UPDATE notification_channels SET last_sent_at=?, last_error=NULL WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
		return
	}
	_, _ = r.db.ExecContext(ctx, `UPDATE notification_channels SET last_error=? WHERE id=?`, sendErr.Error(), id)
}
