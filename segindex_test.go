package tdms

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSegmentIndexSecondOpenSkipsParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	side := SegmentIndexPath(path)
	writeIndexedFile(t, path)

	before := segmentParses.Load()
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	if f.SegmentIndexUsed() {
		t.Fatal("first open should parse")
	}
	if segmentParses.Load() != before+1 {
		t.Fatalf("parses %d -> %d", before, segmentParses.Load())
	}
	want := indexedSnapshot(t, f)
	f.Close()
	if _, err := os.Stat(side); err != nil {
		t.Fatal(err)
	}

	before = segmentParses.Load()
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SegmentIndexUsed() {
		t.Fatal("second open did not use the segment index")
	}
	if segmentParses.Load() != before {
		t.Fatalf("second open parsed segments (%d -> %d)", before, segmentParses.Load())
	}
	got := indexedSnapshot(t, f)
	if got != want {
		t.Fatalf("cache snapshot\n%s\nwant\n%s", got, want)
	}
	temps, _ := filepath.Glob(filepath.Join(dir, ".gtdx-*"))
	if len(temps) != 0 {
		t.Fatalf("temp files left behind: %v", temps)
	}
}

func TestSegmentIndexSampleChangeStaysCorrect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	side := SegmentIndexPath(path)
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
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint64(raw[len(raw)-8:], math.Float64bits(42))
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SegmentIndexUsed() {
		t.Fatal("in-place sample edit should keep the layout cache")
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 42})
}

func TestSegmentIndexMetadataChangeReparses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	side := SegmentIndexPath(path)
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropUnitString, "abcd"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

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
	before := segmentParses.Load()
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	if f.SegmentIndexUsed() {
		t.Fatal("metadata change must not reuse the cache")
	}
	if segmentParses.Load() == before {
		t.Fatal("expected a fresh parse")
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	p, ok := c.Property(PropUnitString)
	f.Close()
	if !ok || p.Value != "wxyz" {
		t.Fatalf("property %+v ok %v", p, ok)
	}

	// The fallback rewrote the sidecar. The next open uses it and still
	// returns the edited property.
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SegmentIndexUsed() {
		t.Fatal("rewritten cache was not used")
	}
	g, _ = f.Group("g")
	c, _ = g.Channel("c")
	p, ok = c.Property(PropUnitString)
	if !ok || p.Value != "wxyz" {
		t.Fatalf("cached property %+v", p)
	}
}

func TestSegmentIndexRewriteAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	side := SegmentIndexPath(path)
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	w, err = Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err = w.Group("g").Channel("d", TypeInt32)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]int32{9, 8, 7, 6}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	if f.SegmentIndexUsed() {
		t.Fatal("replaced file reused a stale cache")
	}
	g, ok := f.Group("g")
	if !ok {
		t.Fatal("group")
	}
	if _, ok := g.Channel("c"); ok {
		t.Fatal("stale channel c")
	}
	d, ok := g.Channel("d")
	if !ok {
		t.Fatal("channel d")
	}
	assertEqual(t, mustValues(t, d), []int32{9, 8, 7, 6})
	f.Close()

	blob, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0xff
	if err := os.WriteFile(side, blob, 0644); err != nil {
		t.Fatal(err)
	}
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	if f.SegmentIndexUsed() {
		t.Fatal("corrupt sidecar was used")
	}
	g, _ = f.Group("g")
	d, _ = g.Channel("d")
	assertEqual(t, mustValues(t, d), []int32{9, 8, 7, 6})
	f.Close()

	if err := os.WriteFile(side, []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.SegmentIndexUsed() {
		t.Fatal("garbage sidecar was used")
	}
	g, _ = f.Group("g")
	d, _ = g.Channel("d")
	assertEqual(t, mustValues(t, d), []int32{9, 8, 7, 6})
}

func TestSegmentIndexDoesNotReplaceDataFile(t *testing.T) {
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
	if err := ch.Write([]float64{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, WithSegmentIndex(path))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("sidecar path equal to the data file changed the TDMS bytes")
	}
}

func TestSegmentIndexOptIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := os.Stat(SegmentIndexPath(path)); !os.IsNotExist(err) {
		t.Fatalf("sidecar created without the option: %v", err)
	}
}

func TestSegmentIndexBigEndianAndIncomplete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "be.tdms")
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], math.Float64bits(1.5))
	file := beLead(TocMetaData|TocNewObjList|TocRawData|TocBigEndian, beObjects(t, []beObj{{"/'g'/'c'", TypeFloat64, 1}}), raw[:])
	if err := os.WriteFile(path, file, 0644); err != nil {
		t.Fatal(err)
	}
	side := SegmentIndexPath(path)
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	if !f.SegmentIndexUsed() {
		t.Fatal("big-endian file did not use the cache")
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1.5})
	f.Close()

	inc := filepath.Join(dir, "tail.tdms")
	writeFloatSegment(t, inc, []float64{1, 2, 3, 4}, true)
	body, err := os.ReadFile(inc)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint64(body[12:], incompleteSegment)
	body = body[:len(body)-3]
	if err := os.WriteFile(inc, body, 0644); err != nil {
		t.Fatal(err)
	}
	iside := SegmentIndexPath(inc)
	f, err = Open(inc, WithSegmentIndex(iside))
	if err != nil {
		t.Fatal(err)
	}
	st := f.Status()
	f.Close()
	f, err = Open(inc, WithSegmentIndex(iside))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SegmentIndexUsed() {
		t.Fatal("incomplete file did not use the cache")
	}
	if f.Status() != st || !f.Status().Incomplete {
		t.Fatalf("cached status %#v, first %#v", f.Status(), st)
	}
	g, _ = f.Group("g")
	c, _ = g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3})
	if c.committed() != 0 {
		t.Fatalf("committed %d", c.committed())
	}
}

