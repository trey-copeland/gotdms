package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trey-copeland/gotdms"
)

func writeCLIFile(t *testing.T, path string) {
	t.Helper()
	w, err := tdms.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", tdms.TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIndexCommand(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.tdms")
	b := filepath.Join(dir, "b.tdms")
	writeCLIFile(t, a)
	writeCLIFile(t, b)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"index", a, b}, &stdout, &stderr); code != 0 {
		t.Fatalf("index: %d %s", code, stderr.String())
	}
	text := stdout.String()
	if !strings.Contains(text, a+": wrote "+tdms.SegmentIndexPath(a)) || !strings.Contains(text, b+": wrote ") {
		t.Fatalf("stdout:\n%s", text)
	}
	for _, path := range []string{a, b} {
		ok, err := tdms.SegmentIndexValid(path)
		if err != nil || !ok {
			t.Fatalf("%s valid %v %v", path, ok, err)
		}
	}

	stdout.Reset()
	stderr.Reset()
	info, err := os.Stat(tdms.SegmentIndexPath(a))
	if err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"index", a}, &stdout, &stderr); code != 0 {
		t.Fatalf("second: %d %s", code, stderr.String())
	}
	if stdout.String() != a+": current\n" {
		t.Fatalf("current:\n%s", stdout.String())
	}
	info2, err := os.Stat(tdms.SegmentIndexPath(a))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, info2) {
		t.Fatal("current replaced the sidecar")
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"index", "--force", a}, &stdout, &stderr); code != 0 {
		t.Fatalf("force: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), ": wrote ") {
		t.Fatalf("force stdout:\n%s", stdout.String())
	}
	info3, err := os.Stat(tdms.SegmentIndexPath(a))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info2, info3) {
		t.Fatal("force kept the previous sidecar inode")
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"index", "--check", a, b}, &stdout, &stderr); code != 0 {
		t.Fatalf("check: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), a+": ok\n") || !strings.Contains(stdout.String(), b+": ok\n") {
		t.Fatalf("check stdout:\n%s", stdout.String())
	}

	if err := os.Remove(tdms.SegmentIndexPath(b)); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"index", "--check", a, b}, &stdout, &stderr); code == 0 {
		t.Fatal("check of a missing sidecar exited 0")
	}
	if !strings.Contains(stdout.String(), a+": ok\n") || !strings.Contains(stderr.String(), "missing sidecar") {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(tdms.SegmentIndexPath(b)); !os.IsNotExist(err) {
		t.Fatal("check rewrote a sidecar")
	}

	if err := os.WriteFile(tdms.SegmentIndexPath(a), []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"index", "--check", a}, &stdout, &stderr); code == 0 {
		t.Fatal("corrupt check exited 0")
	}
	if !strings.Contains(stderr.String(), "does not match") {
		t.Fatalf("stderr %s", stderr.String())
	}
	blob, err := os.ReadFile(tdms.SegmentIndexPath(a))
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != "nope" {
		t.Fatal("check rewrote a corrupt sidecar")
	}

	stdout.Reset()
	stderr.Reset()
	missing := filepath.Join(dir, "missing.tdms")
	if code := run([]string{"index", a, missing}, &stdout, &stderr); code == 0 {
		t.Fatal("missing file exited 0")
	}
	if !strings.Contains(stdout.String(), ": wrote ") || !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"index"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("usage %d %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"index", "--check", "--force", a}, &stdout, &stderr); code != 2 {
		t.Fatalf("both flags %d %s", code, stderr.String())
	}
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "gotdms index") {
		t.Fatalf("help:\n%s", stdout.String())
	}
}

func TestIndexDirAndGlob(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "batch")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, filepath.Join(sub, "one.tdms"))
	writeCLIFile(t, filepath.Join(sub, "two.tdms"))
	if err := os.WriteFile(filepath.Join(sub, "note.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"index", sub}, &stdout, &stderr); code != 0 {
		t.Fatalf("dir: %d %s", code, stderr.String())
	}
	if strings.Count(stdout.String(), ": wrote ") != 2 {
		t.Fatalf("dir stdout:\n%s", stdout.String())
	}
	if err := os.Remove(tdms.SegmentIndexPath(filepath.Join(sub, "one.tdms"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tdms.SegmentIndexPath(filepath.Join(sub, "two.tdms"))); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	pat := filepath.Join(sub, "*.tdms")
	if code := run([]string{"index", pat}, &stdout, &stderr); code != 0 {
		t.Fatalf("glob: %d %s", code, stderr.String())
	}
	if strings.Count(stdout.String(), ": wrote ") != 2 {
		t.Fatalf("glob stdout:\n%s", stdout.String())
	}
}
