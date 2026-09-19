//go:build !linux

package sessionlock

// Production cross-process session locking is Linux-only.
type Lease struct{}

func Acquire(paths ...string) (*Lease, error) { return &Lease{}, nil }
func (*Lease) Close()                         {}
