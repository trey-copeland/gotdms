package tdms

import (
	"encoding/binary"
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
