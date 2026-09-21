package rcon

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Section is where Conan Exiles reads its RCON settings, in Game.ini.
const Section = "RconPlugin"

// Settings are the RCON keys of Game.ini.
type Settings struct {
	Enabled  bool
	Port     int
	Password string
}

// Address is where the manager reaches the server: RCON listens on every
// interface, and the manager runs on the same host.
func (s Settings) Address() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port))
}

// Usable says why the settings cannot be used, or returns nil.
func (s Settings) Usable() error {
	switch {
	case !s.Enabled:
		return fmt.Errorf("RCON is disabled: set RconEnabled=True in [%s] of Game.ini", Section)
	case s.Port <= 0 || s.Port > 65535:
		return fmt.Errorf("RCON port is not set: set RconPort in [%s] of Game.ini", Section)
	case s.Password == "":
		return fmt.Errorf("RCON password is empty: set RconPassword in [%s] of Game.ini", Section)
	}
	return nil
}

// FromINI reads the settings from the entries of the [RconPlugin] section.
// Keys are matched without regard to case, as Unreal does.
func FromINI(entries map[string]string) Settings {
	var s Settings
	for key, value := range entries {
		value = strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "rconenabled":
			s.Enabled = strings.EqualFold(value, "true") || value == "1"
		case "rconport":
			s.Port, _ = strconv.Atoi(value)
		case "rconpassword":
			s.Password = value
		}
	}
	return s
}
