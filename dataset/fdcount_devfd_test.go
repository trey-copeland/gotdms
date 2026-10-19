//go:build unix && !linux

package dataset

// fdDir is this process's file-descriptor directory on Unix other than
// Linux (darwin, the BSDs, and the rest). Linux reads /proc/self/fd.
const fdDir = "/dev/fd"
