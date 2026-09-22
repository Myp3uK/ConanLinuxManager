// Package panel keeps what the operator changes from the web interface about
// the manager itself: its settings and the extra administrators.
package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Kind tells the interface how to edit a setting and the store how to check it.
type Kind string

const (
	KindText     Kind = "text"
	KindInt      Kind = "int"
	KindDuration Kind = "duration"
	KindBool     Kind = "bool"
	KindPath     Kind = "path"
	KindProxy    Kind = "proxy"
	KindOrigin   Kind = "origin"
)

// Setting is one environment variable the panel may override. The list is
// deliberately short: whatever decides where the manager listens, who logs in,
// which key unlocks secrets or which directory holds runnable scripts stays in
// the root-owned environment file, so a stolen panel login cannot widen itself.
type Setting struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Help    string `json:"help"`
	Kind    Kind   `json:"kind"`
	Default string `json:"default"`
	// Live settings take effect as soon as they are saved; the rest on the
	// next start of the manager.
	Live  bool   `json:"live"`
	Group string `json:"group"`
}

// Settings lists the overridable variables in the order the panel shows them.
var Settings = []Setting{
	{Key: "CONAN_MANAGER_NOTIFY_PROXY", Group: "Уведомления", Label: "Прокси для уведомлений", Kind: KindProxy, Live: true,
		Help: "Через него идут сообщения в Discord, если хост не видит Discord напрямую. Формат: socks5://host:1080, http://user:pass@host:3128. Пусто - напрямую."},
	{Key: "CONAN_MANAGER_BACKUP_DIR", Group: "Бэкапы", Label: "Папка бэкапов менеджера", Kind: KindPath, Default: "/var/lib/conan-manager/backups",
		Help: "Внутри - по папке на сервер. Может быть на другом диске; пользователь conan-manager должен иметь право писать в неё."},
	{Key: "CONAN_MANAGER_BACKUP_KEEP", Group: "Бэкапы", Label: "Бэкапов менеджера на сервер", Kind: KindInt, Default: "20",
		Help: "Старые удаляются после каждого нового бэкапа."},
	{Key: "CONAN_MANAGER_BACKUP_RETENTION", Group: "Бэкапы", Label: "Бэкапов игры хранить", Kind: KindInt, Default: "10",
		Help: "Сколько собственных бэкапов игры (game_backup_*.db в Saved) оставлять при каждом запуске."},
	{Key: "CONAN_MANAGER_STOP_TIMEOUT", Group: "Серверы", Label: "Ожидание остановки", Kind: KindDuration, Default: "5m",
		Help: "Сколько ждать нормального выключения сервера, прежде чем убить процесс и проверить базу. TimeoutStopSec в conan-server@.service должен быть больше."},
	{Key: "CONAN_MANAGER_UPDATE_CHECK_INTERVAL", Group: "Серверы", Label: "Проверка обновлений", Kind: KindDuration, Default: "30m",
		Help: "Как часто спрашивать Steam о новой сборке на работающих серверах. Не чаще раза в 5 минут.\n0 - не проверять по времени: с автообновлением Steam спрашивается только перед запуском сервера (кнопкой, по расписанию, при плановом и отложенном рестарте)."},
	{Key: "CONAN_MANAGER_HOOK_TIMEOUT", Group: "Серверы", Label: "Лимит времени скрипта", Kind: KindDuration, Default: "5m",
		Help: "Скрипт, который работает дольше, завершается принудительно."},
	{Key: "CONAN_MANAGER_QUERY_INTERVAL", Group: "Проверка доступности", Label: "Интервал опроса", Kind: KindDuration, Default: "15s",
		Help: "Как часто менеджер спрашивает сервер через query-порт Steam."},
	{Key: "CONAN_MANAGER_QUERY_TIMEOUT", Group: "Проверка доступности", Label: "Ожидание ответа", Kind: KindDuration, Default: "3s",
		Help: "Сколько ждать ответа на один запрос."},
	{Key: "CONAN_MANAGER_QUERY_STARTUP_GRACE", Group: "Проверка доступности", Label: "Время на загрузку", Kind: KindDuration, Default: "10m",
		Help: "Сколько сервер может загружаться, прежде чем молчание query-порта считается сбоем.\n0 - без ограничения: пока сервер ни разу не ответил, он считается загружающимся."},
	{Key: "CONAN_MANAGER_QUERY_UNAVAILABLE_FOR", Group: "Проверка доступности", Label: "Недоступен после", Kind: KindDuration, Default: "5m",
		Help: "Сколько загруженный сервер может молчать, прежде чем его отметят недоступным и уйдёт уведомление.\n0 - не отмечать: молчание видно только как «Задержки». Проверка на зомби настраивается у сервера отдельно."},
	{Key: "CONAN_MANAGER_ALLOWED_ORIGIN", Group: "Доступ", Label: "Адрес панели за прокси", Kind: KindOrigin,
		Help: "Нужен, если панель открывают через HTTPS reverse proxy под другим адресом: https://manager.example.com. Пусто - только адрес, по которому открыта страница."},
	{Key: "CONAN_MANAGER_SESSION_IDLE", Group: "Доступ", Label: "Выход при бездействии", Kind: KindDuration, Default: "12h",
		Help: "Сессия браузера, которой не пользовались столько времени, закрывается, и нужно войти снова. Открытая вкладка панели сессию не отпускает.\n0 - не закрывать по бездействию; через 7 дней после входа сессия закрывается в любом случае."},
	{Key: "CONAN_MANAGER_EXPOSE_ERROR_DETAIL", Group: "Доступ", Label: "Подробные ошибки", Kind: KindBool, Default: "false",
		Help: "Показывать текст внутренних ошибок в панели. В них бывают пути к файлам; полезно при настройке."},
}

