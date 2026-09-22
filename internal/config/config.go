package config

import (
	"fmt"
	"net/netip"
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
	// AllowedIPs lists the networks that may reach the manager; empty allows
	// every address.
	AllowedIPs []netip.Prefix
	// TrustedProxies are reverse proxies whose X-Forwarded-For and
	// X-Forwarded-Proto headers are believed.
	TrustedProxies []netip.Prefix
	// TLSCert and TLSKey make the manager serve HTTPS itself.
	TLSCert string
	TLSKey  string
	// BasicAuth lets scripts log in with HTTP Basic Auth; the browser uses
	// sessions either way.
	BasicAuth bool
	// SessionIdle ends a browser session unused for that long; 0 never does.
	SessionIdle time.Duration
	// SessionMaxAge ends every browser session after that long.
	SessionMaxAge time.Duration
	// EnvFile is the environment file whose permissions are checked.
	EnvFile string
	// Problems are settings that could not be read. Access settings among
	// them stop the manager rather than leave it more open than intended.
	Problems []string
}

func Load() Config {
	return LoadFrom(os.Getenv)
}

// LoadFrom reads the configuration through lookup, so settings the operator
// saved in the panel can take the place of the environment.
func LoadFrom(lookup func(string) string) Config {
	r := reader(lookup)
	var problems []string
	prefixes := func(name string) []netip.Prefix {
		list, err := ParsePrefixes(r.get(name))
		if err != nil {
			problems = append(problems, name+": "+err.Error())
		}
		return list
	}
	cfg := Config{
		AllowedIPs:          prefixes("CONAN_MANAGER_ALLOWED_IPS"),
		TrustedProxies:      prefixes("CONAN_MANAGER_TRUSTED_PROXIES"),
		TLSCert:             r.get("CONAN_MANAGER_TLS_CERT"),
		TLSKey:              r.get("CONAN_MANAGER_TLS_KEY"),
		BasicAuth:           r.booleanOr("CONAN_MANAGER_BASIC_AUTH", true),
		SessionIdle:         r.durationOrOff("CONAN_MANAGER_SESSION_IDLE", 12*time.Hour),
		SessionMaxAge:       r.duration("CONAN_MANAGER_SESSION_MAX_AGE", 7*24*time.Hour),
		EnvFile:             r.env("CONAN_MANAGER_ENV_FILE", "/etc/conan-manager/conan-manager.env"),
		ListenAddress:       r.env("CONAN_MANAGER_LISTEN", "127.0.0.1:8080"),
		DatabasePath:        r.env("CONAN_MANAGER_DATABASE", "/var/lib/conan-manager/manager.db"),
		AdminUsername:       r.get("CONAN_MANAGER_ADMIN_USERNAME"),
		AdminPassword:       r.get("CONAN_MANAGER_ADMIN_PASSWORD"),
		AllowedOrigin:       r.get("CONAN_MANAGER_ALLOWED_ORIGIN"),
		SecretKey:           r.get("CONAN_MANAGER_SECRET_KEY"),
		LauncherIDs:         r.get("CONAN_MANAGER_LAUNCHER_IDS"),
		ExposeErrorDetail:   r.boolean("CONAN_MANAGER_EXPOSE_ERROR_DETAIL"),
		ServerUnits:         r.boolean("CONAN_MANAGER_SERVER_UNITS"),
		UnitDir:             r.env("CONAN_MANAGER_UNIT_DIR", "/var/lib/conan-manager/units"),
		StopTimeout:         r.duration("CONAN_MANAGER_STOP_TIMEOUT", 5*time.Minute),
		HooksDir:            r.env("CONAN_MANAGER_HOOKS_DIR", "/etc/conan-manager/hooks"),
		HookTimeout:         r.duration("CONAN_MANAGER_HOOK_TIMEOUT", 5*time.Minute),
		UpdateCheckInterval: r.updateInterval(),
		BackupRetention:     r.integer("CONAN_MANAGER_BACKUP_RETENTION", 10),
		BackupDir:           r.env("CONAN_MANAGER_BACKUP_DIR", "/var/lib/conan-manager/backups"),
		BackupKeep:          r.integer("CONAN_MANAGER_BACKUP_KEEP", 20),
		NotifyProxy:         r.env("CONAN_MANAGER_NOTIFY_PROXY", ""),
		QueryHost:           r.env("CONAN_MANAGER_QUERY_HOST", "127.0.0.1"),
		Availability: watchdog.AvailabilityPolicy{
			Interval:       r.duration("CONAN_MANAGER_QUERY_INTERVAL", 15*time.Second),
			Timeout:        r.duration("CONAN_MANAGER_QUERY_TIMEOUT", 3*time.Second),
			StartupGrace:   r.durationOrOff("CONAN_MANAGER_QUERY_STARTUP_GRACE", 10*time.Minute),
			UnavailableFor: r.durationOrOff("CONAN_MANAGER_QUERY_UNAVAILABLE_FOR", 5*time.Minute),
		},
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		problems = append(problems, "CONAN_MANAGER_TLS_CERT and CONAN_MANAGER_TLS_KEY must be set together")
	}
	cfg.Problems = problems
	return cfg
}

// ParsePrefixes reads a list of networks and addresses separated by commas
// or spaces: "10.0.0.0/8, 192.168.1.5, 2001:db8::/32". An address alone
// stands for itself.
func ParsePrefixes(value string) ([]netip.Prefix, error) {
	var list []netip.Prefix
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '	' }) {
		if strings.Contains(item, "/") {
			p, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, fmt.Errorf("%q is not a network such as 10.0.0.0/8", item)
			}
			list = append(list, p.Masked())
			continue
		}
		ip, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address", item)
		}
		ip = ip.Unmap()
		list = append(list, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return list, nil
}

type reader func(string) string

func (r reader) get(name string) string { return r(name) }

func (r reader) updateInterval() time.Duration {
	value := r.durationOrOff("CONAN_MANAGER_UPDATE_CHECK_INTERVAL", 30*time.Minute)
	if value == 0 {
		return 0
	}
	// Each check starts SteamCMD; more often than every five minutes buys
	// nothing, since Steam's own app info refreshes no faster.
	if value < 5*time.Minute {
		value = 5 * time.Minute
	}
	return value
}

func (r reader) boolean(name string) bool {
	switch strings.ToLower(strings.TrimSpace(r.get(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (r reader) booleanOr(name string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(r.get(name))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

func (r reader) duration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(r.get(name))
	if err != nil || value < time.Second {
		return fallback
	}
	return value
}

// durationOrOff is a duration where 0 or "off" turns the feature off.
func (r reader) durationOrOff(name string, fallback time.Duration) time.Duration {
	switch strings.ToLower(strings.TrimSpace(r.get(name))) {
	case "0", "0s", "off":
		return 0
	}
	return r.duration(name, fallback)
}

func (r reader) env(name, fallback string) string {
	if value := r.get(name); value != "" {
		return value
	}
	return fallback
}

func (r reader) integer(name string, fallback int) int {
	value, err := strconv.Atoi(r.get(name))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}
