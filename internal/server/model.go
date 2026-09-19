package server

import "time"

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
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Engine              Engine    `json:"engine"`
	Runtime             Runtime   `json:"runtime"`
	InstallDir          string    `json:"install_dir"`
	SteamCMDDir         string    `json:"steamcmd_dir"`
	Branch              string    `json:"branch"`
	LaunchArgs          string    `json:"launch_args"`
	GamePort            int       `json:"game_port"`
	QueryPort           int       `json:"query_port"`
	RCONPort            *int      `json:"rcon_port,omitempty"`
	RamdiskEnabled      bool      `json:"ramdisk_enabled"`
	RamdiskPath         string    `json:"ramdisk_path"`
	RamdiskSizeMB       int       `json:"ramdisk_size_mb"`
	SyncIntervalSeconds int       `json:"sync_interval_seconds"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (c Config) Validate() error {
	if c.ID == "" || c.Name == "" || c.InstallDir == "" || c.SteamCMDDir == "" {
		return ErrInvalidConfig("id, name, install_dir and steamcmd_dir are required")
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
	if c.SyncIntervalSeconds < 0 {
		return ErrInvalidConfig("sync_interval_seconds cannot be negative")
	}
	return nil
}

type ErrInvalidConfig string

func (e ErrInvalidConfig) Error() string { return string(e) }
