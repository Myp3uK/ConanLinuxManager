package server

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

// WarningStep is one announcement: Message is shown to every player Minutes
// before the server goes down.
type WarningStep struct {
	Minutes int    `json:"minutes"`
	Message string `json:"message"`
}

// Warnings tell players over RCON that the server is about to go down for a
// scheduled stop or restart, or an automatic update.
type Warnings struct {
	// Steps are announced longest first. Empty disables warnings.
	Steps []WarningStep `json:"steps"`
	// Command is the console command that shows a message to every player.
	Command string `json:"command,omitempty"`
}

// DefaultWarningCommand is Conan's console command for a server-wide message.
const DefaultWarningCommand = "broadcast"

// DefaultWarningMessage is used for a step without its own text. {action}
// becomes what is about to happen, {minutes} the time left.
const DefaultWarningMessage = "Внимание! Сервер будет {action} через {minutes} мин."

// DefaultWarnings are what an operator starts from: the same marks the
// Dedicated Server Launcher offers.
func DefaultWarnings() Warnings {
	return Warnings{Command: DefaultWarningCommand, Steps: []WarningStep{
		{Minutes: 10, Message: DefaultWarningMessage},
		{Minutes: 5, Message: DefaultWarningMessage},
		{Minutes: 2, Message: DefaultWarningMessage},
	}}
}

var actionWords = map[string]string{"stop": "остановлен", "restart": "перезапущен", "update": "обновлён"}

// maxWarningMinutes keeps a typo from holding a restart for days.
const maxWarningMinutes = 120

// Enabled reports whether any warning is configured.
func (w Warnings) Enabled() bool { return len(w.Steps) > 0 }

// Marks returns the minutes in the order they are announced, longest first.
func (w Warnings) Marks() []int {
	marks := make([]int, 0, len(w.Steps))
	for _, s := range w.Steps {
		marks = append(marks, s.Minutes)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(marks)))
	return marks
}

// Message is the text announced for an action with minutes left.
func (w Warnings) Message(action string, minutes int) string {
	template := ""
	for _, s := range w.Steps {
		if s.Minutes == minutes {
			template = s.Message
		}
	}
	if strings.TrimSpace(template) == "" {
		template = DefaultWarningMessage
	}
	word := actionWords[action]
	if word == "" {
		word = action
	}
	return strings.NewReplacer("{minutes}", fmt.Sprint(minutes), "{action}", word).Replace(template)
}

// CommandLine is the full console command announcing a message.
func (w Warnings) CommandLine(message string) string {
	command := w.Command
	if command == "" {
		command = DefaultWarningCommand
	}
	return command + " " + message
}

// UnmarshalJSON also reads the first format, which had one list of minutes
// and a text per action, so warnings saved by an earlier version survive.
func (w *Warnings) UnmarshalJSON(data []byte) error {
	var raw struct {
		Steps    []WarningStep     `json:"steps"`
		Command  string            `json:"command"`
		Minutes  []int             `json:"minutes"`
		Messages map[string]string `json:"messages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	w.Steps, w.Command = raw.Steps, raw.Command
	if len(w.Steps) == 0 && len(raw.Minutes) > 0 {
		for _, m := range raw.Minutes {
			w.Steps = append(w.Steps, WarningStep{Minutes: m, Message: raw.Messages["restart"]})
		}
	}
	return nil
}

func (w Warnings) validate() error {
	seen := map[int]bool{}
	for _, s := range w.Steps {
		if s.Minutes < 1 || s.Minutes > maxWarningMinutes {
			return ErrInvalidConfig(fmt.Sprintf("warning minutes must be between 1 and %d", maxWarningMinutes))
		}
		if seen[s.Minutes] {
			return ErrInvalidConfig(fmt.Sprintf("warning minute %d is listed twice", s.Minutes))
		}
		seen[s.Minutes] = true
		if strings.ContainsAny(s.Message, "\r\n\x00") || len(s.Message) > 1000 {
			return ErrInvalidConfig("a warning message must be one line of at most 1000 bytes")
		}
	}
	if strings.ContainsAny(w.Command, " \t\r\n\x00") || len(w.Command) > 64 {
		return ErrInvalidConfig("warning command must be a single word")
	}
	return nil
}

func encodeWarnings(w Warnings) (string, error) {
	data, err := json.Marshal(w)
	return string(data), err
}

func decodeWarnings(raw string) Warnings {
	var w Warnings
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &w)
	}
	return w
}

// DiscordMessages are the operator's own texts for the announcements the
// Dedicated Server Launcher sends to Discord. Empty keeps the standard text.
type DiscordMessages struct {
	Ready     string `json:"ready,omitempty"`
	Restarted string `json:"restarted,omitempty"`
	Stopped   string `json:"stopped,omitempty"`
}

// options are the settings stored together in the options column.
type options struct {
	ZombieCheckSeconds      int             `json:"zombie_check_seconds,omitempty"`
	RestartMinUptimeSeconds int             `json:"restart_min_uptime_seconds,omitempty"`
	Map                     string          `json:"map,omitempty"`
	Multihome               string          `json:"multihome,omitempty"`
	BackupOnStop            bool            `json:"backup_on_stop,omitempty"`
	DiscordMessages         DiscordMessages `json:"discord_messages,omitempty"`
}

func encodeOptions(c Config) (string, error) {
	data, err := json.Marshal(options{
		ZombieCheckSeconds: c.ZombieCheckSeconds, RestartMinUptimeSeconds: c.RestartMinUptimeSeconds,
		Map: c.Map, Multihome: c.Multihome, BackupOnStop: c.BackupOnStop, DiscordMessages: c.DiscordMessages,
	})
	return string(data), err
}

func decodeOptions(raw string, c *Config) {
	var o options
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &o)
	}
	c.ZombieCheckSeconds, c.RestartMinUptimeSeconds = o.ZombieCheckSeconds, o.RestartMinUptimeSeconds
	c.Map, c.Multihome, c.BackupOnStop, c.DiscordMessages = o.Map, o.Multihome, o.BackupOnStop, o.DiscordMessages
}

// validMap is an Unreal package path: the game takes it as its first argument.
var validMap = regexp.MustCompile(`^/Game/[A-Za-z0-9_/]{1,200}$`)

func (c Config) validateOptions() error {
	if c.ZombieCheckSeconds != 0 && (c.ZombieCheckSeconds < 60 || c.ZombieCheckSeconds > 3600) {
		return ErrInvalidConfig("zombie_check_seconds must be 0 (off) or between 60 and 3600")
	}
	if c.RestartMinUptimeSeconds < 0 || c.RestartMinUptimeSeconds > 7*86400 {
		return ErrInvalidConfig("restart_min_uptime_seconds must be between 0 and 604800")
	}
	if c.Map != "" && !validMap.MatchString(c.Map) {
		return ErrInvalidConfig("map must be a game path such as /Game/Maps/ConanSandbox/ConanSandbox")
	}
	if c.Multihome != "" && net.ParseIP(c.Multihome) == nil {
		return ErrInvalidConfig("multihome must be an IP address")
	}
	for _, text := range []string{c.DiscordMessages.Ready, c.DiscordMessages.Restarted, c.DiscordMessages.Stopped} {
		if len(text) > 1500 {
			return ErrInvalidConfig("a Discord message must be at most 1500 bytes")
		}
	}
	return nil
}
