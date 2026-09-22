package httpapi

import (
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/config"
)

// check is one finding of the security self-check.
type check struct {
	// Level is ok, warn or bad.
	Level  string `json:"level"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// securityChecks looks at how the manager is deployed: who can read its
// secrets, whether logins cross the network in clear, whether the defaults
// from the example file are still in place.
func securityChecks(cfg config.Config) []check {
	var out []check
	add := func(level, title, detail string) { out = append(out, check{level, title, detail}) }

	switch info, err := os.Stat(cfg.EnvFile); {
	case errors.Is(err, fs.ErrPermission):
		add("ok", "Файл окружения закрыт", cfg.EnvFile+": служба не может даже заглянуть в его папку, systemd читает его от root.")
	case errors.Is(err, fs.ErrNotExist):
		add("warn", "Файл окружения не найден", cfg.EnvFile+" отсутствует. Если настройки заданы иначе, укажите путь в CONAN_MANAGER_ENV_FILE.")
	case err != nil:
		add("warn", "Файл окружения не проверен", err.Error())
	default:
		mode := info.Mode().Perm()
		uid, known := fileOwner(info)
		switch {
		case mode&0o077 != 0:
			add("bad", "Файл окружения доступен другим", cfg.EnvFile+" имеет права "+mode.String()+": пароль администратора и ключ шифрования может прочитать не только root. Выполните chmod 600.")
		case known && uid != 0:
			add("bad", "Файл окружения принадлежит не root", cfg.EnvFile+": владелец может менять пароль администратора и ключ. Выполните chown root:root.")
		default:
			add("ok", "Файл окружения защищён", cfg.EnvFile+": "+mode.String()+", владелец root.")
		}
	}

	if info, err := os.Stat(cfg.DatabasePath); err == nil {
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			add("bad", "База доступна другим", cfg.DatabasePath+" имеет права "+mode.String()+". Выполните chmod 600.")
		} else {
			add("ok", "База закрыта", cfg.DatabasePath+": "+mode.String()+".")
		}
	}
	if dir := filepath.Dir(cfg.DatabasePath); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.Mode().Perm()&0o007 != 0 {
			add("warn", "Папка данных открыта всем", dir+" имеет права "+info.Mode().Perm().String()+". Достаточно 750.")
		}
	}
	if info, err := os.Stat(cfg.BackupDir); err == nil && info.Mode().Perm()&0o007 != 0 {
		add("warn", "Папка бэкапов открыта всем", cfg.BackupDir+" имеет права "+info.Mode().Perm().String()+": бэкапы мира может прочитать любой пользователь. Достаточно 750.")
	}

	switch key := cfg.SecretKey; {
	case key == "":
		add("warn", "Ключ шифрования не задан", "Без CONAN_MANAGER_SECRET_KEY уведомления выключены: адреса вебхуков негде хранить зашифрованными.")
	case key == "replace-with-a-long-random-value" || len(key) < 32:
		add("bad", "Слабый ключ шифрования", "CONAN_MANAGER_SECRET_KEY короче 32 символов или взят из примера. Создайте новый: openssl rand -base64 48. Каналы уведомлений после смены придётся ввести заново.")
	default:
		add("ok", "Ключ шифрования задан", "")
	}
	if cfg.AdminUsername != "" && (cfg.AdminPassword == "change-me" || len([]rune(cfg.AdminPassword)) < 12) {
		add("bad", "Слабый пароль основного администратора", "CONAN_MANAGER_ADMIN_PASSWORD короче 12 символов или взят из примера.")
	}

	exposed := !loopbackListen(cfg.ListenAddress)
	switch {
	case cfg.TLSCert != "":
		add("ok", "HTTPS", "Менеджер сам отдаёт HTTPS.")
	case len(cfg.TrustedProxies) > 0 && !exposed:
		add("ok", "HTTPS через прокси", "Менеджер слушает только локальный адрес, наружу его выводит reverse proxy.")
	case exposed:
		add("bad", "Панель доступна по HTTP", "Менеджер слушает "+cfg.ListenAddress+" без TLS: пароль и сессия идут по сети открыто. Поставьте reverse proxy с HTTPS или задайте CONAN_MANAGER_TLS_CERT и CONAN_MANAGER_TLS_KEY.")
	default:
		add("ok", "Только локальный адрес", "Менеджер слушает "+cfg.ListenAddress+"; снаружи - через SSH-туннель или reverse proxy.")
	}
	if exposed && len(cfg.AllowedIPs) == 0 {
		add("warn", "Нет списка разрешённых адресов", "Панель открыта с любого адреса. Ограничьте её в CONAN_MANAGER_ALLOWED_IPS.")
	}
	if cfg.BasicAuth {
		add("warn", "Basic Auth включён", "Скрипты могут входить логином и паролем в каждом запросе. Если они не нужны, выключите: CONAN_MANAGER_BASIC_AUTH=false.")
	}
	for _, p := range cfg.Problems {
		add("bad", "Ошибка в настройках", p)
	}
	return out
}

func loopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func prefixStrings(list []netip.Prefix) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		if p.Bits() == p.Addr().BitLen() {
			out = append(out, p.Addr().String())
		} else {
			out = append(out, p.String())
		}
	}
	return out
}

// security serves GET /security: the self-check and the access settings.
func (a *api) security(w http.ResponseWriter, r *http.Request) {
	c := clientOf(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"checks": securityChecks(a.cfg),
		"access": map[string]any{
			"allowed_ips":     prefixStrings(a.cfg.AllowedIPs),
			"trusted_proxies": prefixStrings(a.cfg.TrustedProxies),
			"tls":             a.cfg.TLSCert != "",
			"basic_auth":      a.cfg.BasicAuth,
			"session_idle":    durationText(a.cfg.SessionIdle),
			"session_max_age": durationText(a.cfg.SessionMaxAge),
			"client_ip":       clientIP(r),
			"https":           c.HTTPS,
			"listen":          a.cfg.ListenAddress,
		},
	})
}

func durationText(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	return strings.TrimSuffix(s, "0m")
}
