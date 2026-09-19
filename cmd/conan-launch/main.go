// conan-launch is a root-operated launcher, not a setuid or sudo API.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	config := flag.String("config", "", "root-owned launcher JSON configuration")
	worker := flag.Bool("worker", false, "internal namespace worker")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	if err := run(*config, *worker); err != nil {
		fmt.Fprintln(os.Stderr, "Launcher failed:", err)
		os.Exit(2)
	}
}
