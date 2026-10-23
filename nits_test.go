package tdms

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMustMulPanicsOnOverflow(t *testing.T) {
	if got := mustMul(1<<20, 1<<20); got != 1<<40 {
		t.Fatalf("mustMul = %d", got)
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("mustMul overflow did not panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "overflows int64") {
			t.Fatalf("panic = %v", r)
		}
	}()
	mustMul(math.MaxInt64, 2)
}

// The default WithSegmentIndex open replaces a bad sidecar after a parse.
// withoutSegmentIndexWrite (test-only) reads it the same way and leaves it.
func TestSegmentIndexWriteOnMissUnlessDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeInt32)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]int32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	junk := []byte("not a sidecar")

	kept := filepath.Join(dir, "kept.gtdx")
	if err := os.WriteFile(kept, junk, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, WithSegmentIndex(kept), withoutSegmentIndexWrite())
	if err != nil {
		t.Fatal(err)
	}
	if f.SegmentIndexUsed() {
		t.Fatal("junk sidecar was used")
	}
	f.Close()
	if b, _ := os.ReadFile(kept); string(b) != string(junk) {
		t.Fatalf("withoutSegmentIndexWrite rewrote the sidecar (%d bytes)", len(b))
	}

	replaced := filepath.Join(dir, "replaced.gtdx")
	if err := os.WriteFile(replaced, junk, 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Open(path, WithSegmentIndex(replaced))
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	if b, _ := os.ReadFile(replaced); string(b) == string(junk) {
		t.Fatal("default open did not replace a bad sidecar")
	}
	h, err := Open(path, WithSegmentIndex(replaced))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if !h.SegmentIndexUsed() {
		t.Fatal("replaced sidecar was not a hit")
	}
}