func TestSegmentIndexAdversarialLengths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
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
	side := SegmentIndexPath(path)
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	blob, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) < segmentIndexHeader+spanRecordV1+8 {
		t.Fatalf("sidecar len %d", len(blob))
	}
	spanAt := len(blob) - 8 - spanRecordV1
	if got := int64(binary.LittleEndian.Uint64(blob[spanAt+16:])); got != 1 {
		t.Fatalf("nChunks at %d = %d; span is not where this test assumed", spanAt, got)
	}
	if got := int64(binary.LittleEndian.Uint64(blob[spanAt+24:])); got != 3 {
		t.Fatalf("nPerChunk = %d", got)
	}
	if got := binary.LittleEndian.Uint32(blob[spanAt+48:]); got != 4 {
		t.Fatalf("typeSize = %d", got)
	}
	if got := int64(binary.LittleEndian.Uint64(blob[spanAt+8:])); got != 12 {
		t.Fatalf("chunkSize = %d", got)
	}
	if got := int64(binary.LittleEndian.Uint64(blob[spanAt+40:])); got != 4 {
		t.Fatalf("stride = %d", got)
	}

	patch := func(edit func([]byte)) {
		t.Helper()
		b := append([]byte(nil), blob...)
		edit(b)
		binary.LittleEndian.PutUint32(b[44:48], crc32.ChecksumIEEE(b[segmentIndexHeader:]))
		if _, err := decodeSegmentIndex(b); err == nil {
			t.Fatal("crafted sidecar decoded")
		}
		if err := os.WriteFile(side, b, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := Open(path, WithSegmentIndex(side))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer f.Close()
		if f.SegmentIndexUsed() {
			t.Fatal("crafted sidecar was used")
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		assertEqual(t, mustValues(t, c), []int32{1, 2, 3})
	}

	patch(func(b []byte) {
		// More chunks than the file can hold. total must agree or the
		// length check fails before the span-in-file check.
		nChunks := int64(1) << 40
		binary.LittleEndian.PutUint64(b[spanAt+16:], uint64(nChunks))
		binary.LittleEndian.PutUint64(b[len(b)-8:], uint64(nChunks*3))
	})
	patch(func(b []byte) {
		binary.LittleEndian.PutUint64(b[spanAt:], uint64(1)<<40)
	})
	patch(func(b []byte) {
		n := int64(1) << 60
		binary.LittleEndian.PutUint64(b[spanAt+24:], uint64(n))
		binary.LittleEndian.PutUint64(b[len(b)-8:], uint64(n))
	})
	patch(func(b []byte) {
		// Stride that walks a sample past the 12-byte chunk: col 0, stride 8,
		// 3 samples reaches byte 20. The sample count and total still agree, so
		// the length checks pass; only the intra-chunk walk catches it. Without
		// that check the read addresses the next chunk or runs past EOF.
		binary.LittleEndian.PutUint64(b[spanAt+40:], 8)
	})
	patch(func(b []byte) {
		// An out-of-range stride that stays within the file byte budget
		// (samples*typeSize) but blows past the chunk.
		binary.LittleEndian.PutUint64(b[spanAt+40:], uint64(1)<<40)
	})
}

func writeIndexedFile(t *testing.T, path string) {
	t.Helper()
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w.SetInterleaved(true)
	when := time.Date(2024, 6, 1, 12, 0, 0, 123456789, time.UTC)
	if err := w.SetProperty("title", "demo"); err != nil {
		t.Fatal(err)
	}
	if err := w.SetProperty("flag", true); err != nil {
		t.Fatal(err)
	}
	g := w.Group("acq")
	if err := g.SetProperty("note", "lab"); err != nil {
		t.Fatal(err)
	}
	a, err := g.Channel("a", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.Channel("b", TypeInt16)
	if err != nil {
		t.Fatal(err)
	}
	s, err := g.Channel("s", TypeString)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetProperty(PropUnitString, "V"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetProperty(PropStartTime, NewTimestamp(when)); err != nil {
		t.Fatal(err)
	}
	if err := a.Write([]float64{1.5, -2.5, 3.5}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]int16{10, 20, 30}); err != nil {
		t.Fatal(err)
	}
	// Strings are not interleaved with the numeric channels. A second segment
	// holds them so the cache covers both layouts.
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	w.SetInterleaved(false)
	if err := s.Write([]string{"a", "b,c", ""}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func indexedSnapshot(t *testing.T, f *File) string {
	t.Helper()
	g, ok := f.Group("acq")
	if !ok {
		t.Fatal("group")
	}
	a, _ := g.Channel("a")
	b, _ := g.Channel("b")
	s, _ := g.Channel("s")
	av := mustValues(t, a)
	bv := mustValues(t, b)
	sv := mustValues(t, s)
	title, _ := f.Property("title")
	flag, _ := f.Property("flag")
	note, _ := g.Property("note")
	unit, _ := a.Property(PropUnitString)
	start, _ := a.Property(PropStartTime)
	return fmt.Sprintf("%v|%v|%v|%v|%v|%v|%v|%v|%v|%v|%v",
		f.Version(), f.SegmentCount(), f.Status().String(),
		title.Value, flag.Value, note.Value, unit.Value, start.Value, av, bv, sv)
}
