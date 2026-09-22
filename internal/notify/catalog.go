package notify

import "github.com/example/conan-manager/internal/events"

// Category is what the channel form offers: a few kinds of message instead
// of every event the manager knows. A channel stores the event types of the
// categories it takes.
type Category struct {
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Help     string   `json:"help"`
	Audience string   `json:"audience"`
	Types    []string `json:"types"`
}

var categories = []struct {
	key, title, help, audience string
	types                      []events.Type
}{
	{"start", "Сервер запущен", "Сервер загрузился и пускает игроков.", "players",
		[]events.Type{events.ServerReady}},
	{"restart", "Рестарты", "Предупреждения «сервер будет перезапущен через N мин.» и сообщение, что рестарт прошёл. Плановые и ручные.", "players",
		[]events.Type{events.PlayersWarned, events.ServerRestarted}},
	{"update", "Обновления", "Сервер уходит на обновление и обновление установлено.", "players",
		[]events.Type{events.UpdateStarted, events.UpdateFinished}},
	{"technical", "Технический рестарт", "Менеджер сам перезапускает сервер: после падения, зависания или проверки на зомби.", "players",
		[]events.Type{events.ServerRestarting, events.ServerHang}},
	{"crash", "Сервер упал", "С причиной из лога игры. Упоминание (если задано) ставится к этому сообщению.", "admins",
		[]events.Type{events.ServerCrashed}},
	{"attention", "Нужно вмешательство", "Сервер остался выключенным и сам не поднимется: падает раз за разом, не запускается, повреждена база, не удалось обновление или бэкап.", "admins",
		[]events.Type{events.ServerRestartAbandoned, events.ServerStartFailed, events.ServerRecovery, events.DatabaseCorrupt, events.UpdateFailed, events.BackupFailed}},
}

// Categories lists the kinds of message a channel can take.
func Categories() []Category {
	list := make([]Category, 0, len(categories))
	for _, c := range categories {
		types := make([]string, len(c.types))
		for i, t := range c.types {
			types[i] = string(t)
		}
		list = append(list, Category{Key: c.key, Title: c.title, Help: c.help, Audience: c.audience, Types: types})
	}
	return list
}
