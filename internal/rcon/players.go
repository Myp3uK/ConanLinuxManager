package rcon

import "strings"

// Player is one row of the listplayers table, keyed by the table's own
// column names ("Char name", "Player name", "Platform ID", ...), since they
// differ between game versions.
type Player map[string]string

// ParsePlayers reads the pipe-separated table listplayers prints: a header
// row, then one row per connected player. An empty server prints the header
// alone.
func ParsePlayers(output string) []Player {
	players := []Player{}
	var header []string
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r", ""), "\n") {
		if !strings.Contains(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if header == nil {
			header = cells
			continue
		}
		player := Player{}
		for i, cell := range cells {
			if i < len(header) && header[i] != "" {
				player[header[i]] = cell
			}
		}
		players = append(players, player)
	}
	return players
}