// Lookup finds a setting by its variable name.
func Lookup(key string) (Setting, bool) {
	for _, s := range Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// offable settings are turned off by 0.
var offable = map[string]bool{
	"CONAN_MANAGER_UPDATE_CHECK_INTERVAL": true,
	"CONAN_MANAGER_QUERY_STARTUP_GRACE":   true,
	"CONAN_MANAGER_QUERY_UNAVAILABLE_FOR": true,
	"CONAN_MANAGER_SESSION_IDLE":          true,
}

// Normalize checks a value for a setting and returns the form to store.
func (s Setting) Normalize(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("value must be one line")
	}
	switch s.Kind {
	case KindInt:
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100000 {
			return "", errors.New("must be a whole number from 1")
		}
		return strconv.Itoa(n), nil
	case KindDuration:
		if offable[s.Key] && (value == "0" || strings.EqualFold(value, "off")) {
			return "0", nil
		}
		d, err := time.ParseDuration(value)
		if err != nil || d < time.Second || d > 7*24*time.Hour {
			return "", errors.New("must be a duration such as 30s, 5m or 1h")
		}
		return value, nil
	case KindBool:
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			return "true", nil
		case "0", "false", "no", "off", "":
			return "false", nil
		}
		return "", errors.New("must be true or false")
	case KindPath:
		// The manager runs on Linux, so paths are checked the POSIX way.
		if !path.IsAbs(value) || path.Clean(value) != value || value == "/" {
			return "", errors.New("must be an absolute path")
		}
		return value, nil
	case KindProxy:
		if value == "" {
			return "", nil
		}
		u, err := url.Parse(value)
		if err != nil || u.Host == "" {
			return "", errors.New("must be a URL such as socks5://host:1080")
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
			return value, nil
		}
		return "", errors.New("scheme must be http, https or socks5")
	case KindOrigin:
		if value == "" {
			return "", nil
		}
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
			return "", errors.New("must be an origin such as https://manager.example.com")
		}
		return value, nil
	}
	if len(value) > 500 {
		return "", errors.New("value is too long")
	}
	return value, nil
}

// Display returns a value safe to show: credentials in a proxy URL are masked.
func (s Setting) Display(value string) string {
	if s.Kind != KindProxy || value == "" {
		return value
	}
	u, err := url.Parse(value)
	if err != nil {
		return value
	}
	return u.Redacted()
}

// Store keeps the overrides in SQLite.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Values returns every stored override.
func (s *Store) Values(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM panel_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		// A variable that is no longer overridable is ignored rather than
		// applied, so a downgrade of the list takes effect.
		if _, ok := Lookup(key); ok {
			values[key] = value
		}
	}
	return values, rows.Err()
}

// Set stores an override after checking it; it returns the stored form.
func (s *Store) Set(ctx context.Context, key, value string) (string, error) {
	setting, ok := Lookup(key)
	if !ok {
		return "", fmt.Errorf("%s cannot be changed from the panel", key)
	}
	value, err := setting.Normalize(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO panel_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, time.Now().UTC().Format(time.RFC3339Nano))
	return value, err
}

// Reset removes an override, so the environment file applies again.
func (s *Store) Reset(ctx context.Context, key string) error {
	if _, ok := Lookup(key); !ok {
		return fmt.Errorf("%s cannot be changed from the panel", key)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM panel_settings WHERE key=?`, key)
	return err
}

// Overlay returns a lookup that prefers stored overrides over env.
func Overlay(stored map[string]string, env func(string) string) func(string) string {
	return func(key string) string {
		if value, ok := stored[key]; ok {
			return value
		}
		return env(key)
	}
}
