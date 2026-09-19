package config

import (
	"os"
	"strconv"
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
	Availability    watchdog.AvailabilityPolicy
	QueryHost       string
}

func Load() Config {
	return Config{
		ListenAddress:   env("CONAN_MANAGER_LISTEN", "127.0.0.1:8080"),
		DatabasePath:    env("CONAN_MANAGER_DATABASE", "/var/lib/conan-manager/manager.db"),
		AdminUsername:   os.Getenv("CONAN_MANAGER_ADMIN_USERNAME"),
		AdminPassword:   os.Getenv("CONAN_MANAGER_ADMIN_PASSWORD"),
		AllowedOrigin:   os.Getenv("CONAN_MANAGER_ALLOWED_ORIGIN"),
		BackupRetention: integer("CONAN_MANAGER_BACKUP_RETENTION", 10),
		QueryHost:       env("CONAN_MANAGER_QUERY_HOST", "127.0.0.1"),
		Availability: watchdog.AvailabilityPolicy{
			Interval:       duration("CONAN_MANAGER_QUERY_INTERVAL", 15*time.Second),
			Timeout:        duration("CONAN_MANAGER_QUERY_TIMEOUT", 3*time.Second),
			StartupGrace:   duration("CONAN_MANAGER_QUERY_STARTUP_GRACE", 10*time.Minute),
			UnavailableFor: duration("CONAN_MANAGER_QUERY_UNAVAILABLE_FOR", 5*time.Minute),
		},
	}
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
