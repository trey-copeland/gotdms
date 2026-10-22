//go:build !unix && !windows

package tdms

import "os"

// fileIdentity returns 0, 0 on platforms without a portable device and inode.
// The segment index still checks size, modification time, and the metadata
// fingerprint. Trust mode on these platforms checks only size and modification
// time, and both identity fields are stored as 0.
func fileIdentity(fi os.FileInfo) (dev, ino uint64) { return 0, 0 }

// fileID returns 0, 0 on platforms without a file index. The nil error is
// deliberate: there is nothing to query, and a trust check stores both
// fields as 0.
func fileID(f *os.File) (dev, ino uint64, err error) { return 0, 0, nil }
