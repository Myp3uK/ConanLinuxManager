package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/events"
)

type text struct{ ru, en string }

// titles give each event a short, recognisable headline. Unknown types fall
// back to the event's own message, so a new event is never silently dropped.
var titles = map[events.Type]text{
	events.ServerStarted:          {"▶️ Процесс сервера запущен", "▶️ Server process started"},
	events.ServerReady:            {"🟢 Сервер запущен и принимает игроков", "🟢 Server is up and accepting players"},
	events.ServerStartFailed:      {"❌ Не удалось запустить сервер", "❌ Server failed to start"},
	events.ServerStopping:         {"🟡 Сервер останавливается и сохраняет мир", "🟡 Server is stopping and saving the world"},
	events.ServerStopped:          {"⚫ Сервер остановлен", "⚫ Server stopped"},
	events.ServerKilled:           {"💀 Сервер принудительно убит", "💀 Server was killed"},
	events.ServerCrashed:          {"🔴 Сервер упал", "🔴 Server crashed"},
	events.ServerRestarting:       {"🔧 Технический рестарт после сбоя", "🔧 Technical restart after a failure"},
	events.ServerRestartAbandoned: {"⛔ Автоперезапуск прекращён", "⛔ Automatic restart abandoned"},
	events.ServerHang:             {"🧊 Сервер завис", "🧊 Server hung"},
	events.ServerWatchdogStop:     {"⚠️ Watchdog остановил сервер", "⚠️ Watchdog stopped the server"},
	events.ServerUnavailable:      {"📡 Сервер перестал отвечать", "📡 Server stopped responding"},
	events.ServerRecovery:         {"🛑 Требуется восстановление", "🛑 Recovery required"},
	events.DatabaseVerified:       {"✅ База мира проверена: цела", "✅ World database checked: intact"},
	events.DatabaseCorrupt:        {"🛑 База мира повреждена", "🛑 World database is damaged"},
	events.HookFailed:             {"⚠️ Ошибка скрипта-хука", "⚠️ Hook script failed"},
	events.ScheduleFailed:         {"⚠️ Ошибка задания по расписанию", "⚠️ Scheduled task failed"},
	events.InstallStarted:         {"⬇️ Установка сервера начата", "⬇️ Server install started"},
	events.InstallFinished:        {"✅ Сервер установлен", "✅ Server installed"},
	events.InstallFailed:          {"❌ Ошибка установки", "❌ Install failed"},
	events.UpdateAvailable:        {"🆕 Найдено обновление", "🆕 Update found"},
	events.UpdateStarted:          {"⬇️ Сервер уходит на обновление", "⬇️ Server is going down for an update"},
	events.UpdateFinished:         {"✅ Обновление установлено", "✅ Update installed"},
	events.UpdateFailed:           {"❌ Ошибка обновления", "❌ Update failed"},
	events.BackupStarted:          {"💾 Бэкап начат", "💾 Backup started"},
	events.BackupFinished:         {"💾 Бэкап готов", "💾 Backup finished"},
	events.BackupFailed:           {"❌ Ошибка бэкапа", "❌ Backup failed"},
	events.BackupRestored:         {"♻️ Мир восстановлен из бэкапа", "♻️ World restored from a backup"},
	events.ConfigChanged:          {"📝 Настройки изменены", "📝 Settings changed"},
	events.PlayersWarned:          {"📢 Предупреждение игрокам", "📢 Notice to players"},
	events.ServerRestarted:        {"🔄 Сервер перезапущен", "🔄 Server restarted"},
	events.ScheduleSkipped:        {"⏭️ Плановый рестарт пропущен", "⏭️ Scheduled restart skipped"},
	events.RconFailed:             {"⚠️ Ошибка RCON", "⚠️ RCON failed"},
	events.RestartWarning:         {"⏳ Плановый рестарт", "⏳ Scheduled restart"},
	events.RestartManual:          {"🔄 Рестарт сервера", "🔄 Server restart"},
	events.StopManual:             {"⏹️ Остановка сервера", "⏹️ Server shutdown"},
	events.RestartDelayed:         {"⏳ Технический рестарт", "⏳ Delayed restart"},
	events.RestartCancelled:       {"↩️ Технический рестарт отменён", "↩️ Delayed restart cancelled"},
}

var detailNames = map[string]text{
	"exit":    {"Выход", "Exit"},
	"log":     {"Из лога игры", "From the game log"},
	"attempt": {"Попытка", "Attempt"},
	"error":   {"Ошибка", "Error"},
	"reason":  {"Причина", "Reason"},
	"took":    {"Длительность", "Took"},
	"event":   {"Событие", "Event"},
	"script":  {"Скрипт", "Script"},
	"action":  {"Действие", "Action"},
	"players": {"Игроков", "Players"},
	"minutes": {"Минут", "Minutes"},
	"file":    {"Файл", "File"},
	"uptime":  {"Аптайм", "Uptime"},
	"minimum": {"Минимум", "Minimum"},
	"size":    {"Размер, байт", "Size, bytes"},
}

// quiet events are left out of a channel with no explicit event list. They
// happen on every start and carry nothing the ready event does not.
var quiet = map[events.Type]bool{events.ServerStarting: true, events.ServerStarted: true}

func severityRank(s string) int {
	switch s {
	case string(events.Error):
		return 2
	case string(events.Warning):
		return 1
	}
	return 0
}

