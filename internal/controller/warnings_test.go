package controller

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

type warnRun struct {
	online []int // players seen at each check; the last value repeats
	fail   error
	said   []string
	waited []time.Duration
}

// say records the minutes of each message: 0 is the single message without
// a countdown.
func (r *warnRun) run(marks []int) error {
	checks := 0
	count := func() (int, error) {
		i := min(checks, len(r.online)-1)
		checks++
		return r.online[i], r.fail
	}
	say := func(minutes int) error {
		r.said = append(r.said, strconv.Itoa(minutes))
		return nil
	}
	wait := func(_ context.Context, d time.Duration) error { r.waited = append(r.waited, d); return nil }
	return announce(context.Background(), marks, count, say, wait)
}

func TestManualWarningsCountDown(t *testing.T) {
	r := &warnRun{online: []int{3}}
	if err := r.run([]int{15, 5, 1}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"15", "5", "1"}; !reflect.DeepEqual(r.said, want) {
		t.Fatalf("said %q", r.said)
	}
	if want := []time.Duration{10 * time.Minute, 4 * time.Minute, time.Minute}; !reflect.DeepEqual(r.waited, want) {
		t.Fatalf("waited %v", r.waited)
	}
}

// With nobody online the action is not delayed; one message still goes out,
// in the game too.
func TestNobodyOnlineMeansOneMessageAndNoWait(t *testing.T) {
	r := &warnRun{online: []int{0}}
	if err := r.run([]int{10, 1}); err != nil || !reflect.DeepEqual(r.said, []string{"0"}) || len(r.waited) != 0 {
		t.Fatalf("said %q waited %v %v", r.said, r.waited, err)
	}
}

func TestWithoutCountdownOneMessageToThePlayers(t *testing.T) {
	r := &warnRun{online: []int{2}}
	if err := r.run(nil); err != nil || !reflect.DeepEqual(r.said, []string{"0"}) || len(r.waited) != 0 {
		t.Fatalf("said %q waited %v %v", r.said, r.waited, err)
	}
}

func TestWarningsEndWhenTheLastPlayerLeaves(t *testing.T) {
	r := &warnRun{online: []int{2, 0}}
	if err := r.run([]int{10, 5, 1}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.said, []string{"10"}) || len(r.waited) != 1 {
		t.Fatalf("said %q waited %v", r.said, r.waited)
	}
}

func TestAnnounceReportsRconErrors(t *testing.T) {
	boom := errors.New("connection refused")
	r := &warnRun{online: []int{0}, fail: boom}
	if err := r.run([]int{5}); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

// A scheduled action warns its minutes before the set time and happens at
// that time, whether anyone is online or not.
func TestCountdownKeepsTheActionOnTime(t *testing.T) {
	due := time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		start time.Duration
		fail  error
		said  []string
	}{
		{"on time", -15 * time.Minute, nil, []string{"15@05:45", "10@05:50", "5@05:55"}},
		{"claimed late", -7 * time.Minute, nil, []string{"5@05:55"}},
		{"rcon down", -15 * time.Minute, errors.New("rcon down"), []string{"15@05:45", "10@05:50", "5@05:55"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := due.Add(tc.start)
			var said []string
			err := countdown(context.Background(), []int{15, 10, 5}, due, func() time.Time { return clock },
				func(minutes int) error {
					said = append(said, strconv.Itoa(minutes)+"@"+clock.Format("15:04"))
					return tc.fail
				},
				func(_ context.Context, d time.Duration) error { clock = clock.Add(d); return nil })
			if !clock.Equal(due) {
				t.Fatalf("returned at %s, want exactly %s", clock.Format("15:04:05"), due.Format("15:04:05"))
			}
			if !reflect.DeepEqual(said, tc.said) {
				t.Fatalf("said %v, want %v", said, tc.said)
			}
			if (err != nil) != (tc.fail != nil) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRenderDropsPartsWithoutValues(t *testing.T) {
	text := "Администратор перезапускает сервер[ через {minutes} мин.]"
	if got := server.Render(text, map[string]string{"minutes": "5"}); got != "Администратор перезапускает сервер через 5 мин." {
		t.Fatalf("%q", got)
	}
	if got := server.Render(text, nil); got != "Администратор перезапускает сервер" {
		t.Fatalf("%q", got)
	}
	if got := server.Render("Администратор перезапускает сервер [ через {minutes} мин.]", map[string]string{"minutes": "15"}); got != "Администратор перезапускает сервер через 15 мин." {
		t.Fatalf("%q", got)
	}
	if got := server.Render("Найдено обновление {target}: {version}. {unknown}", map[string]string{"target": "игры", "version": "сборка 7"}); got != "Найдено обновление игры: сборка 7. {unknown}" {
		t.Fatalf("%q", got)
	}
}

func TestMessagesFallBackToDefaults(t *testing.T) {
	m := server.Messages{server.MsgStarted: {Text: "Готово", Game: true, Discord: true}}
	if got := m.Get(server.MsgStarted); got.Text != "Готово" || got.Game {
		t.Fatalf("started may not go into the game: %+v", got)
	}
	if got := m.Get(server.MsgManualStop); !got.Game || !got.Discord || got.Text == "" {
		t.Fatalf("default: %+v", got)
	}
}
