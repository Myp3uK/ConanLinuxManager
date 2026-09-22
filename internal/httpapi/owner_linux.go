//go:build linux

package httpapi

import (
	"io/fs"
	"syscall"
)

func fileOwner(info fs.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}
