package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/example/conan-manager/internal/rcon"
)

// runRcon is the rcon subcommand, a standalone console client:
//
//	conan-manager rcon -a 127.0.0.1 -p 25704 -P secret -q "broadcast Привет всем"
//
// The command may also follow the flags unquoted. The password can come from
// CONAN_RCON_PASSWORD instead of -P, which keeps it out of the process list.
func runRcon(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("rcon", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("a", "127.0.0.1", "server address")
	port := flags.Int("p", 25575, "RCON port (RconPort in Game.ini)")
	password := flags.String("P", "", "RCON password (or CONAN_RCON_PASSWORD)")
	query := flags.String("q", "", "command to run")
	timeout := flags.Duration("t", 10*time.Second, "timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	command := *query
	if command == "" {
		command = strings.Join(flags.Args(), " ")
	}
	if command == "" {
		fmt.Fprintln(stderr, "rcon: no command; pass -q \"...\"")
		return 2
	}
	if *password == "" {
		*password = os.Getenv("CONAN_RCON_PASSWORD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client, err := rcon.Dial(ctx, net.JoinHostPort(*address, strconv.Itoa(*port)), *password)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer client.Close()
	out, err := client.Exec(ctx, command)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprint(stdout, out)
	if out != "" && !strings.HasSuffix(out, "\n") {
		fmt.Fprintln(stdout)
	}
	return 0
}
