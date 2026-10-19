//go:build unix

package dataset

import "os"

// countOpenFDs returns the number of open file descriptors.
// The count includes the directory descriptor opened to read fdDir.
// An error opening that directory fails the test: a missing fd listing
// must not turn into a skipped leak check on Unix.
func countOpenFDs() (int, bool, error) {
	f, err := os.Open(fdDir)
	if err != nil {
		return 0, false, err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return 0, false, err
	}
	return len(names), true, nil
}
