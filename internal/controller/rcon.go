package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/gameconfig"
	"github.com/example/conan-manager/internal/rcon"
	"github.com/example/conan-manager/internal/server"
)

// rconTimeout bounds one RCON exchange; the game answers in milliseconds, and
// a hung game must not hold an API request or a restart.
const rconTimeout = 10 * time.Second

// RconSettings reads the [RconPlugin] section of the server's Game.ini. The
// game is the one that listens, so its file is the only source of truth.
func RconSettings(cfg server.Config) (rcon.Settings, error) {
	_, content, _, err := readConfig(cfg, "Game")
	if err != nil {
		return rcon.Settings{}, err
	}
	entries := map[string]string{}
	for _, section := range gameconfig.Parse(content) {
		if !strings.EqualFold(section.Name, rcon.Section) {
			continue
		}
		for _, e := range section.Entries {
			entries[e.Key] = e.Value
		}
	}
	return rcon.FromINI(entries), nil
}

// Rcon runs one console command on a running server.
func (c *Controller) Rcon(ctx context.Context, id, command string) (string, error) {
	cfg, err := c.Repo.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return c.rcon(ctx, cfg, command)
}

func (c *Controller) rcon(ctx context.Context, cfg server.Config, command string) (string, error) {
	if state := c.Lifecycle(cfg.ID).State; state != "running" {
		return "", fmt.Errorf("server %q is %s, RCON needs it running", cfg.ID, state)
	}
	settings, err := RconSettings(cfg)
	if err != nil {
		return "", err
	}
	if err := settings.Usable(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, rconTimeout)
	defer cancel()
	client, err := rcon.Dial(ctx, settings.Address(), settings.Password)
	if err != nil {
		return "", err
	}
	defer client.Close()
	return client.Exec(ctx, command)
}

// Players lists the players connected to a running server.
func (c *Controller) Players(ctx context.Context, id string) ([]rcon.Player, error) {
	out, err := c.Rcon(ctx, id, "listplayers")
	if err != nil {
		return nil, err
	}
	return rcon.ParsePlayers(out), nil
}
