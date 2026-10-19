package dataset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trey-copeland/gotdms"
)

func TestSegmentIndexOption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.tdms")
	writePair(t, path, "acq", "ai0", []float64{0, 1}, []float64{10, 11})

	plain := openAt(t, dir)
	s, err := plain.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tdms.SegmentIndexPath(path)); !os.IsNotExist(err) {
		t.Fatalf("sidecar without the option: %v", err)
	}

	cached := openAt(t, dir, WithSegmentIndex(true))
	cs, err := cached.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(cs.Y, s.Y) {
		t.Fatalf("Y %v want %v", cs.Y, s.Y)
	}
	side := tdms.SegmentIndexPath(path)
	if _, err := os.Stat(side); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(side, []byte("corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	again := openAt(t, dir, WithSegmentIndex(true))
	as, err := again.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(as.Y, s.Y) {
		t.Fatalf("after corrupt sidecar Y %v", as.Y)
	}
	blob, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) < 4 || string(blob[:4]) != "GTDX" {
		t.Fatal("corrupt sidecar was not replaced")
	}
}
