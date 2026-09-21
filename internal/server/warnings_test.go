package server

import "testing"

func TestWarningsValidation(t *testing.T) {
	good := Warnings{Minutes: []int{15, 5, 1}, Messages: map[string]string{"restart": "Рестарт через {minutes} мин"}, Command: "broadcast"}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	for name, w := range map[string]Warnings{
		"zero minutes":   {Minutes: []int{0}},
		"too long":       {Minutes: []int{500}},
		"duplicate":      {Minutes: []int{5, 5}},
		"unknown action": {Minutes: []int{5}, Messages: map[string]string{"reboot": "x"}},
		"two lines":      {Minutes: []int{5}, Messages: map[string]string{"stop": "a\nexit"}},
		"command args":   {Minutes: []int{5}, Command: "broadcast now"},
	} {
		if w.validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
