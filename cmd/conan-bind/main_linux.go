package main

import "github.com/Myp3uK/ConanLinuxManager/internal/bindmount"

func run(action, saved, ram string) error { return bindmount.Run(action, saved, ram) }
