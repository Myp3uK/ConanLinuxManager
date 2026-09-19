//go:build !linux

package ramstate

func restoreOwner(path, directory string) error { return nil }
