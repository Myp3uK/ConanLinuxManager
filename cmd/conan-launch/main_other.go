//go:build !linux

package main

import "fmt"

func run(path string, worker bool) error { return fmt.Errorf("launcher requires Linux") }
