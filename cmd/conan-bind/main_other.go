//go:build !linux

package main

import "fmt"

func run(action, saved, ram string) error {
	return fmt.Errorf("conan-bind requires Linux")
}
