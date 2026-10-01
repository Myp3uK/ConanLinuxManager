package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/security"
	"github.com/example/conan-manager/internal/store"
)

const token = "sEcReT-ToKeN_abcdefghijklmnopqrstuvwxyz0123456789"
const webhook = "https://discord.com/api/webhooks/123456789012345678/" + token

func str(s string) *string { return &s }

func testRepo(t *testing.T) (*Repository, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	box, err := security.NewSecretBox("test-master-key")
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(db.DB(), box), db
}

func TestOnlyDiscordWebhooksAreAccepted(t *testing.T) {
	good := []string{
		webhook,
		"https://discordapp.com/api/webhooks/123456789012345678/" + token,
		"https://canary.discord.com/api/webhooks/123456789012345678/" + token,
	}
	for _, u := range good {
		if err := validWebhook(u); err != nil {
			t.Errorf("rejected %s: %v", u, err)
		}
	}
	bad := []string{
		"http://discord.com/api/webhooks/123456789012345678/" + token,
		"https://discord.com.evil.example/api/webhooks/123456789012345678/" + token,
		"https://127.0.0.1/api/webhooks/123456789012345678/" + token,
		"https://discord.com:8443/api/webhooks/123456789012345678/" + token,
		"https://user@discord.com/api/webhooks/123456789012345678/" + token,
		"https://discord.com/api/webhooks/123456789012345678/" + token + "?wait=true",
		"https://discord.com/api/v9/channels/1/messages",
		"https://169.254.169.254/latest/meta-data",
	}
	for _, u := range bad {
		if err := validWebhook(u); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
}

// The token must not be readable anywhere outside the sealed column: not in
// the database, not in the API view of a channel.
func TestWebhookTokenIsNeverStoredOrShownInClear(t *testing.T) {
	repo, db := testRepo(t)
	ctx := context.Background()
	created, err := repo.Create(ctx, Input{Name: str("Admins"), WebhookURL: str(webhook)})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.DB().QueryRow(`SELECT secret FROM notification_channels WHERE id=?`, created.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, token) {
		t.Fatal("webhook stored in clear")
	}
	listed, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view, _ := json.Marshal(listed)
	if strings.Contains(string(view), token) {
		t.Fatalf("API view leaks the token: %s", view)
	}
	if !strings.Contains(string(view), "/api/webhooks/123456789012345678/") {
		t.Fatalf("mask lost the webhook id: %s", view)
	}
}

func TestChannelFilters(t *testing.T) {
	c := Channel{Enabled: true, Kind: KindDiscord, secret: secret{URL: webhook}, MinSeverity: "warning", EventTypes: []string{}}
	crash := events.Event{Type: events.ServerCrashed, Severity: events.Error, ServerID: "a"}
	ready := events.Event{Type: events.ServerReady, Severity: events.Info, ServerID: "a"}
	if !c.wants(crash) || c.wants(ready) {
		t.Fatal("severity filter")
	}
	c.MinSeverity = "info"
	if !c.wants(ready) || c.wants(events.Event{Type: events.ServerStarted, Severity: events.Info}) {
		t.Fatal("quiet events must be left out by default")
	}
	c.ServerIDs = []string{"b"}
	if c.wants(crash) {
		t.Fatal("server filter")
	}
	// A channel can watch several servers at once, and an event from any of
	// them is taken.
	c.ServerIDs = []string{"b", "a"}
	if !c.wants(crash) {
		t.Fatal("a channel watching several servers takes events from each")
	}
	c.ServerIDs, c.EventTypes = nil, []string{"server.ready"}
	if c.wants(crash) || !c.wants(ready) {
		t.Fatal("event type filter")
	}
	c.Enabled = false
	if c.wants(ready) {
		t.Fatal("disabled channel")
	}
}

// Text from an event, such as a player name in a log line, must never ping
// anyone; only the configured mention may, and only for errors.
func TestMentionsAreExplicitAndOnlyForErrors(t *testing.T) {
	c := Channel{Language: "ru", Mention: "<@&987654321098765>"}
	info := format(events.Event{Type: events.ServerReady, Severity: events.Info, Message: "@everyone hello"}, c)
	if strings.Contains(info.Content, c.Mention) || len(info.AllowedMentions.Parse) != 0 || len(info.AllowedMentions.Roles) != 0 {
		t.Fatalf("info notice may ping: %+v", info)
	}
	crash := format(events.Event{Type: events.ServerCrashed, Severity: events.Error, ServerName: "PvP",
		Details: map[string]string{"exit": "signal SIGSEGV", "announce": "Сервер PvP упал."}}, c)
	if crash.Content != c.Mention+" Сервер PvP упал." || len(crash.AllowedMentions.Roles) != 1 || crash.AllowedMentions.Roles[0] != "987654321098765" {
		t.Fatalf("error notice mention: %+v", crash)
	}
}

type fakeDiscord struct {
	mu        sync.Mutex
	responses []int
	bodies    []string
}

func (f *fakeDiscord) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	f.bodies = append(f.bodies, string(body))
	status := http.StatusNoContent
	if len(f.responses) > 0 {
		status, f.responses = f.responses[0], f.responses[1:]
	}
	response := &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
	if status == http.StatusTooManyRequests {
		response.Body = io.NopCloser(strings.NewReader(`{"retry_after": 0.25}`))
	}
	return response, nil
}

func testSender(rt http.RoundTripper, waits *[]time.Duration) sender {
	return sender{client: &http.Client{Transport: rt}, attempts: 3, sleep: func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}}
}

