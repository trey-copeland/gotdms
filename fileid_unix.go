//go:build unix

package tdms

import (
	"os"
	"syscall"
)

// fileIdentity returns the device and inode of fi. Both are 0 when the
// FileInfo does not carry a Stat_t. A replaced file has a new inode even
// when size and modification time were copied onto it.
func fileIdentity(fi os.FileInfo) (dev, ino uint64) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, 0
	}
	return uint64(st.Dev), uint64(st.Ino)
}
