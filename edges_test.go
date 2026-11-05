package tdms

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strconv"
	"testing"
	"time"
)

// Edges the first real Gremlins run left alive: the line ran, and a fallback
// or a second check still produced the same result.

func TestCursorAtRangeEdges(t *testing.T) {
	const n = 8
	data := make([]float64, n)
	for i := range data {
		data[i] = float64(i)
	}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")

	cur := c.CursorAt(0, n, 3)
	var got []float64
	for cur.Next() {
		got = append(got, cur.Values().([]float64)...)
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, data)

	// The whole tail from a nonzero start is in range; one more sample is not.
	cur = c.CursorAt(1, n-1, 3)
	got = nil
	for cur.Next() {
		got = append(got, cur.Values().([]float64)...)
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, data[1:])
	over := c.CursorAt(1, n, 3)
	if over.Next() || !errors.Is(over.Err(), ErrArgument) {
		t.Fatalf("one past: %v", over.Err())
	}

	for _, at := range [][2]int{{0, 0}, {n, 0}, {3, 0}} {
		empty := c.CursorAt(at[0], at[1], 4)
		if empty.Next() || empty.Err() != nil {
			t.Fatalf("empty %d:%d next err %v", at[0], at[1], empty.Err())
		}
	}
}

func TestCursorNextAfterClose(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeInt16)
	if err := ch.Write([]int16{1, 2, 3, 4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	cur := c.Cursor(2)
	if !cur.Next() {
		t.Fatal(cur.Err())
	}
	cur.Close()
	if cur.Next() || cur.Err() != nil {
		t.Fatalf("Next after Close: err %v", cur.Err())
	}

	cur = c.Cursor(2)
	cur.Close()
	if cur.Next() || cur.Err() != nil {
		t.Fatalf("Next after Close before any read: err %v", cur.Err())
	}
}

func TestEmptySliceIsTyped(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	str := mustCh(t, w, "g", "s", TypeString)
	if err := ch.Write([]float64{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := str.Write([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	for _, at := range []int{0, 2, c.Len()} {
		got, err := c.Slice(at, 0)
		if err != nil {
			t.Fatal(err)
		}
		s, ok := got.([]float64)
		if !ok || len(s) != 0 {
			t.Fatalf("Slice(%d, 0) = %#v", at, got)
		}
	}
	sc, _ := g.Channel("s")
	got, err := sc.Slice(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	ss, ok := got.([]string)
	if !ok || len(ss) != 0 {
		t.Fatalf("string Slice = %#v", got)
	}
	// Strings return from readRangeFrom before readTyped, so the
	// total-start check there is the one that rejects this range.
	if _, err := sc.Slice(1, 2); !errors.Is(err, ErrArgument) {
		t.Fatalf("string one past: %v", err)
	}
}

func TestDAQmxSliceOnePast(t *testing.T) {
	file := buildDAQmx(daqTOC, []dmObj{daqI16(RawIndexDAQmxFormatChanging, 4)}, i16Raw(1, 2, -1, -2))
	f := openBytes(t, file)
	c := daqCh(t, f, "g", "c")
	got, err := c.DAQmxSlice(0, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []int16{2, -1, -2})
	if _, err := c.DAQmxSlice(0, 1, 4); !errors.Is(err, ErrArgument) {
		t.Fatalf("one past: %v", err)
	}
}

func TestStreamWriteOnePastRemaining(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	s, err := ch.Stream(5)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{3, 4, 5, 6}); !errors.Is(err, ErrArgument) {
		t.Fatalf("one past remaining: %v", err)
	}
	if err := s.Write(ch, []float64{3, 4, 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3, 4, 5})
}

func TestStreamInterleavedOnePastRemaining(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	a := mustCh(t, w, "g", "a", TypeInt16)
	b := mustCh(t, w, "g", "b", TypeInt16)
	s, err := w.Stream([]*ChannelWriter{a, b}, []int{4, 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteInterleaved([]int16{1}, []int16{10}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteInterleaved([]int16{2, 3, 4, 5}, []int16{11, 12, 13, 14}); !errors.Is(err, ErrArgument) {
		t.Fatalf("one past remaining: %v", err)
	}
	if err := s.WriteInterleaved([]int16{2, 3, 4}, []int16{11, 12, 13}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPropertyWidthLimits(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	g := w.Group("g")
	if err := g.SetProperty("lo", int(math.MinInt32)); err != nil {
		t.Fatal(err)
	}
	if err := g.SetProperty("hi", int(math.MaxInt32)); err != nil {
		t.Fatal(err)
	}
	if err := g.SetProperty("i32lo", int32(math.MinInt32)); err != nil {
		t.Fatal(err)
	}
	if err := g.SetProperty("i32hi", int32(math.MaxInt32)); err != nil {
		t.Fatal(err)
	}
	if err := g.SetProperty("u32", uint32(math.MaxUint32)); err != nil {
		t.Fatal(err)
	}
	if err := g.SetProperty("uhi", uint(math.MaxUint32)); err != nil {
		t.Fatal(err)
	}
	if strconv.IntSize == 64 {
		hi := int(math.MaxInt32)
		if err := g.SetProperty("wide", hi+1); err != nil {
			t.Fatal(err)
		}
		u := uint(math.MaxUint32)
		if err := g.SetProperty("uwide", u+1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	gr, _ := openBytes(t, buf.Bytes()).Group("g")
	check := func(name string, want any) {
		t.Helper()
		p, ok := findProp(gr.Properties(), name)
		if !ok || p.Value != want {
			t.Fatalf("%s %+v", name, p)
		}
	}
	check("lo", int32(math.MinInt32))
	check("hi", int32(math.MaxInt32))
	check("i32lo", int32(math.MinInt32))
	check("i32hi", int32(math.MaxInt32))
	check("u32", uint32(math.MaxUint32))
	check("uhi", uint32(math.MaxUint32))
	if strconv.IntSize == 64 {
		check("wide", int64(math.MaxInt32)+1)
		check("uwide", uint64(math.MaxUint32)+1)
	}
}

func TestErrorTextIncludesPath(t *testing.T) {
	if got := wrap("read", "", ErrClosed).Error(); got != "tdms: read: closed" {
		t.Fatalf("%q", got)
	}
	if got := wrap("read", "/'g'/'c'", ErrArgument).Error(); got != "tdms: read /'g'/'c': invalid argument" {
		t.Fatalf("%q", got)
	}
}

func TestTimestampFractionIsNearest(t *testing.T) {
	// 2^64 / 1e9 = 18446744073.709..., so one nanosecond is fraction 18446744074.
	// Time() still matches a fraction two units away; the field is the nearer one.
	epoch := time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	in := epoch.Add(time.Nanosecond)
	ts := NewTimestamp(in)
	if ts.Seconds != 0 || ts.Fraction != 18446744074 {
		t.Fatalf("%+v", ts)
	}
	out, ok := ts.Time()
	if !ok || !out.Equal(in) {
		t.Fatalf("%s ok=%v", out, ok)
	}
}

func TestSeqReaderCacheAndClosed(t *testing.T) {
	var none *seqReader
	if n, err := none.ReadAt([]byte{0}, 0); n != 0 || !errors.Is(err, ErrClosed) {
		t.Fatalf("nil reader %d %v", n, err)
	}
	closed := &seqReader{}
	if n, err := closed.ReadAt([]byte{0}, 0); n != 0 || !errors.Is(err, ErrClosed) {
		t.Fatalf("nil source %d %v", n, err)
	}

	data := make([]byte, 16)
	for i := range data {
		data[i] = 0x11
	}
	src := &poisonAfter{data: data}
	seq := newSeqReader(src, 16)
	defer seq.release()
	got := make([]byte, 4)
	if n, err := seq.ReadAt(got, 0); n != 4 || err != nil || got[0] != 0x11 {
		t.Fatalf("fill %d %v %x", n, err, got)
	}
	// A later read of the underlying file returns different bytes. A hit
	// at the cached base, and one that ends on the last cached byte, must
	// not take that read.
	if n, err := seq.ReadAt(got, 0); n != 4 || err != nil || got[0] != 0x11 {
		t.Fatalf("cache base %d %v %x calls %d", n, err, got, src.calls)
	}
	if n, err := seq.ReadAt(got, 12); n != 4 || err != nil || got[0] != 0x11 {
		t.Fatalf("cache end %d %v %x calls %d", n, err, got, src.calls)
	}
	// Eight bytes from offset 12 extend past the cached window.
	past := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	n, err := seq.ReadAt(past, 12)
	if n != 4 || !errors.Is(err, io.EOF) {
		t.Fatalf("past window %d %v %x", n, err, past)
	}

	short := newSeqReader(shortNilReader{}, 32)
	defer short.release()
	buf := make([]byte, 8)
	n, err = short.ReadAt(buf, 0)
	if n != 1 || !errors.Is(err, io.EOF) || buf[0] != 0x5A {
		t.Fatalf("short nil %d %v %x", n, err, buf)
	}
}

// poisonAfter serves data on the first ReadAt and 0xFF after that.
type poisonAfter struct {
	data  []byte
	calls int
}

func (p *poisonAfter) ReadAt(dst []byte, off int64) (int, error) {
	p.calls++
	if off < 0 || off >= int64(len(p.data)) {
		return 0, io.EOF
	}
	n := copy(dst, p.data[off:])
	if p.calls > 1 {
		for i := 0; i < n; i++ {
			dst[i] = 0xFF
		}
	}
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

// shortNilReader returns one byte and a nil error, which io.ReaderAt does not.
type shortNilReader struct{}

func (shortNilReader) ReadAt(p []byte, _ int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = 0x5A
	return 1, nil
}
