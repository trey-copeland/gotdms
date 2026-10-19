//go:build !unix

package dataset

// countOpenFDs reports that this OS has no file-descriptor directory.
// The leak check then renames the files under test. On Windows that
// rename fails while a handle is open: os.Open does not pass
// FILE_SHARE_DELETE.
func countOpenFDs() (int, bool, error) {
	return 0, false, nil
}
