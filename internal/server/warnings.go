package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Warnings tell players over RCON that the server is about to go down for a
// scheduled stop or restart, or an automatic update.
type Warnings struct {
	// Minutes are the marks, before the action, at which players are told,
	// for example [15, 5, 1]. Empty disables warnings.
	Minutes []int `json:"minutes"`
	// Messages holds a template per action (stop, restart, update); {minutes}
	// is replaced by the time left. A missing template uses the default.
	Messages map[string]string `json:"messages,omitempty"`
	// Command is the console command that shows a message to every player.
	Command string `json:"command,omitempty"`
}

// DefaultWarningCommand is Conan's console command for a server-wide message.
const DefaultWarningCommand = "broadcast"

var defaultWarningMessages = map[string]string{
	"stop":    "Сервер будет остановлен через {minutes} мин. / Server stops in {minutes} min.",
	"restart": "Сервер будет перезапущен через {minutes} мин. / Server restarts in {minutes} min.",
	"update":  "Сервер будет обновлён через {minutes} мин. / Server updates in {minutes} min.",
}

// maxWarningMinutes keeps a typo from holding a restart for days.
const maxWarningMinutes = 120

// Enabled reports whether any warning is configured.
func (w Warnings) Enabled() bool { return len(w.Minutes) > 0 }

// Marks returns the minutes in the order they are announced, longest first.
func (w Warnings) Marks() []int {
	marks := append([]int(nil), w.Minutes...)
	sort.Sort(sort.Reverse(sort.IntSlice(marks)))
	return marks
}

// Message is the text announced for an action with minutes left.
func (w Warnings) Message(action string, minutes int) string {
	template := w.Messages[action]
	if template == "" {
		template = defaultWarningMessages[action]
	}
	return strings.ReplaceAll(template, "{minutes}", fmt.Sprint(minutes))
}

// CommandLine is the full console command announcing a message.
func (w Warnings) CommandLine(message string) string {
	command := w.Command
	if command == "" {
		command = DefaultWarningCommand
	}
	return command + " " + message
}

func (w Warnings) validate() error {
	seen := map[int]bool{}
	for _, m := range w.Minutes {
		if m < 1 || m > maxWarningMinutes {
			return ErrInvalidConfig(fmt.Sprintf("warning minutes must be between 1 and %d", maxWarningMinutes))
		}
		if seen[m] {
			return ErrInvalidConfig(fmt.Sprintf("warning minute %d is listed twice", m))
		}
		seen[m] = true
	}
	for action, text := range w.Messages {
		if defaultWarningMessages[action] == "" {
			return ErrInvalidConfig(fmt.Sprintf("unknown warning action %q (use stop, restart or update)", action))
		}
		if strings.ContainsAny(text, "\r\n\x00") || len(text) > 1000 {
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
