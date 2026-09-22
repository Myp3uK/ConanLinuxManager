package server

import (
	"regexp"
	"time"
)

type Runtime string

const (
	RuntimeNative Runtime = "native"
	RuntimeWine   Runtime = "wine"
	RuntimeProton Runtime = "proton"
)

type Engine string

const (
	EngineUE5 Engine = "ue5"
	EngineUE4 Engine = "ue4"
)

type Config struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Engine              Engine  `json:"engine"`
	Runtime             Runtime `json:"runtime"`
	InstallDir          string  `json:"install_dir"`
	SteamCMDDir         string  `json:"steamcmd_dir"`
	Branch              string  `json:"branch"`
	LaunchArgs          string  `json:"launch_args"`
	GamePort            int     `json:"game_port"`
	QueryPort           int     `json:"query_port"`
	RCONPort            *int    `json:"rcon_port,omitempty"`
	RamdiskEnabled      bool    `json:"ramdisk_enabled"`
	RamdiskPath         string  `json:"ramdisk_path"`
	RamdiskSizeMB       int     `json:"ramdisk_size_mb"`
	SyncIntervalSeconds int     `json:"sync_interval_seconds"`
	// PeriodicBackupIntervalSeconds is written into the game's Game.ini before
	// each start. Zero leaves the game's own value alone. Raising it is the
	// only configuration lever on backup write volume: every cycle copies the
	// whole database, so the interval divides that cost directly.
	PeriodicBackupIntervalSeconds int `json:"periodic_backup_interval_seconds"`
	// RestartOnCrash restarts a server that exits without being asked to, up to
	// a bounded number of attempts.
	RestartOnCrash bool `json:"restart_on_crash"`
	// DesiredState is what the operator last asked for. The manager restores it
	// after a host reboot; it is not changed by crashes or automatic restarts.
	DesiredState string `json:"desired_state"`
	// RecoveryReason is set when the world failed its check after a kill or a
	// crash. The server cannot start until an operator verifies it again.
	RecoveryReason string `json:"recovery_reason,omitempty"`
	// Hooks maps a hook event to the scripts, from the hook directory, that
	// run for it in order.
	Hooks map[string][]string `json:"hooks"`
	// AutoUpdate installs a new build as soon as Steam publishes one. The
	// server is stopped for it, so players are disconnected.
	AutoUpdate bool `json:"auto_update"`
	// Warnings are announced to players over RCON before a scheduled stop or
	// restart and an automatic update.
	Warnings Warnings `json:"warnings"`
	// ZombieCheckSeconds restarts a server whose query port has not answered
	// for this long after it finished loading; 0 turns the check off.
	ZombieCheckSeconds int `json:"zombie_check_seconds"`
	// RestartMinUptimeSeconds skips a scheduled restart of a server that has
	// run for less than this, such as one started minutes before.
	RestartMinUptimeSeconds int `json:"restart_min_uptime_seconds"`
	// Map is the game path of the map to load; empty loads the Exiled Lands.
	Map string `json:"map"`
	// Multihome binds the server to one address of a host with several.
	Multihome string `json:"multihome"`
	// BackupOnStop takes a manager backup each time the server stops cleanly.
	BackupOnStop bool `json:"backup_on_stop"`
	// DiscordMessages replace the standard Discord texts for this server.
	// Deprecated: Messages holds every text now; these are read once into it.
	DiscordMessages DiscordMessages `json:"discord_messages"`
	// Messages are the texts the manager announces, and where each goes.
	Messages  Messages  `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// validID names a systemd unit, a unit specification and a backup directory,
// so it is limited to characters that are safe in all three.
var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (c Config) Validate() error {
	if c.ID == "" || c.Name == "" || c.InstallDir == "" || c.SteamCMDDir == "" {
		return ErrInvalidConfig("id, name, install_dir and steamcmd_dir are required")
	}
	if !validID.MatchString(c.ID) {
		return ErrInvalidConfig("id must be 1-64 letters, digits, dashes or underscores, starting with a letter or digit")
	}
	if c.Engine != EngineUE4 && c.Engine != EngineUE5 {
		return ErrInvalidConfig("engine must be ue4 or ue5")
	}
	if c.Runtime != RuntimeNative && c.Runtime != RuntimeWine && c.Runtime != RuntimeProton {
		return ErrInvalidConfig("runtime must be native, wine or proton")
	}
	if c.GamePort < 1 || c.GamePort > 65535 || c.QueryPort < 1 || c.QueryPort > 65535 {
		return ErrInvalidConfig("game_port and query_port must be valid TCP/UDP ports")
	}
	if c.RCONPort != nil && (*c.RCONPort < 1 || *c.RCONPort > 65535) {
		return ErrInvalidConfig("rcon_port must be a valid port")
	}
	if c.RamdiskEnabled && (c.RamdiskPath == "" || c.RamdiskSizeMB < 128) {
		return ErrInvalidConfig("enabled ramdisk requires a path and at least 128 MB")
	}
	if err := c.validateOptions(); err != nil {
		return err
	}
	if err := c.Warnings.validate(); err != nil {
		return err
	}
	if err := validateHooks(c.Hooks); err != nil {
		return err
	}
	if c.SyncIntervalSeconds < 0 {
		return ErrInvalidConfig("sync_interval_seconds cannot be negative")
	}
	// A backup copies the entire database, so an interval shorter than a minute
	// buys nothing and risks overlapping cycles on a large world.
	if c.PeriodicBackupIntervalSeconds != 0 &&
		(c.PeriodicBackupIntervalSeconds < 60 || c.PeriodicBackupIntervalSeconds > 86400) {
		return ErrInvalidConfig("periodic_backup_interval_seconds must be 0 (unmanaged) or between 60 and 86400")
	}
	return nil
}

type ErrInvalidConfig string

func (e ErrInvalidConfig) Error() string { return string(e) }
