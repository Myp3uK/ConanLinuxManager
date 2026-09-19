// conan-bind is an offline, root-only mount probe. It is not a setuid helper.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	action := flag.String("action", "", "mount or unmount")
	saved := flag.String("saved", "", "absolute prepared Saved directory")
	ram := flag.String("ram", "", "absolute prepared tmpfs database directory")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(1)
	}
	if err := run(*action, *saved, *ram); err != nil {
		fmt.Fprintln(os.Stderr, "Bind operation failed:", err)
		os.Exit(1)
	}
}
