package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/trey-copeland/gotdms"
)

// writeFragmented writes a two-segment file and returns its path.
func writeFragmented(t *testing.T, path string) {
	t.Helper()
	w, err := tdms.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", tdms.TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{4, 5}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func segmentCount(t *testing.T, path string) int {
	t.Helper()
	f, err := tdms.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return f.SegmentCount()
}

func TestDefragmentCLIInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writeFragmented(t, path)
	if got := segmentCount(t, path); got < 2 {
		t.Fatalf("fixture segments %d", got)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"defragment", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("defragment: %d %s", code, stderr.String())
	}
	if got := segmentCount(t, path); got != 1 {
		t.Fatalf("segments %d, want 1", got)
	}
}

func TestDefragmentCLIOutFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.tdms")
	dst := filepath.Join(dir, "out.tdms")
	writeFragmented(t, src)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"defragment", "-o", dst, src}, &stdout, &stderr); code != 0 {
		t.Fatalf("defragment: %d %s", code, stderr.String())
	}
	if got := segmentCount(t, dst); got != 1 {
		t.Fatalf("out segments %d, want 1", got)
	}
	// Input is unchanged.
	if got := segmentCount(t, src); got < 2 {
		t.Fatalf("input segments %d, should be untouched", got)
	}
}

func TestDefragmentCLIMissingFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"defragment", filepath.Join(t.TempDir(), "nope.tdms")}, &stdout, &stderr); code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("expected an error message")
	}
}

func TestDefragmentCLIUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"defragment"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
}
