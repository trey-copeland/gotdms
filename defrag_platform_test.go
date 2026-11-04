package tdms

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A read-only file rewritten in place. A rename replaces it on Unix (the
// directory is writable). Windows refuses to replace a read-only file with
// MoveFileEx; either way the result must be the defragmented file or the
// untouched original, with no temporary file left behind.
func TestDefragmentFileReadOnly(t *testing.T) {
	src := fragmented(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writeSrc(t, path, src, 0o444)
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	err := DefragmentFile(path)
	if err == nil {
		assertDefragged(t, path, src)
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o444 {
				t.Fatalf("mode %v, want 0444", fi.Mode().Perm())
			}
		}
	} else {
		t.Logf("read-only destination: %v", err)
		if got, rerr := os.ReadFile(path); rerr != nil || !bytes.Equal(got, src) {
			t.Fatalf("original changed after a failed rewrite: %v", rerr)
		}
	}
	assertOnlyFiles(t, dir, "run.tdms")
}

// A destination held open by another handle. Unix renames over it and the
// open handle keeps the old bytes (documented). Windows cannot replace a
// file opened without FILE_SHARE_DELETE, which os.Open does not request; the
// call must then fail cleanly.
func TestDefragmentToFileDestinationOpen(t *testing.T) {
	src := fragmented(t)
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "in.tdms")
	dstPath := filepath.Join(dir, "out.tdms")
	writeSrc(t, srcPath, src, 0o644)
	writeSrc(t, dstPath, []byte("old"), 0o644)
	h, err := os.Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	err = DefragmentToFile(dstPath, srcPath)
	var held [3]byte
	_, rerr := h.ReadAt(held[:], 0)
	h.Close()
	if rerr != nil || string(held[:]) != "old" {
		t.Fatalf("open handle sees %q (%v), want the old bytes", held[:], rerr)
	}
	if err == nil {
		assertDefragged(t, dstPath, src)
	} else {
		if runtime.GOOS != "windows" {
			t.Fatalf("rename over an open file failed: %v", err)
		}
		if got, _ := os.ReadFile(dstPath); string(got) != "old" {
			t.Fatalf("destination changed after a failed rewrite: %q", got)
		}
	}
	assertOnlyFiles(t, dir, "in.tdms", "out.tdms")
}
