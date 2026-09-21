package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/watchdog"
)

type Config struct {
	ListenAddress   string
	DatabasePath    string
	AdminUsername   string
	AdminPassword   string
	AllowedOrigin   string
	BackupRetention int
	// BackupDir holds the manager's own world backups, one directory per
	// server; it can be on another disk than the servers.
	BackupDir string
	// BackupKeep is how many manager backups each server keeps; 0 keeps all.
	BackupKeep int
	// NotifyProxy carries outgoing notifications (http://, https:// or
	// socks5://) where the host cannot reach Discord directly.
	NotifyProxy  string
	Availability watchdog.AvailabilityPolicy
	QueryHost    string
	// SecretKey unlocks stored backup credentials. It is supplied by the service
	// environment and never written to the database.
	SecretKey string
	// LauncherIDs enables the systemd launcher for the listed server IDs.
	LauncherIDs string
	// ExposeErrorDetail returns internal error text to API clients. It is off by
	// default because those messages carry absolute filesystem paths.
	ExposeErrorDetail bool
	// ServerUnits runs each server as a conan-server@ systemd unit, so servers
	// keep running while the manager restarts. It needs the unit template and
	// the polkit rule from deploy/.
	ServerUnits bool
	// UnitDir holds the launch specifications the units read.
	UnitDir string
	// StopTimeout is how long a server may take to stop before it is killed
	// and its world checked. Keep TimeoutStopSec of conan-server@.service
	// longer, so the manager, not systemd, does the kill and the check.
	StopTimeout time.Duration
	// HooksDir holds the operator's hook scripts. It must be owned by root and
	// not writable by the service user.
	HooksDir string
	// HookTimeout bounds each hook script.
	HookTimeout time.Duration
	// UpdateCheckInterval is how often Steam is asked for a newer build.
	// Zero disables the check ("off" in the environment).
	UpdateCheckInterval time.Duration
}

func Load() Config {
	return Config{
		ListenAddress:       env("CONAN_MANAGER_LISTEN", "127.0.0.1:8080"),
		DatabasePath:        env("CONAN_MANAGER_DATABASE", "/var/lib/conan-manager/manager.db"),
		AdminUsername:       os.Getenv("CONAN_MANAGER_ADMIN_USERNAME"),
		AdminPassword:       os.Getenv("CONAN_MANAGER_ADMIN_PASSWORD"),
		AllowedOrigin:       os.Getenv("CONAN_MANAGER_ALLOWED_ORIGIN"),
		SecretKey:           os.Getenv("CONAN_MANAGER_SECRET_KEY"),
		LauncherIDs:         os.Getenv("CONAN_MANAGER_LAUNCHER_IDS"),
		ExposeErrorDetail:   boolean("CONAN_MANAGER_EXPOSE_ERROR_DETAIL"),
		ServerUnits:         boolean("CONAN_MANAGER_SERVER_UNITS"),
		UnitDir:             env("CONAN_MANAGER_UNIT_DIR", "/var/lib/conan-manager/units"),
		StopTimeout:         duration("CONAN_MANAGER_STOP_TIMEOUT", 5*time.Minute),
		HooksDir:            env("CONAN_MANAGER_HOOKS_DIR", "/etc/conan-manager/hooks"),
		HookTimeout:         duration("CONAN_MANAGER_HOOK_TIMEOUT", 5*time.Minute),
		UpdateCheckInterval: updateInterval(),
		BackupRetention:     integer("CONAN_MANAGER_BACKUP_RETENTION", 10),
		BackupDir:           env("CONAN_MANAGER_BACKUP_DIR", "/var/lib/conan-manager/backups"),
		BackupKeep:          integer("CONAN_MANAGER_BACKUP_KEEP", 20),
		NotifyProxy:         env("CONAN_MANAGER_NOTIFY_PROXY", ""),
		QueryHost:           env("CONAN_MANAGER_QUERY_HOST", "127.0.0.1"),
		Availability: watchdog.AvailabilityPolicy{
			Interval:       duration("CONAN_MANAGER_QUERY_INTERVAL", 15*time.Second),
			Timeout:        duration("CONAN_MANAGER_QUERY_TIMEOUT", 3*time.Second),
			StartupGrace:   duration("CONAN_MANAGER_QUERY_STARTUP_GRACE", 10*time.Minute),
			UnavailableFor: duration("CONAN_MANAGER_QUERY_UNAVAILABLE_FOR", 5*time.Minute),
		},
	}
}

func updateInterval() time.Duration {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CONAN_MANAGER_UPDATE_CHECK_INTERVAL")), "off") {
		return 0
	}
	value := duration("CONAN_MANAGER_UPDATE_CHECK_INTERVAL", 30*time.Minute)
	// Each check starts SteamCMD; more often than every five minutes buys
	// nothing, since Steam's own app info refreshes no faster.
	if value < 5*time.Minute {
		value = 5 * time.Minute
	}
	return value
}

func boolean(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func duration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err != nil || value < time.Second {
		return fallback
	}
	return value
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func integer(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}
