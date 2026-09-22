package notify

import (
	"github.com/example/conan-manager/internal/events"
	"github.com/example/conan-manager/internal/server"
)

// Category is what the channel form offers: one per message kind. A channel
// stores the event types of the categories it takes.
type Category struct {
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Help     string   `json:"help"`
	Audience string   `json:"audience"`
	Types    []string `json:"types"`
}

// kindEvents are the events that carry each message kind.
var kindEvents = map[string][]events.Type{
	server.MsgScheduledRestart: {events.RestartWarning},
	server.MsgDelayedRestart:   {events.RestartDelayed},
	server.MsgRestartCancelled: {events.RestartCancelled},
	server.MsgUpdate:           {events.UpdateAvailable},
	server.MsgStarted:          {events.ServerReady},
	server.MsgManualRestart:    {events.RestartManual},
	server.MsgManualStop:       {events.StopManual},
	server.MsgCrash:            {events.ServerCrashed},
	server.MsgTechnical:        {events.ServerRestarting, events.ServerHang},
	server.MsgAttention: {events.ServerRestartAbandoned, events.ServerStartFailed, events.ServerRecovery,
		events.DatabaseCorrupt, events.UpdateFailed, events.BackupFailed},
}

// Categories lists the kinds of message a channel can take.
func Categories() []Category {
	list := make([]Category, 0, len(server.MessageKinds))
	for _, k := range server.MessageKinds {
		var types []string
		for _, t := range kindEvents[k.Key] {
			types = append(types, string(t))
		}
		list = append(list, Category{Key: k.Key, Title: k.Title, Help: k.Help, Audience: k.Group, Types: types})
	}
	return list
}
