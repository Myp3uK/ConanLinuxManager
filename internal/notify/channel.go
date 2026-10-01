// Package notify sends lifecycle events to Discord, Telegram, or any HTTP
// endpoint.
//
// A webhook URL, a bot token and an endpoint's address and headers are
// credentials: anyone holding them can post to the channel or the endpoint.
// They are stored encrypted with the manager's secret key and never returned
// by the API, only a masked form of them.
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
	"sort"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/security"
)

// Channel kinds.
const (
	KindDiscord  = "discord"
	KindTelegram = "telegram"
	KindHTTP     = "http"
)

type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Target is where the channel sends, masked: the secret parts are left
	// out. WebhookMask is the same for Discord, kept for older clients.
	Target      string `json:"target"`
	WebhookMask string `json:"webhook,omitempty"`
	// ServerIDs are the servers the channel watches. Empty means every
	// server, including the ones added later.
	ServerIDs   []string `json:"server_ids"`
	MinSeverity string   `json:"min_severity"`
	EventTypes  []string `json:"event_types"`
	Language    string   `json:"language"`
	Mention     string   `json:"mention,omitempty"`
	Enabled     bool     `json:"enabled"`
	Settings    Settings `json:"settings"`
	// Headers names the stored headers of an HTTP channel; their values stay
	// secret.
	Headers    []string `json:"headers,omitempty"`
	LastSentAt string   `json:"last_sent_at,omitempty"`
	LastError  string   `json:"last_error,omitempty"`
	secret     secret
}

// Settings are a channel's options that are not secret.
type Settings struct {
	// HTTP: the method, the body template and its content type.
	Method      string `json:"method,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body,omitempty"`
	// AllowPrivate lets an HTTP channel reach local and private addresses.
	AllowPrivate bool `json:"allow_private,omitempty"`
	// Telegram: the chat, and the topic in a forum group.
	ChatID   string `json:"chat_id,omitempty"`
	ThreadID int64  `json:"thread_id,omitempty"`
	// Proxy sends a Telegram or HTTP channel through the notification proxy.
	// Discord always uses it when one is set.
	Proxy bool `json:"proxy,omitempty"`
}

// secret is what is stored encrypted. Discord keeps its original form, the
// bare webhook URL; the other kinds store this as JSON.
type secret struct {
	URL     string   `json:"url,omitempty"`
	Token   string   `json:"token,omitempty"`
	Headers []header `json:"headers,omitempty"`
}

