package notify

import "github.com/example/conan-manager/internal/events"

// EventInfo describes an event for the channel form.
type EventInfo struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Group    string `json:"group"`
	Severity string `json:"severity"`
}

var catalogGroups = []struct {
	name  string
	types []events.Type
}{
	{"Работа сервера", []events.Type{events.ServerReady, events.ServerRestarted, events.ServerStopping, events.ServerStopped, events.ServerStarted}},
	{"Проблемы", []events.Type{events.ServerCrashed, events.ServerRestarting, events.ServerRestartAbandoned, events.ServerStartFailed,
		events.ServerHang, events.ServerUnavailable, events.ServerWatchdogStop, events.ServerKilled, events.ServerRecovery,
		events.DatabaseCorrupt, events.RconFailed}},
	{"Обновления", []events.Type{events.UpdateAvailable, events.UpdateStarted, events.UpdateFinished, events.UpdateFailed}},
	{"Бэкапы", []events.Type{events.BackupStarted, events.BackupFinished, events.BackupFailed, events.BackupRestored, events.DatabaseVerified}},
	{"Расписание и прочее", []events.Type{events.PlayersWarned, events.ScheduleSkipped, events.ScheduleFailed, events.HookFailed, events.ConfigChanged}},
}

// Catalog lists the events a channel can subscribe to, grouped for the form.
func Catalog() []EventInfo {
	var list []EventInfo
	for _, g := range catalogGroups {
		for _, t := range g.types {
			list = append(list, EventInfo{Type: string(t), Title: titles[t].ru, Group: g.name, Severity: string(events.SeverityOf(t))})
		}
	}
	return list
}
