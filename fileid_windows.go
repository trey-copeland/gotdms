//go:build windows

package tdms

import (
	"os"
	"syscall"
)

// fileIdentity cannot see a Windows file index. FileInfo.Sys is a
// Win32FileAttributeData, which has no file index. Callers that have the
// open handle use fileID instead.
func fileIdentity(fi os.FileInfo) (dev, ino uint64) { return 0, 0 }

// fileID reads the volume serial number and file index from the open handle.
// A file replaced at the same path gets a new file index, so trust mode can
// tell it from the file the sidecar was built for even when size and
// modification time were copied onto the replacement.
//
// A failed call is returned: storing 0, 0 would make trust mode treat every
// unreadable handle as the same file.
func fileID(f *os.File) (dev, ino uint64, err error) {
	if f == nil {
		return 0, 0, os.ErrInvalid
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return 0, 0, err
	}
	dev = uint64(info.VolumeSerialNumber)
	ino = uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return dev, ino, nil
}
