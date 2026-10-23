package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusCompleteAndTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := f.Status()
	if !st.Complete || st.Incomplete || st.PartialTail || st.UnusedTail != 0 {
		t.Fatalf("complete file: %#v", st)
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3, 4})
	if c.committed() != 4 {
		t.Fatalf("committed %d", c.committed())
	}
	f.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	offs := leadInOffsets(t, raw)
	if len(offs) != 2 {
		t.Fatalf("lead-ins %v", offs)
	}
	// Leave the first segment finished. Mark the second as the NI incomplete
	// sentinel and drop three trailing raw bytes so the tail is short.
	binary.LittleEndian.PutUint64(raw[offs[1]+12:], incompleteSegment)
	raw = raw[:len(raw)-3]
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	f, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st = f.Status()
	if st.Complete || !st.Incomplete || !st.PartialTail || st.UnusedTail != 5 {
		t.Fatalf("truncated file: %#v", st)
	}
	g, _ = f.Group("g")
	c, _ = g.Channel("c")
	// Historical read: whole values in the incomplete segment are kept.
	// 16 raw bytes of the second segment minus 3 leaves one float64 and 5 unused bytes.
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3})
	if c.committed() != 2 {
		t.Fatalf("committed %d, want the finished segment only", c.committed())
	}
	if st.String() != "incomplete" {
		t.Fatal(st.String())
	}
}

func TestLenientTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cut.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	offs := leadInOffsets(t, raw)
	if len(offs) != 2 {
		t.Fatalf("lead-ins %v", offs)
	}
	// Keep the stored next-segment offset. Cutting the tail makes that
	// finite length run past EOF. This is not the incomplete sentinel.
	cut := append([]byte(nil), raw[:len(raw)-3]...)
	if err := os.WriteFile(path, cut, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrTruncated) {
		t.Fatalf("strict open: %v", err)
	}
	f, err := Open(path, WithLenientTail())
	if err != nil {
		t.Fatal(err)
	}
	st := f.Status()
	if st.Complete || st.Incomplete || !st.Truncated || !st.PartialTail || st.UnusedTail != 5 {
		t.Fatalf("%#v", st)
	}
	if st.String() != "truncated" {
		t.Fatal(st.String())
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3})
	if c.committed() != 2 {
		t.Fatalf("committed %d", c.committed())
	}
	f.Close()

	// A fragment shorter than a lead-in is truncated strictly. A lenient
	// open drops it as a torn tail and keeps both whole segments.
	short := append([]byte(nil), raw...)
	short = append(short, []byte("TDSm")...)
	if err := os.WriteFile(path, short, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrTruncated) {
		t.Fatalf("strict short lead-in: %v", err)
	}
	sf, err := Open(path, WithLenientTail())
	if err != nil {
		t.Fatalf("short lead-in: %v", err)
	}
	if st := sf.Status(); !st.Truncated || st.Complete || st.DroppedTail != 4 || !errors.Is(st.TailError, ErrTruncated) || st.PartialTail || st.UnusedTail != 0 {
		t.Fatalf("short lead-in status %#v", st)
	}
	sg, _ := sf.Group("g")
	sc, _ := sg.Channel("c")
	assertEqual(t, mustValues(t, sc), []float64{1, 2, 3, 4})
	if sc.committed() != 4 {
		t.Fatalf("committed %d", sc.committed())
	}
	sf.Close()

	// Metadata that itself runs past EOF, with no whole segment before it,
	// is still truncated: there is nothing to keep.
	var meta le
	meta.u32(1)
	meta.str("/'g'/'c'")
	meta.full(TypeInt32, 1)
	meta.u32(0)
	seg := finishSegment(t, TocMetaData|TocNewObjList|TocRawData, meta.b, []byte{1, 0, 0, 0})
	// Claim a metadata length past the bytes we keep.
	binary.LittleEndian.PutUint64(seg[20:28], uint64(len(meta.b)+8))
	binary.LittleEndian.PutUint64(seg[12:20], uint64(len(meta.b)+8+4))
	kept := seg[:28+4]
	if _, err := NewReader(bytes.NewReader(kept), WithLenientTail()); !errors.Is(err, ErrTruncated) {
		t.Fatalf("metadata past EOF: %v", err)
	}
}

func leadInOffsets(t *testing.T, b []byte) []int {
	t.Helper()
	var offs []int
	pos := 0
	for pos+leadInSize <= len(b) {
		offs = append(offs, pos)
		next := binary.LittleEndian.Uint64(b[pos+12 : pos+20])
		if next == incompleteSegment {
			break
		}
		step := leadInSize + int(next)
		if step <= 0 || pos+step > len(b) {
			break
		}
		pos += step
	}
	return offs
}
