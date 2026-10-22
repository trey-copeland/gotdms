package dataset

import (
	"bytes"
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

func TestSegmentIndexTrustIdentityOption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.tdms")
	w, err := tdms.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("acq").Channel("ai0", tdms.TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(tdms.PropUnitString, "abcd"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	clock, err := w.Group("acq").Channel("Time", tdms.TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := clock.Write([]float64{0, 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tdms.BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}
	side := tdms.SegmentIndexPath(path)
	before, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(raw, []byte("abcd"))
	if idx < 0 {
		t.Fatal("property bytes not found")
	}
	copy(raw[idx:], []byte("wxyz"))
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}

	ds, err := OpenFiles([]string{path},
		WithTimestampChannelName("Time"),
		WithNumericEpoch(epoch),
		WithSegmentIndexTrustIdentity(),
	)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 2}) {
		t.Fatalf("Y %v", s.Y)
	}
	after, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("trust-identity catalog rewrote the sidecar")
	}

	plain, err := OpenFiles([]string{path},
		WithTimestampChannelName("Time"),
		WithNumericEpoch(epoch),
		WithSegmentIndex(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Read(Selector{Name: "ai0"}); err != nil {
		t.Fatal(err)
	}
	rewritten, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, rewritten) {
		t.Fatal("default segment index kept a stale sidecar")
	}

	off := openAt(t, dir, WithSegmentIndexTrustIdentity(), WithSegmentIndex(false))
	if _, err := off.Read(Selector{Name: "ai0"}); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentIndexTrustThenDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.tdms")
	writePair(t, path, "acq", "ai0", []float64{0, 1}, []float64{10, 11})
	off := openAt(t, dir, WithSegmentIndexTrustIdentity(), WithSegmentIndex(false))
	if _, err := off.Read(Selector{Name: "ai0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tdms.SegmentIndexPath(path)); !os.IsNotExist(err) {
		t.Fatalf("WithSegmentIndex(false) after trust still wrote a sidecar: %v", err)
	}
	on := openAt(t, dir, WithSegmentIndex(false), WithSegmentIndexTrustIdentity())
	if _, err := on.Read(Selector{Name: "ai0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tdms.SegmentIndexPath(path)); err != nil {
		t.Fatalf("trust after WithSegmentIndex(false) did not enable the sidecar: %v", err)
	}
}
