package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trey-copeland/gotdms"
)

// A literal file name with glob metacharacters (common on Windows, e.g.
// "run[1].tdms") must be indexed as that file, not treated as a pattern.
func TestIndexLiteralBracketName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run[1].tdms")
	writeCLIFile(t, p)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"index", p}, &stdout, &stderr); code != 0 {
		t.Fatalf("index %s: %d %s", p, code, stderr.String())
	}
	if _, err := os.Stat(tdms.SegmentIndexPath(p)); err != nil {
		t.Fatal(err)
	}
}

// The same file named twice (file plus its directory) is indexed once.
func TestIndexDedupesPaths(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.tdms")
	writeCLIFile(t, p)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"index", p, dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("index: %d %s", code, stderr.String())
	}
	if n := strings.Count(stdout.String(), "a.tdms:"); n != 1 {
		t.Fatalf("a.tdms reported %d times:\n%s", n, stdout.String())
	}
}

// A directory entry named *.tdms that is not a regular file is skipped.
func TestIndexDirSkipsNonRegular(t *testing.T) {
	dir := t.TempDir()
	writeCLIFile(t, filepath.Join(dir, "a.tdms"))
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "linked.tdms")); err != nil {
		t.Skip(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"index", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("index: %d %s", code, stderr.String())
	}
}
