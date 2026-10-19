//go:build linux

package dataset

// fdDir is this process's file-descriptor directory.
// /dev/fd points here on Linux; the test reads this path directly.
const fdDir = "/proc/self/fd"
