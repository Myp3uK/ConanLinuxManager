package server

import (
	"regexp"
	"strings"
	"unicode"
)

// Message kinds: what the manager announces, and where. Every kind has a
// text the operator can change; game kinds are said over RCON, Discord kinds
// go to the channels that take them.
const (
	MsgScheduledRestart = "scheduled_restart"
	MsgDelayedRestart   = "delayed_restart"
	MsgRestartCancelled = "restart_cancelled"
	MsgUpdate           = "update"
	MsgStarted          = "started"
	MsgManualRestart    = "manual_restart"
	MsgManualStop       = "manual_stop"
	MsgCrash            = "crash"
	MsgTechnical        = "technical"
	MsgAttention        = "attention"
)

// Message is one kind's text and where it goes.
type Message struct {
	Text    string `json:"text"`
	Game    bool   `json:"game"`
	Discord bool   `json:"discord"`
}

// Messages are a server's messages by kind; a missing kind uses its default.
type Messages map[string]Message

// MessageKind describes a kind for the settings form.
type MessageKind struct {
	Key   string  `json:"key"`
	Title string  `json:"title"`
	Help  string  `json:"help"`
	Group string  `json:"group"`
	Vars  string  `json:"vars"`
	Game  bool    `json:"game_allowed"`
	Def   Message `json:"default"`
}

// MessageKinds lists every kind with its default, in the form's order.
var MessageKinds = []MessageKind{
	{Key: MsgScheduledRestart, Group: "players", Title: "Рестарт по расписанию", Game: true, Vars: "{minutes} {name} {reason}",
		Help: "Уходит за каждый интервал предупреждений до планового рестарта или остановки и до отложенного рестарта (по кнопке или ради обновления). Рестарт - ровно в назначенное время. {reason} - «обновление» у рестарта ради обновления, у остальных пусто.",
		Def:  Message{Text: "Плановый рестарт сервера через {minutes} мин.", Game: true, Discord: true}},
	{Key: MsgDelayedRestart, Group: "players", Title: "Отложенный рестарт", Game: true, Vars: "{minutes} {name}",
		Help: "Кнопка «Отложенный рестарт»: уходит сразу, с полным сроком. Дальше отсчёт идёт сообщением «Рестарт по расписанию».",
		Def:  Message{Text: "Технический рестарт через {minutes} мин.", Game: true, Discord: true}},
	{Key: MsgRestartCancelled, Group: "players", Title: "Отмена отложенного рестарта", Game: true, Vars: "{name}",
		Help: "Отложенный рестарт отменён кнопкой «Отменить рестарт».",
		Def:  Message{Text: "Технический рестарт отменён.", Game: true, Discord: true}},
	{Key: MsgUpdate, Group: "players", Title: "Найдено обновление", Game: true, Vars: "{target} {version} {minutes} {name}",
		Help: "Один раз, когда в Steam вышла новая сборка. С автообновлением и запущенным сервером менеджер тут же ставит отложенный рестарт, и {minutes} - сколько до него; если плановый рестарт наступит раньше, обновление ставится при нём, и {minutes} - сколько до планового. Дальше отсчёт идёт сообщением «Рестарт по расписанию». {target} - «игры» или «мода», {version} - сборка или список модов.",
		Def:  Message{Text: "Найдено обновление {target}: {version}.[ Сервер будет обновлён через {minutes} мин.]", Game: true, Discord: true}},
	{Key: MsgStarted, Group: "players", Title: "Сервер запущен", Vars: "{name}",
		Help: "Сервер загрузился и пускает игроков. Только в Discord: в игре в этот момент ещё никого нет.",
		Def:  Message{Text: "Сервер {name} запущен и ждёт игроков.", Discord: true}},
	{Key: MsgManualRestart, Group: "players", Title: "Рестарт по кнопке", Game: true, Vars: "{minutes} {name}",
		Help: "Кнопка «Рестарт»: один раз, и сервер сразу перезапускается.",
		Def:  Message{Text: "Администратор перезапускает сервер[ через {minutes} мин.]", Game: true, Discord: true}},
	{Key: MsgManualStop, Group: "players", Title: "Остановка по кнопке", Game: true, Vars: "{minutes} {name}",
		Help: "Кнопка «Остановить»: один раз, и сервер сразу останавливается. Кнопка «Убить» шлёт это же сообщение, но только в Discord: зависший сервер его уже не покажет.",
		Def:  Message{Text: "Администратор выключает сервер[ через {minutes} мин.]", Game: true, Discord: true}},
	{Key: MsgCrash, Group: "admins", Title: "Сервер упал", Vars: "{name} {reason}",
		Help: "Процесс завершился сам. К сообщению прикладываются строки из лога игры с ошибкой. Упоминание роли ставится к этому и следующим двум.",
		Def:  Message{Text: "Сервер {name} упал.", Discord: true}},
	{Key: MsgTechnical, Group: "admins", Title: "Технический рестарт", Vars: "{name} {reason}",
		Help: "Менеджер сам поднимает сервер: после падения, зависания или проверки на зомби.",
		Def:  Message{Text: "Технический рестарт сервера {name}: {reason}", Discord: true}},
	{Key: MsgAttention, Group: "admins", Title: "Нужно вмешательство", Vars: "{name} {reason}",
		Help: "Сервер остался выключенным и сам не поднимется: падает раз за разом, не запускается, повреждена база, не удалось обновление или бэкап.",
		Def:  Message{Text: "Серверу {name} нужно внимание: {reason}", Discord: true}},
}

