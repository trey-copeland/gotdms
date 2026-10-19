//go:build !unix

package tdms

import "os"

// fileIdentity returns 0, 0 on platforms without a portable device and inode.
// The segment index still checks size, modification time, and the metadata
// fingerprint.
func fileIdentity(fi os.FileInfo) (dev, ino uint64) { return 0, 0 }
