package main

import "github.com/example/conan-manager/internal/bindmount"

func run(action, saved, ram string) error { return bindmount.Run(action, saved, ram) }