// wants decides whether a channel receives an event.
func (c Channel) wants(e events.Event) bool {
	if !c.Enabled || !c.usable() {
		return false
	}
	// No servers named means every server, including ones added later.
	if len(c.ServerIDs) > 0 {
		watched := false
		for _, id := range c.ServerIDs {
			if id == e.ServerID {
				watched = true
				break
			}
		}
		if !watched {
			return false
		}
	}
	if severityRank(string(e.Severity)) < severityRank(c.MinSeverity) {
		return false
	}
	if len(c.EventTypes) == 0 {
		return !quiet[e.Type]
	}
	for _, t := range c.EventTypes {
		if t == string(e.Type) {
			return true
		}
	}
	return false
}

func pick(t text, language string) string {
	if language == "en" {
		return t.en
	}
	return t.ru
}

// payload is a Discord webhook message: the text alone, as the server's
// message kinds set it.
type payload struct {
	Username        string `json:"username"`
	Content         string `json:"content"`
	AllowedMentions struct {
		Parse []string `json:"parse"`
		Roles []string `json:"roles,omitempty"`
		Users []string `json:"users,omitempty"`
	} `json:"allowed_mentions"`
}

// limit truncates to a service's message size; an over-long message is
// rejected outright and the notice would be lost.
func limit(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// mentionFor is the channel's mention, which only errors carry.
func mentionFor(e events.Event, c Channel) string {
	if c.Mention != "" && e.Severity == events.Error {
		return c.Mention
	}
	return ""
}

func format(e events.Event, c Channel) payload {
	text := summarize(e, c.Language).plain()
	p := payload{Username: "Conan Manager", Content: limit(text, 1900)}
	// Nothing in an event may ping anyone; only the configured mention can,
	// and only for errors.
	p.AllowedMentions.Parse = []string{}
	if mention := mentionFor(e, c); mention != "" {
		p.Content = mention + " " + p.Content
		switch {
		case mention == "@here" || mention == "@everyone":
			p.AllowedMentions.Parse = []string{"everyone"}
		case strings.HasPrefix(mention, "<@&"):
			p.AllowedMentions.Roles = []string{strings.Trim(mention, "<@&>")}
		default:
			p.AllowedMentions.Users = []string{strings.Trim(mention, "<@>")}
		}
	}
	return p
}

// discordBody is what a Discord channel posts: its own JSON template when it
// has one, else the text.
func discordBody(e events.Event, c Channel) ([]byte, error) {
	if strings.TrimSpace(c.Settings.Body) == "" {
		return json.Marshal(format(e, c))
	}
	values := summarize(e, c.Language).values()
	values["mention"] = mentionFor(e, c)
	return []byte(fill(c.Settings.Body, values, "application/json")), nil
}

// sender posts notices, honouring rate limits. client goes through the
// notification proxy when one is set; direct never does, and guarded
// reaches public addresses only.
type sender struct {
	client   *http.Client
	direct   *http.Client
	guarded  *http.Client
	proxied  bool
	attempts int
	sleep    func(context.Context, time.Duration) error
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// pick chooses the client for a channel: through the proxy when it asks
// for it and one is set, else directly, to public addresses only unless
// private ones are allowed.
func (s sender) pick(proxy, allowPrivate bool) *http.Client {
	switch {
	case proxy && s.proxied, s.direct == nil:
		return s.client
	case allowPrivate:
		return s.direct
	case s.guarded != nil:
		return s.guarded
	}
	return s.direct
}

// send posts to a Discord webhook.
func (s sender) send(ctx context.Context, webhook string, p payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.post(ctx, webhook, body)
}

func (s sender) post(ctx context.Context, webhook string, body []byte) error {
	return s.deliver(ctx, s.client, "Discord", webhook, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
}

// deliver makes a request until it succeeds, retrying rate limits and
// server errors. secret is kept out of the errors: net/http puts the whole
// URL, and with it a token, into them.
func (s sender) deliver(ctx context.Context, client *http.Client, service, secret string, build func() (*http.Request, error)) error {
	var last error
	for attempt := 1; attempt <= s.attempts; attempt++ {
		req, err := build()
		if err != nil {
			return fmt.Errorf("request: %s", redact(err.Error(), secret))
		}
		resp, err := client.Do(req)
		if err != nil {
			last = fmt.Errorf("request failed: %s", redact(err.Error(), secret))
			if errors.Is(err, errPrivateAddress) || s.sleep(ctx, time.Duration(attempt)*2*time.Second) != nil {
				return last
			}
			continue
		}
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := retryAfter(resp, detail)
			last = fmt.Errorf("rate limited by %s", service)
			if s.sleep(ctx, wait) != nil {
				return last
			}
		case resp.StatusCode >= 500:
			last = fmt.Errorf("%s returned %s", service, resp.Status)
			if s.sleep(ctx, time.Duration(attempt)*2*time.Second) != nil {
				return last
			}
		default:
			// Other answers will not change on retry: a deleted webhook, a
			// wrong token, or a message the service refuses.
			return fmt.Errorf("%s rejected the message: %s %s", service, resp.Status, redact(strings.TrimSpace(string(detail)), secret))
		}
	}
	return last
}

func retryAfter(resp *http.Response, body []byte) time.Duration {
	var parsed struct {
		RetryAfter float64 `json:"retry_after"`
		// Telegram puts it here.
		Parameters struct {
			RetryAfter float64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		if parsed.RetryAfter > 0 {
			return time.Duration(parsed.RetryAfter * float64(time.Second))
		}
		if parsed.Parameters.RetryAfter > 0 {
			return time.Duration(parsed.Parameters.RetryAfter * float64(time.Second))
		}
	}
	if seconds, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	return 2 * time.Second
}

// redact replaces a secret with its masked form.
func redact(message, secret string) string {
	if secret == "" {
		return message
	}
	masked := "…"
	if strings.HasPrefix(secret, "https://discord") {
		masked = mask(secret)
	}
	return strings.ReplaceAll(message, secret, masked)
}
