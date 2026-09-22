package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/example/conan-manager/internal/events"
)

// Dispatcher delivers events to every channel that wants them. It runs as a
// bus subscriber, so a slow or failing service never delays a lifecycle action.
type Dispatcher struct {
	Repo   *Repository
	Logger *slog.Logger

	mu     sync.RWMutex
	sender sender
}

func NewDispatcher(repo *Repository, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{Repo: repo, Logger: logger, sender: sender{
		client:   &http.Client{Timeout: 10 * time.Second},
		direct:   &http.Client{Timeout: 10 * time.Second, CheckRedirect: noRedirects},
		guarded:  guardedClient(10 * time.Second),
		attempts: 3,
		sleep:    sleepCtx,
	}}
}

// UseProxy sends notifications through a proxy: http://, https:// or
// socks5://, with credentials in the URL if the proxy needs them. Where the
// host cannot reach Discord or Telegram directly, this is the only way out.
// Discord always goes through it; Telegram and HTTP channels when they ask.
// An empty value goes back to sending directly. It may be called while
// events flow.
func (d *Dispatcher) UseProxy(raw string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if raw == "" {
		d.sender.client = &http.Client{Timeout: d.sender.client.Timeout}
		d.sender.proxied = false
		return nil
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Host == "" {
		return fmt.Errorf("notification proxy must be a URL such as socks5://host:1080")
	}
	switch proxy.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("notification proxy scheme must be http, https or socks5, not %q", proxy.Scheme)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxy)
	d.sender.client = &http.Client{Timeout: d.sender.client.Timeout, Transport: transport, CheckRedirect: noRedirects}
	d.sender.proxied = true
	return nil
}

// Get returns one channel, with its webhook masked.
func (r *Repository) Get(ctx context.Context, id string) (Channel, error) {
	return r.get(ctx, id)
}

// Handle is the bus subscriber.
func (d *Dispatcher) Handle(e events.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	channels, err := d.Repo.List(ctx)
	if err != nil {
		d.Logger.Error("load notification channels", "error", err)
		return
	}
	// A server can keep a message kind out of Discord.
	if e.Details["discord"] == "off" {
		return
	}
	for _, c := range channels {
		if !c.wants(e) {
			continue
		}
		err := d.deliver(ctx, c, e)
		d.Repo.markResult(ctx, c.ID, err)
		if err != nil {
			d.Logger.Warn("notification not delivered", "channel", c.Name, "event", e.Type, "error", err)
		}
	}
}

// deliver sends an event to one channel in its service's form.
func (d *Dispatcher) deliver(ctx context.Context, c Channel, e events.Event) error {
	s := d.current()
	switch c.Kind {
	case KindTelegram:
		return s.sendTelegram(ctx, c, summarize(e, c.Language))
	case KindHTTP:
		return s.sendHTTP(ctx, c, summarize(e, c.Language))
	}
	return s.send(ctx, c.secret.URL, format(e, c))
}

// Test sends a sample notice to one channel and reports the outcome directly,
// so an operator knows the channel works before relying on it.
func (d *Dispatcher) Test(ctx context.Context, id string) error {
	c, err := d.Repo.get(ctx, id)
	if err != nil {
		return err
	}
	if !c.usable() {
		return fmt.Errorf("channel cannot send: %s", c.LastError)
	}
	message := "Тестовое уведомление: канал работает"
	if c.Language == "en" {
		message = "Test notification: this channel works"
	}
	e := events.Event{Type: "manager.test", Severity: events.Info, Time: time.Now().UTC(), Message: "🔔 " + message}
	err = d.deliver(ctx, c, e)
	d.Repo.markResult(ctx, c.ID, err)
	return err
}

func (d *Dispatcher) current() sender {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.sender
}
