package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/server"
)

type warnRun struct {
	online []int // players seen at each check; the last value repeats
	said   []string
	waited []time.Duration
}

func (r *warnRun) run(w server.Warnings, action string) error {
	checks := 0
	count := func() (int, error) {
		i := checks
		if i >= len(r.online) {
			i = len(r.online) - 1
		}
		checks++
		return r.online[i], nil
	}
	say := func(text string) error { r.said = append(r.said, text); return nil }
	wait := func(_ context.Context, d time.Duration) error { r.waited = append(r.waited, d); return nil }
	return announce(context.Background(), w, action, count, say, wait)
}

func TestWarningsCountDownToTheAction(t *testing.T) {
	w := server.Warnings{Steps: []server.WarningStep{{Minutes: 1, Message: "Рестарт через {minutes} мин"}, {Minutes: 15, Message: "Рестарт через {minutes} мин"}, {Minutes: 5, Message: "Рестарт через {minutes} мин"}}}
	r := &warnRun{online: []int{3}}
	if err := r.run(w, "restart"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Рестарт через 15 мин", "Рестарт через 5 мин", "Рестарт через 1 мин"}; !reflect.DeepEqual(r.said, want) {
		t.Fatalf("said %q", r.said)
	}
	if want := []time.Duration{10 * time.Minute, 4 * time.Minute, time.Minute}; !reflect.DeepEqual(r.waited, want) {
		t.Fatalf("waited %v", r.waited)
	}
}

func TestNobodyOnlineMeansNoWait(t *testing.T) {
	r := &warnRun{online: []int{0}}
	if err := r.run(server.Warnings{Steps: []server.WarningStep{{Minutes: 10}, {Minutes: 1}}}, "stop"); err != nil || len(r.said) != 0 || len(r.waited) != 0 {
		t.Fatalf("said %q waited %v %v", r.said, r.waited, err)
	}
}

func TestWarningsEndWhenTheLastPlayerLeaves(t *testing.T) {
	r := &warnRun{online: []int{2, 0}}
	if err := r.run(server.Warnings{Steps: []server.WarningStep{{Minutes: 10}, {Minutes: 5}, {Minutes: 1}}}, "update"); err != nil {
		t.Fatal(err)
	}
	if len(r.said) != 1 || len(r.waited) != 1 {
		t.Fatalf("said %q waited %v", r.said, r.waited)
	}
}

func TestWarningCommandLine(t *testing.T) {
	w := server.Warnings{}
	if got := w.CommandLine(w.Message("stop", 5)); got != "broadcast Внимание! Сервер будет остановлен через 5 мин." {
		t.Fatalf("%q", got)
	}
}

func TestAnnounceReportsRconErrors(t *testing.T) {
	boom := errors.New("connection refused")
	err := announce(context.Background(), server.Warnings{Steps: []server.WarningStep{{Minutes: 5}}}, "stop",
		func() (int, error) { return 0, boom }, nil, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}
