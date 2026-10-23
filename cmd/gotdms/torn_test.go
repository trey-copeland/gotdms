package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/trey-copeland/gotdms"
)

func TestInfoTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "torn.tdms")
	writeCLIFile(t, path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	whole := fi.Size()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("TDSm\x0e")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"info", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("strict info: %d", code)
	}
	if s := stderr.String(); !strings.Contains(s, "torn tail: 5 bytes") || !strings.Contains(s, "--lenient") ||
		!strings.Contains(s, "truncating the file to "+itoa(whole)+" bytes makes it open") {
		t.Fatalf("hint: %s", s)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"info", "--lenient", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("lenient info: %d %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"status: truncated", "dropped torn tail bytes: 5", "whole segments end at byte: " + itoa(whole), "tail error: ", "c"} {
		if !strings.Contains(out, want) {
			t.Fatalf("lenient info missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unused tail bytes") {
		t.Fatalf("lenient info mixes torn and partial-tail fields:\n%s", out)
	}

	stdout.Reset()
	if code := run([]string{"info", "--json", "--lenient", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("lenient json: %d %s", code, stderr.String())
	}
	var doc struct {
		Status struct {
			Truncated   bool   `json:"truncated"`
			DroppedTail int64  `json:"dropped_tail_bytes"`
			WholeBytes  int64  `json:"whole_bytes"`
			TailError   string `json:"tail_error"`
		} `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Status.Truncated || doc.Status.DroppedTail != 5 || doc.Status.WholeBytes != whole || doc.Status.TailError == "" {
		t.Fatalf("json status %+v", doc.Status)
	}

	// A clean file: no hint, no torn fields.
	clean := filepath.Join(dir, "clean.tdms")
	writeCLIFile(t, clean)
	stdout.Reset()
	if code := run([]string{"info", "--json", "--lenient", clean}, &stdout, &stderr); code != 0 {
		t.Fatal(code)
	}
	if strings.Contains(stdout.String(), "dropped_tail_bytes") || strings.Contains(stdout.String(), "tail_error") {
		t.Fatalf("clean json has torn fields: %s", stdout.String())
	}

	// Not a TDMS file at all: the strict error, and no hint.
	junk := filepath.Join(dir, "junk.tdms")
	if err := os.WriteFile(junk, make([]byte, 64), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"info", junk}, &stdout, &stderr); code != 1 || strings.Contains(stderr.String(), "lenient") {
		t.Fatalf("junk: %d %s", code, stderr.String())
	}
}

// A truncate size is printed only when the file did not change between
// the stat before the open and the one after it.
func TestWholeBytesFileChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.tdms")
	writeCLIFile(t, path)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := tdms.FileStatus{Truncated: true, DroppedTail: 5}
	if got := wholeBytes(path, st, before); got != before.Size()-5 {
		t.Fatalf("unchanged file: %d", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("TDSm")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := wholeBytes(path, st, before); got != -1 {
		t.Fatalf("grown file: %d", got)
	}
	if got := wholeBytes(path, st, nil); got != -1 {
		t.Fatalf("no stat before: %d", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
