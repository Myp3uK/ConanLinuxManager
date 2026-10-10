package controller

import (
	"context"
	"strings"

	"github.com/Myp3uK/ConanLinuxManager/internal/events"
	"github.com/Myp3uK/ConanLinuxManager/internal/server"
)

// messageEvent is the event that carries each announced kind to Discord.
var messageEvent = map[string]events.Type{
	server.MsgScheduledRestart: events.RestartWarning,
	server.MsgDelayedRestart:   events.RestartDelayed,
	server.MsgRestartCancelled: events.RestartCancelled,
	server.MsgUpdate:           events.UpdateAvailable,
	server.MsgManualRestart:    events.RestartManual,
	server.MsgManualStop:       events.StopManual,
}

// attentionReasons say, for the operator, why a server stays down.
var attentionReasons = map[events.Type]string{
	events.ServerRestartAbandoned: "падает раз за разом, автоперезапуск прекращён",
	events.ServerStartFailed:      "не запускается",
	events.ServerRecovery:         "нужно восстановление базы",
	events.DatabaseCorrupt:        "база мира повреждена",
	events.UpdateFailed:           "обновление не удалось",
	events.InstallFailed:          "установка не удалась",
	events.BackupFailed:           "бэкап не удался",
}

// kindOf tells which message kind an emitted event is, and why, for the
// events that are not sent through message.
func kindOf(t events.Type, details map[string]string) (string, string) {
	switch t {
	case events.ServerReady:
		return server.MsgStarted, ""
	case events.ServerCrashed:
		return server.MsgCrash, details["exit"]
	case events.ServerRestarting:
		return server.MsgTechnical, "после падения"
	case events.ServerHang:
		if strings.HasPrefix(details["reason"], "zombie:") {
			return server.MsgTechnical, "сервер перестал отвечать (проверка на зомби)"
		}
		return server.MsgTechnical, "завис игровой поток"
	}
	if reason, ok := attentionReasons[t]; ok {
		return server.MsgAttention, reason
	}
	return "", ""
}

// withMessage adds a kind's text to an event's details, or marks the event
// as not for Discord when the server turned that kind off there.
func withMessage(cfg server.Config, kind string, vars map[string]string, details map[string]string) map[string]string {
	if details == nil {
		details = map[string]string{}
	}
	msg := cfg.Messages.Get(kind)
	if !msg.Discord {
		details["discord"] = "off"
	}
	if details["announce"] == "" {
		all := map[string]string{"name": cfg.Name}
		for k, v := range vars {
			all[k] = v
		}
		details["announce"] = server.Render(msg.Text, all)
	}
	return details
}

// message announces a kind: over RCON when it goes to the game and inGame
// says someone is there, and to Discord through its event. Only the RCON
// failure is returned; Discord delivery is the dispatcher's business.
func (c *Controller) message(ctx context.Context, cfg server.Config, kind string, vars map[string]string, inGame bool) error {
	details := withMessage(cfg, kind, vars, map[string]string{"kind": kind})
	var err error
	if cfg.Messages.Get(kind).Game && inGame {
		// Emoji stay in Discord; the game would show question marks.
		if text := server.GameText(details["announce"]); text != "" {
			_, err = c.rcon(ctx, cfg, cfg.Warnings.CommandLine(text))
		}
	}
	c.emit(messageEvent[kind], cfg, details["announce"], details)
	return err
}
