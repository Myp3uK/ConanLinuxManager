package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/example/conan-manager/internal/events"
)

// Dispatcher delivers events to every channel that wants them. It runs as a
// bus subscriber, so a slow or failing Discord never delays a lifecycle action.
type Dispatcher struct {
	Repo   *Repository
	Logger *slog.Logger
	sender sender
}

func NewDispatcher(repo *Repository, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{Repo: repo, Logger: logger, sender: sender{
		client:   &http.Client{Timeout: 10 * time.Second},
		attempts: 3,
		sleep:    sleepCtx,
	}}
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
	for _, c := range channels {
		if !c.wants(e) {
			continue
		}
		err := d.sender.send(ctx, c.webhook, format(e, c))
		d.Repo.markResult(ctx, c.ID, err)
		if err != nil {
			d.Logger.Warn("notification not delivered", "channel", c.Name, "event", e.Type, "error", err)
		}
	}
}

// Test sends a sample notice to one channel and reports the outcome directly,
// so an operator knows the webhook works before relying on it.
func (d *Dispatcher) Test(ctx context.Context, id string) error {
	c, err := d.Repo.get(ctx, id)
	if err != nil {
		return err
	}
	if c.webhook == "" {
		return fmt.Errorf("channel has no usable webhook: %s", c.LastError)
	}
	message := "Тестовое уведомление: канал работает"
	if c.Language == "en" {
		message = "Test notification: this channel works"
	}
	e := events.Event{Type: "manager.test", Severity: events.Info, Time: time.Now().UTC(), Message: "🔔 " + message}
	err = d.sender.send(ctx, c.webhook, format(e, c))
	d.Repo.markResult(ctx, c.ID, err)
	return err
}
