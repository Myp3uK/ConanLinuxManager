package server

import (
	"encoding/json"
	"testing"
)

func TestWarningsValidation(t *testing.T) {
	if err := DefaultWarnings().validate(); err != nil {
		t.Fatal(err)
	}
	for name, w := range map[string]Warnings{
		"zero minutes": {Steps: []WarningStep{{Minutes: 0}}},
		"too long":     {Steps: []WarningStep{{Minutes: 500}}},
		"duplicate":    {Steps: []WarningStep{{Minutes: 5}, {Minutes: 5}}},
		"two lines":    {Steps: []WarningStep{{Minutes: 5, Message: "a\nexit"}}},
		"command args": {Steps: []WarningStep{{Minutes: 5}}, Command: "broadcast now"},
	} {
		if w.validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestWarningMessagesFillActionAndMinutes(t *testing.T) {
	w := Warnings{Steps: []WarningStep{{Minutes: 10, Message: "Рестарт через {minutes} мин"}, {Minutes: 2}}}
	if got := w.Message("restart", 10); got != "Рестарт через 10 мин" {
		t.Fatalf("%q", got)
	}
	if got := w.Message("stop", 2); got != "Внимание! Сервер будет остановлен через 2 мин." {
		t.Fatalf("step without text: %q", got)
	}
	if got := w.Marks(); len(got) != 2 || got[0] != 10 {
		t.Fatalf("marks %v", got)
	}
}

// Warnings saved by the first version had a list of minutes and a text per
// action; they must still load.
func TestWarningsReadTheFirstFormat(t *testing.T) {
	var w Warnings
	if err := json.Unmarshal([]byte(`{"minutes":[2,1],"messages":{"restart":"Рестарт через {minutes} мин."}}`), &w); err != nil {
		t.Fatal(err)
	}
	if len(w.Steps) != 2 || w.Message("restart", 1) != "Рестарт через 1 мин." {
		t.Fatalf("%+v", w)
	}
}

func TestOptionsAreValidated(t *testing.T) {
	base := Config{ID: "a", Name: "a", Engine: EngineUE5, Runtime: RuntimeNative, InstallDir: "/srv/a", SteamCMDDir: "/srv/s", GamePort: 7777, QueryPort: 27015}
	good := base
	good.ZombieCheckSeconds, good.RestartMinUptimeSeconds = 300, 7200
	good.Map, good.Multihome = "/Game/Maps/ConanSandbox/ConanSandbox", "192.168.0.100"
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"zombie too short": func(c *Config) { c.ZombieCheckSeconds = 10 },
		"map with spaces":  func(c *Config) { c.Map = "/Game/Maps/x -log" },
		"map not a path":   func(c *Config) { c.Map = "ConanSandbox" },
		"multihome name":   func(c *Config) { c.Multihome = "example.com" },
	} {
		c := base
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
