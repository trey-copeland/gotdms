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

// fileID is the device and inode of an open file. It matches fileIdentity
// of the handle's Stat result.
func fileID(f *os.File) (dev, ino uint64, err error) {
	if f == nil {
		return 0, 0, os.ErrInvalid
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	dev, ino = fileIdentity(fi)
	return dev, ino, nil
}
