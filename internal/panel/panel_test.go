package panel

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/config"
)

// The defaults the panel shows must be the ones config applies.
func TestDefaultsMatchConfig(t *testing.T) {
	cfg := config.LoadFrom(func(string) string { return "" })
	got := map[string]string{
		"CONAN_MANAGER_BACKUP_DIR":            cfg.BackupDir,
		"CONAN_MANAGER_BACKUP_KEEP":           strconv.Itoa(cfg.BackupKeep),
		"CONAN_MANAGER_BACKUP_RETENTION":      strconv.Itoa(cfg.BackupRetention),
		"CONAN_MANAGER_STOP_TIMEOUT":          cfg.StopTimeout.String(),
		"CONAN_MANAGER_UPDATE_CHECK_INTERVAL": cfg.UpdateCheckInterval.String(),
		"CONAN_MANAGER_HOOK_TIMEOUT":          cfg.HookTimeout.String(),
		"CONAN_MANAGER_QUERY_INTERVAL":        cfg.Availability.Interval.String(),
		"CONAN_MANAGER_QUERY_TIMEOUT":         cfg.Availability.Timeout.String(),
		"CONAN_MANAGER_QUERY_STARTUP_GRACE":   cfg.Availability.StartupGrace.String(),
		"CONAN_MANAGER_QUERY_UNAVAILABLE_FOR": cfg.Availability.UnavailableFor.String(),
		"CONAN_MANAGER_EXPOSE_ERROR_DETAIL":   strconv.FormatBool(cfg.ExposeErrorDetail),
		"CONAN_MANAGER_NOTIFY_PROXY":          cfg.NotifyProxy,
		"CONAN_MANAGER_ALLOWED_ORIGIN":        cfg.AllowedOrigin,
	}
	for _, s := range Settings {
		want, ok := got[s.Key]
		if !ok {
			t.Errorf("%s: not checked", s.Key)
			continue
		}
		def := s.Default
		if s.Kind == KindDuration {
			d, err := time.ParseDuration(def)
			if err != nil {
				t.Fatal(err)
			}
			def = d.String()
		}
		if def != want {
			t.Errorf("%s: panel default %q, config %q", s.Key, def, want)
		}
	}
}

func TestOverlayReachesConfig(t *testing.T) {
	cfg := config.LoadFrom(Overlay(map[string]string{"CONAN_MANAGER_BACKUP_KEEP": "3"},
		func(k string) string {
			if k == "CONAN_MANAGER_BACKUP_KEEP" {
				return "9"
			}
			return ""
		}))
	if cfg.BackupKeep != 3 {
		t.Fatalf("backup keep = %d, want the stored 3", cfg.BackupKeep)
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		key, value string
		ok         bool
	}{
		{"CONAN_MANAGER_BACKUP_DIR", "/mnt/disk2/backups", true},
		{"CONAN_MANAGER_BACKUP_DIR", "backups", false},
		{"CONAN_MANAGER_BACKUP_DIR", "/", false},
		{"CONAN_MANAGER_BACKUP_DIR", "/a/../b", false},
		{"CONAN_MANAGER_UPDATE_CHECK_INTERVAL", "off", true},
		{"CONAN_MANAGER_STOP_TIMEOUT", "off", false},
		{"CONAN_MANAGER_STOP_TIMEOUT", "10m", true},
		{"CONAN_MANAGER_NOTIFY_PROXY", "ftp://x", false},
		{"CONAN_MANAGER_NOTIFY_PROXY", "", true},
		{"CONAN_MANAGER_ALLOWED_ORIGIN", "https://m.example.com", true},
		{"CONAN_MANAGER_ALLOWED_ORIGIN", "https://m.example.com/x", false},
		{"CONAN_MANAGER_BACKUP_KEEP", "0", false},
		{"CONAN_MANAGER_EXPOSE_ERROR_DETAIL", "yes", true},
	}
	for _, c := range cases {
		s, _ := Lookup(c.key)
		_, err := s.Normalize(c.value)
		if (err == nil) != c.ok {
			t.Errorf("%s=%q: err %v, want ok=%v", c.key, c.value, err, c.ok)
		}
	}
	for _, key := range []string{"CONAN_MANAGER_HOOKS_DIR", "CONAN_MANAGER_ADMIN_PASSWORD", "CONAN_MANAGER_SECRET_KEY", "CONAN_MANAGER_LISTEN"} {
		if _, ok := Lookup(key); ok {
			t.Errorf("%s must stay in the environment file", key)
		}
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") || !verifyPassword(hash, "correct horse battery") || verifyPassword(hash, "correct horse batterY") {
		t.Fatalf("hash round trip failed: %s", hash)
	}
	if verifyPassword("plain", "plain") {
		t.Fatal("unknown format accepted")
	}
}
