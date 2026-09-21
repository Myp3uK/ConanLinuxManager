package recoveryalert

// Recovery alerts are deployed on Linux. Windows does not support directory Sync.
func syncDirectory(path string) error { return nil }