type header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Input is what an operator submits. The secret fields are only read, never
// echoed; left out, they keep their stored value.
type Input struct {
	Name *string `json:"name"`
	// Kind is set when the channel is created and cannot change.
	Kind       *string `json:"kind"`
	WebhookURL *string `json:"webhook_url"`
	// URL is an HTTP channel's address; Token a Telegram bot's token.
	URL   *string `json:"url"`
	Token *string `json:"token"`
	// Headers are an HTTP channel's headers, one "Name: value" per line; an
	// empty string removes them.
	Headers  *string   `json:"headers"`
	Settings *Settings `json:"settings"`
	// ServerIDs limits the channel to those servers; an empty list is every
	// server. ServerID is the older form of the same thing, one server or
	// none, and is read when ServerIDs is absent.
	ServerIDs   *[]string `json:"server_ids"`
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
	botToken   = regexp.MustCompile(`^\d{5,15}:[A-Za-z0-9_-]{30,64}$`)
	chatID     = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{4,63})$`)
	headerName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	methods    = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	// contentTypes are the bodies an HTTP channel can send; the template is
	// filled in each one's own escaping.
	contentTypes = map[string]bool{"application/json": true, "application/x-www-form-urlencoded": true, "text/plain": true}
	// reservedHeaders are set by the manager or by the transport.
	reservedHeaders = map[string]bool{"host": true, "content-length": true, "content-type": true, "transfer-encoding": true, "connection": true}
)

// maxChannelServers bounds the set a channel may watch; a manager with more
// servers than this on one host has other problems.
const maxChannelServers = 200

// cleanServerIDs drops blanks and repeats and puts the ids in a settled order,
// so "every server" is one shape - the empty list - however it was asked for.
func cleanServerIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// validWebhook accepts only a Discord webhook. A Discord channel sends to
// Discord and nowhere else; any other address is an HTTP channel, with its
// own safeguards.
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

// maskEndpoint shows where an HTTP channel sends without its path and query,
// which often carry a key.
func maskEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// maskToken keeps the bot's id, the part before the colon.
func maskToken(token string) string {
	id, _, _ := strings.Cut(token, ":")
	return "бот " + id + ":…"
}

// parseHeaders reads "Name: value" lines.
func parseHeaders(text string) ([]header, error) {
	var list []header
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !headerName.MatchString(name) {
			return nil, fmt.Errorf("a header must be written as Name: value, not %q", strings.TrimSpace(line))
		}
		if reservedHeaders[strings.ToLower(name)] {
			return nil, fmt.Errorf("header %s is set by the manager", name)
		}
		if strings.ContainsAny(value, "\r\n\x00") || len(value) > 4096 {
			return nil, fmt.Errorf("header %s has an invalid value", name)
		}
		list = append(list, header{Name: name, Value: value})
	}
	if len(list) > 20 {
		return nil, fmt.Errorf("at most 20 headers")
	}
	return list, nil
}

// usable says whether a channel has what it needs to send.
func (c Channel) usable() bool {
	switch c.Kind {
	case KindTelegram:
		return c.secret.Token != ""
	default:
		return c.secret.URL != ""
	}
}

func (c *Channel) describe() {
	c.Target, c.WebhookMask, c.Headers = "", "", nil
	switch c.Kind {
	case KindDiscord:
		if c.secret.URL != "" {
			c.Target = mask(c.secret.URL)
			c.WebhookMask = c.Target
		}
	case KindTelegram:
		if c.secret.Token != "" {
			c.Target = maskToken(c.secret.Token) + " → " + c.Settings.ChatID
		}
	case KindHTTP:
		if c.secret.URL != "" {
			c.Target = c.Settings.Method + " " + maskEndpoint(c.secret.URL)
		}
		for _, h := range c.secret.Headers {
			c.Headers = append(c.Headers, h.Name)
		}
	}
}

func (c *Channel) apply(in Input) error {
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.Settings != nil {
		c.Settings = *in.Settings
	}
	switch c.Kind {
	case KindDiscord:
		if in.WebhookURL != nil {
			if err := validWebhook(strings.TrimSpace(*in.WebhookURL)); err != nil {
				return err
			}
			c.secret.URL = strings.TrimSpace(*in.WebhookURL)
		}
		// Of the settings Discord takes only its own JSON template.
		c.Settings = Settings{Body: c.Settings.Body}
		if err := validJSONTemplate(c.Settings.Body); err != nil {
			return err
		}
	case KindTelegram:
		if in.Token != nil {
			token := strings.TrimSpace(*in.Token)
			if !botToken.MatchString(token) {
				return fmt.Errorf("token must be a Telegram bot token, as @BotFather gives it (123456789:AA...)")
			}
			c.secret.Token = token
		}
		c.Settings.ChatID = strings.TrimSpace(c.Settings.ChatID)
		c.Settings.Method, c.Settings.ContentType, c.Settings.Body, c.Settings.AllowPrivate = "", "", "", false
		c.Mention = ""
		if !chatID.MatchString(c.Settings.ChatID) {
			return fmt.Errorf("chat_id must be a chat id such as -1001234567890, or @channelname")
		}
		if c.Settings.ThreadID < 0 {
			return fmt.Errorf("thread_id must be a topic id or 0")
		}
	case KindHTTP:
		if in.URL != nil {
			c.secret.URL = strings.TrimSpace(*in.URL)
		}
		if in.Headers != nil {
			list, err := parseHeaders(*in.Headers)
			if err != nil {
				return err
			}
			c.secret.Headers = list
		}
		c.Settings.ChatID, c.Settings.ThreadID = "", 0
		c.Mention = ""
		if err := c.validateHTTP(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("kind must be discord, telegram or http")
	}
	// server_ids is the current form; server_id is the older one, and naming
	// one server there still means a channel that watches only it.
	if in.ServerIDs != nil {
		c.ServerIDs = cleanServerIDs(*in.ServerIDs)
	} else if in.ServerID != nil {
		c.ServerIDs = cleanServerIDs([]string{*in.ServerID})
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
	if in.Mention != nil && c.Kind == KindDiscord {
		c.Mention = strings.TrimSpace(*in.Mention)
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	switch {
	case c.Name == "" || len(c.Name) > 100:
		return fmt.Errorf("name is required and at most 100 characters")
	case c.Kind == KindDiscord && c.secret.URL == "":
		return fmt.Errorf("webhook_url is required")
	case c.Kind == KindTelegram && c.secret.Token == "":
		return fmt.Errorf("token is required")
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
	c := Channel{ID: newID(), Kind: KindDiscord, MinSeverity: string(events.Info), Language: "ru", Enabled: true, EventTypes: []string{}}
	if in.Kind != nil && *in.Kind != "" {
		c.Kind = *in.Kind
	}
	if c.Kind == KindHTTP {
		c.Settings = Settings{Method: "POST", ContentType: "application/json", Body: DefaultBody}
	}
	if c.Kind == KindTelegram {
		c.Settings.Proxy = true
	}
	if err := c.apply(in); err != nil {
		return Channel{}, err
	}
	c.describe()
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
	if in.Kind != nil && *in.Kind != "" && *in.Kind != c.Kind {
		return Channel{}, fmt.Errorf("the kind of a channel cannot change; add a new channel")
	}
	if err := c.apply(in); err != nil {
		return Channel{}, err
	}
	c.describe()
	return c, r.save(ctx, c, false)
}

func (r *Repository) save(ctx context.Context, c Channel, create bool) error {
	plain := c.secret.URL
	if c.Kind != KindDiscord {
		data, err := json.Marshal(c.secret)
		if err != nil {
			return err
		}
		plain = string(data)
	}
	sealed, err := r.box.Seal(plain)
	if err != nil {
		return err
	}
	types, err := json.Marshal(c.EventTypes)
	if err != nil {
		return err
	}
	settings, err := json.Marshal(c.Settings)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// The channel and the servers it watches are written together: a half
	// saved channel would send to the wrong set until the next edit.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if create {
		_, err = tx.ExecContext(ctx, `INSERT INTO notification_channels
            (id,name,kind,secret,settings,min_severity,event_types,language,mention,enabled,created_at,updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Kind, sealed, string(settings), c.MinSeverity, string(types), c.Language, c.Mention, c.Enabled, now, now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE notification_channels SET name=?,secret=?,settings=?,min_severity=?,
            event_types=?,language=?,mention=?,enabled=?,updated_at=? WHERE id=?`,
			c.Name, sealed, string(settings), c.MinSeverity, string(types), c.Language, c.Mention, c.Enabled, now, c.ID)
	}
	if err != nil {
		return fmt.Errorf("save notification channel: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notification_channel_servers WHERE channel_id=?`, c.ID); err != nil {
		return fmt.Errorf("save notification channel servers: %w", err)
	}
	for _, id := range c.ServerIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO notification_channel_servers (channel_id,server_id) VALUES (?,?)`, c.ID, id); err != nil {
			return fmt.Errorf("channel %q cannot watch server %q: %w", c.Name, id, err)
		}
	}
	return tx.Commit()
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

// List returns channels with their secrets masked. The unsealed values stay
// inside the package.
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
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,kind,secret,settings,min_severity,event_types,language,mention,enabled,
        COALESCE(last_sent_at,''),COALESCE(last_error,''),
        COALESCE((SELECT group_concat(server_id, char(10)) FROM notification_channel_servers WHERE channel_id=notification_channels.id),'')
        FROM notification_channels `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Channel, 0)
	for rows.Next() {
		var c Channel
		var sealed, types, settings string
		var servers string
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &sealed, &settings, &c.MinSeverity, &types, &c.Language, &c.Mention, &c.Enabled, &c.LastSentAt, &c.LastError, &servers); err != nil {
			return nil, err
		}
		c.ServerIDs = []string{}
		if servers != "" {
			c.ServerIDs = strings.Split(servers, "\n")
			sort.Strings(c.ServerIDs)
		}
		_ = json.Unmarshal([]byte(types), &c.EventTypes)
		if c.EventTypes == nil {
			c.EventTypes = []string{}
		}
		_ = json.Unmarshal([]byte(settings), &c.Settings)
		if r.box != nil {
			plain, err := r.box.Open(sealed)
			switch {
			case err != nil:
				c.LastError = "cannot decrypt the channel's secret: was CONAN_MANAGER_SECRET_KEY changed?"
			case c.Kind == KindDiscord:
				c.secret.URL = plain
			default:
				_ = json.Unmarshal([]byte(plain), &c.secret)
			}
		}
		c.describe()
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