// Kind returns the description of a kind.
func Kind(key string) (MessageKind, bool) {
	for _, k := range MessageKinds {
		if k.Key == key {
			return k, true
		}
	}
	return MessageKind{}, false
}

// Get is a kind's message, the default where the server has none. A kind
// that cannot be said in the game never is.
func (m Messages) Get(key string) Message {
	kind, ok := Kind(key)
	msg, set := m[key]
	if !set {
		msg = kind.Def
	}
	if strings.TrimSpace(msg.Text) == "" {
		msg.Text = kind.Def.Text
	}
	if ok && !kind.Game {
		msg.Game = false
	}
	return msg
}

var (
	optionalPart = regexp.MustCompile(`\[([^\[\]]*)\]`)
	placeholder  = regexp.MustCompile(`\{([a-z]+)\}`)
)

// Render fills a text. A part in square brackets is kept only when every
// placeholder in it has a value, so one text reads right with and without,
// say, the minutes left.
func Render(text string, vars map[string]string) string {
	text = optionalPart.ReplaceAllStringFunc(text, func(part string) string {
		inner := part[1 : len(part)-1]
		for _, m := range placeholder.FindAllStringSubmatch(inner, -1) {
			if vars[m[1]] == "" {
				return ""
			}
		}
		return inner
	})
	text = placeholder.ReplaceAllStringFunc(text, func(p string) string {
		if value, ok := vars[p[1:len(p)-1]]; ok {
			return value
		}
		return p
	})
	return strings.Join(strings.Fields(text), " ")
}

// shortcode is a Discord emoji written as text, such as :warning:.
var shortcode = regexp.MustCompile(`:[a-z][a-z0-9_+-]{1,39}:`)

// GameText is a message as the game can show it. The game's font has no
// emoji, which it draws as question marks, so they are left out, together
// with Discord's :shortcodes:. One text then reads well in both places.
func GameText(text string) string {
	text = shortcode.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		switch {
		case r > 0xFFFF, r == 0x200D, r == 0x20E3, r >= 0xFE00 && r <= 0xFE0F:
			return -1
		case r >= 0x2300 && unicode.Is(unicode.So, r):
			return -1
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func (m Messages) validate() error {
	for key, msg := range m {
		if _, ok := Kind(key); !ok {
			return ErrInvalidConfig("unknown message kind " + key)
		}
		if strings.ContainsAny(msg.Text, "\r\n\x00") || len(msg.Text) > 1000 {
			return ErrInvalidConfig("a message must be one line of at most 1000 bytes")
		}
	}
	return nil
}