func TestRateLimitIsHonouredAndRetried(t *testing.T) {
	fake := &fakeDiscord{responses: []int{http.StatusTooManyRequests, http.StatusNoContent}}
	var waits []time.Duration
	s := testSender(fake, &waits)
	if err := s.send(context.Background(), webhook, payload{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(fake.bodies) != 2 || len(waits) != 1 || waits[0] != 250*time.Millisecond {
		t.Fatalf("attempts=%d waits=%v", len(fake.bodies), waits)
	}
}

func TestADeletedWebhookIsNotRetried(t *testing.T) {
	fake := &fakeDiscord{responses: []int{http.StatusNotFound}}
	var waits []time.Duration
	if err := testSender(fake, &waits).send(context.Background(), webhook, payload{}); err == nil {
		t.Fatal("expected an error")
	}
	if len(fake.bodies) != 1 {
		t.Fatalf("retried a 404 %d times", len(fake.bodies))
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: lookup failed for " + req.URL.String())
}

// net/http puts the full URL in its errors, and with it the token.
func TestTransportErrorsDoNotLeakTheToken(t *testing.T) {
	var waits []time.Duration
	err := testSender(failingTransport{}, &waits).send(context.Background(), webhook, payload{})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks token or is missing: %v", err)
	}
}

func TestDispatcherDeliversToMatchingChannelsAndRecordsResult(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	all, err := repo.Create(ctx, Input{Name: str("All"), WebhookURL: str(webhook)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, Input{Name: str("Errors"), WebhookURL: str(webhook), MinSeverity: str("error")}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDiscord{}
	d := NewDispatcher(repo, slogDiscard())
	d.sender.client = &http.Client{Transport: fake}
	d.Handle(events.Event{Type: events.ServerReady, Severity: events.Info, ServerID: "a", ServerName: "PvE", Time: time.Now(), Details: map[string]string{"announce": "PvE запущен"}})
	if len(fake.bodies) != 1 || !strings.Contains(fake.bodies[0], "PvE") {
		t.Fatalf("deliveries: %v", fake.bodies)
	}
	channels, _ := repo.List(ctx)
	for _, c := range channels {
		if c.ID == all.ID && c.LastSentAt == "" {
			t.Fatal("delivery not recorded")
		}
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestProxyMustBeAUsableURL(t *testing.T) {
	d := NewDispatcher(nil, nil)
	for _, good := range []string{"", "socks5://user:pass@127.0.0.1:1080", "http://proxy.local:3128", "https://proxy.local:443"} {
		if err := d.UseProxy(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	for _, bad := range []string{"127.0.0.1:1080", "ftp://proxy:21", "socks5://"} {
		if err := d.UseProxy(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A server's own text is the whole message: no embed, no fields.
func TestServerTextIsTheMessage(t *testing.T) {
	c := Channel{Language: "ru"}
	body, _ := discordBody(events.Event{Type: events.ServerReady, Severity: events.Info, ServerName: "PvE",
		Details: map[string]string{"announce": "Сервер готов, заходите!", "kind": "started"}}, c)
	var sent map[string]any
	_ = json.Unmarshal(body, &sent)
	if sent["content"] != "Сервер готов, заходите!" || sent["embeds"] != nil {
		t.Fatalf("%s", body)
	}
	// Without a text of its own, the event's title says what happened.
	killed := format(events.Event{Type: events.ServerKilled, Severity: events.Warning, Message: "Killed by the operator"}, c)
	if killed.Content != "💀 Сервер принудительно убит\nKilled by the operator" {
		t.Fatalf("%q", killed.Content)
	}
}

// A Discord channel with its own JSON posts that, filled in.
func TestDiscordJSONTemplate(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, Input{Name: str("x"), WebhookURL: str(webhook), Settings: &Settings{Body: `{"content": {text}}`}}); err == nil {
		t.Fatal("accepted a template that is not JSON")
	}
	c, err := repo.Create(ctx, Input{Name: str("x"), WebhookURL: str(webhook), Mention: str("@here"),
		Settings: &Settings{Body: `{"content": "{mention} **{server}**: {text}", "embeds": [{"description": "{log}"}]}`, ChatID: "ignored"}})
	if err != nil || c.Settings.ChatID != "" {
		t.Fatalf("%+v %v", c.Settings, err)
	}
	c, _ = repo.get(ctx, c.ID)
	body, _ := discordBody(events.Event{Type: events.ServerCrashed, Severity: events.Error, ServerName: "PvE \"1\"",
		Details: map[string]string{"announce": "Упал", "log": "line1\nline2"}}, c)
	var sent struct {
		Content string `json:"content"`
		Embeds  []struct {
			Description string `json:"description"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(body, &sent); err != nil || sent.Content != "@here **PvE \"1\"**: Упал" || sent.Embeds[0].Description != "line1\nline2" {
		t.Fatalf("%s %v", body, err)
	}
}

// Every category names events the formatter has a title for.
func TestCategoriesUseTitledEvents(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Categories() {
		if c.Title == "" || len(c.Types) == 0 || (c.Audience != "players" && c.Audience != "admins") {
			t.Errorf("%s: incomplete category %+v", c.Key, c)
		}
		for _, typ := range c.Types {
			if _, ok := titles[events.Type(typ)]; !ok {
				t.Errorf("%s: %s has no title", c.Key, typ)
			}
			if seen[typ] {
				t.Errorf("%s: %s is in two categories", c.Key, typ)
			}
			seen[typ] = true
		}
	}
}
