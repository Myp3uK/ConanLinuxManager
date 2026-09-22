package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
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

var colors = map[events.Severity]int{events.Info: 0x2ECC71, events.Warning: 0xF1C40F, events.Error: 0xE74C3C}

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
	if !c.Enabled || c.webhook == "" {
		return false
	}
	if c.ServerID != "" && c.ServerID != e.ServerID {
		return false
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

type embedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type embed struct {
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color"`
	Fields      []embedField `json:"fields,omitempty"`
	Timestamp   string       `json:"timestamp"`
	Footer      struct {
		Text string `json:"text"`
	} `json:"footer"`
}

type payload struct {
	Username        string  `json:"username"`
	Content         string  `json:"content,omitempty"`
	Embeds          []embed `json:"embeds"`
	AllowedMentions struct {
		Parse []string `json:"parse"`
		Roles []string `json:"roles,omitempty"`
		Users []string `json:"users,omitempty"`
	} `json:"allowed_mentions"`
}

// limit truncates to Discord's field sizes; an over-long embed is rejected
// outright and the notice would be lost.
func limit(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func format(e events.Event, c Channel) payload {
	title := e.Message
	if t, ok := titles[e.Type]; ok {
		title = pick(t, c.Language)
	}
	server := e.ServerName
	if server == "" {
		server = e.ServerID
	}
	em := embed{Title: limit(title, 256), Color: colors[e.Severity], Timestamp: e.Time.UTC().Format(time.RFC3339)}
	if server != "" {
		em.Description = "**" + limit(server, 200) + "**"
	}
	if _, known := titles[e.Type]; known && e.Message != "" && (e.Type == events.ServerRestartAbandoned || e.Type == events.ServerKilled || e.Type == events.ServerRestarting) {
		em.Description += "\n" + limit(e.Message, 1000)
	}
	// A server's own text for the event is the message itself, not a field.
	if text := e.Details["announce"]; text != "" {
		em.Description += "\n" + limit(text, 1500)
	}
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		// announce is the message; discord and kind steer delivery only.
		if k != "announce" && k != "discord" && k != "kind" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := k
		if n, ok := detailNames[k]; ok {
			name = pick(n, c.Language)
		}
		value := e.Details[k]
		inline := len(value) < 40
		if !inline {
			value = "```" + limit(value, 900) + "```"
		}
		em.Fields = append(em.Fields, embedField{Name: limit(name, 256), Value: value, Inline: inline})
		if len(em.Fields) == 10 {
			break
		}
	}
	em.Footer.Text = "conan-manager · " + string(e.Type)
	p := payload{Username: "Conan Manager", Embeds: []embed{em}}
	// Nothing in an event may ping anyone; only the configured mention can,
	// and only for errors.
	p.AllowedMentions.Parse = []string{}
	if c.Mention != "" && e.Severity == events.Error {
		p.Content = c.Mention
		switch {
		case c.Mention == "@here" || c.Mention == "@everyone":
			p.AllowedMentions.Parse = []string{"everyone"}
		case strings.HasPrefix(c.Mention, "<@&"):
			p.AllowedMentions.Roles = []string{strings.Trim(c.Mention, "<@&>")}
		default:
			p.AllowedMentions.Users = []string{strings.Trim(c.Mention, "<@>")}
		}
	}
	return p
}

// sender posts to a webhook, honouring Discord's rate limits.
type sender struct {
	client   *http.Client
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

func (s sender) send(ctx context.Context, webhook string, p payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var last error
	for attempt := 1; attempt <= s.attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			// The error text of a failed request contains the URL, and with it
			// the token; report only what went wrong.
			last = fmt.Errorf("request failed: %s", redact(err.Error(), webhook))
			if s.sleep(ctx, time.Duration(attempt)*2*time.Second) != nil {
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
			last = fmt.Errorf("rate limited by Discord")
			if s.sleep(ctx, wait) != nil {
				return last
			}
		case resp.StatusCode >= 500:
			last = fmt.Errorf("Discord returned %s", resp.Status)
			if s.sleep(ctx, time.Duration(attempt)*2*time.Second) != nil {
				return last
			}
		default:
			// 4xx other than 429 will not succeed on retry: a deleted
			// webhook, or a message Discord refuses.
			return fmt.Errorf("Discord rejected the message: %s %s", resp.Status, strings.TrimSpace(string(detail)))
		}
	}
	return last
}

func retryAfter(resp *http.Response, body []byte) time.Duration {
	var parsed struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.RetryAfter > 0 {
		return time.Duration(parsed.RetryAfter * float64(time.Second))
	}
	if seconds, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	return 2 * time.Second
}

func redact(message, webhook string) string {
	return strings.ReplaceAll(message, webhook, mask(webhook))
}
