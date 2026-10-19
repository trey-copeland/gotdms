package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"testing"
	"time"
)

func TestStreamRoundTripChunks(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.SetProperty(PropUnitString, "V"); err != nil {
		t.Fatal(err)
	}
	const n = 10
	s, err := ch.Stream(n)
	if err != nil {
		t.Fatal(err)
	}
	parts := [][]float64{{1, 2, 3}, {4, 5}, {6, 7, 8, 9, 10}}
	for _, p := range parts {
		if err := s.Write(ch, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Finish(); err != nil {
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
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	u, ok := c.Unit()
	if !ok || u != "V" {
		t.Fatalf("unit %q %v", u, ok)
	}
	if binary.LittleEndian.Uint64(buf.Bytes()[12:20]) == incompleteSegment {
		t.Fatal("finished non-seekable segment left the incomplete sentinel")
	}
}

func TestStreamSeekableRoundTrip(t *testing.T) {
	f, err := os.CreateTemp("", "tdms-stream-*.tdms")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	defer os.Remove(name)
	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	if !w.seekable {
		t.Fatal("temp file should be seekable")
	}
	ch := mustCh(t, w, "g", "c", TypeInt32)
	s, err := ch.Stream(5)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []int32{1, 2}); err != nil || s.Write(ch, []int32{3, 4, 5}) != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	rf, err := Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	g, _ := rf.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []int32{1, 2, 3, 4, 5})
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint64(raw[12:20]) == incompleteSegment {
		t.Fatal("Finish did not patch the lead-in")
	}
}

func TestStreamMultiChannelContiguous(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	b := mustCh(t, w, "g", "b", TypeFloat32)
	a := mustCh(t, w, "g", "a", TypeFloat32)
	// Creation order is b then a. Stream's argument order follows the caller
	// and is rewritten to creation order.
	s, err := w.Stream([]*ChannelWriter{a, b}, []int{2, 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(a, []float32{1}); err == nil || !errors.Is(err, ErrLayout) {
		t.Fatalf("out of order: %v", err)
	}
	if err := s.Write(b, []float32{10, 11}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(b, []float32{12, 13}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(a, []float32{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	ca, _ := g.Channel("a")
	cb, _ := g.Channel("b")
	assertEqual(t, mustValues(t, ca), []float32{1, 2})
	assertEqual(t, mustValues(t, cb), []float32{10, 11, 12, 13})
}

func TestStreamInterleaved(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	a := mustCh(t, w, "g", "a", TypeInt16)
	b := mustCh(t, w, "g", "b", TypeFloat64)
	s, err := w.Stream([]*ChannelWriter{a, b}, []int{4, 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(a, []int16{1}); err == nil || !errors.Is(err, ErrLayout) {
		t.Fatalf("single write: %v", err)
	}
	if err := s.WriteInterleaved([]int16{1, 2}, []float64{10}); err == nil || !errors.Is(err, ErrLayout) {
		t.Fatalf("unequal: %v", err)
	}
	if err := s.WriteInterleaved([]int16{1, 2}, []float64{10, 11}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteInterleaved([]int16{3, 4}, []float64{12, 13}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	ca, _ := g.Channel("a")
	cb, _ := g.Channel("b")
	assertEqual(t, mustValues(t, ca), []int16{1, 2, 3, 4})
	assertEqual(t, mustValues(t, cb), []float64{10, 11, 12, 13})
}

func TestStreamUnequalInterleaveRejected(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	a := mustCh(t, w, "g", "a", TypeFloat64)
	b := mustCh(t, w, "g", "b", TypeFloat64)
	_, err := w.Stream([]*ChannelWriter{a, b}, []int{3, 4})
	if !errors.Is(err, ErrLayout) {
		t.Fatal(err)
	}
}

func TestStreamSeekableAppendBothOrders(t *testing.T) {
	f, err := os.CreateTemp("", "tdms-append-*.tdms")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	defer os.Remove(name)
	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write([]float64{1, 2}); err != nil || w.Flush() != nil {
		t.Fatal(err)
	}
	s, err := ch.Stream(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{3, 4}); err != nil || s.Finish() != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{5}); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	rf, err := Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	g, _ := rf.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3, 4, 5})
}

func TestStreamAppendBothOrders(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write([]float64{1, 2}); err != nil || w.Flush() != nil {
		t.Fatal(err)
	}
	s, err := ch.Stream(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{3, 4, 5}); err != nil || s.Finish() != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{6}); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3, 4, 5, 6})
}

func TestStreamWaveform(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	start := NewTimestamp(time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC))
	s, err := ch.StreamWaveform(start, 0.001, "V", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{0.1, 0.2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{0.3, 0.4}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	wf, err := ReadWaveform[float64](c)
	if err != nil {
		t.Fatal(err)
	}
	if !wf.HasStart() || wf.Start != start || !wf.HasIncrement() || wf.Increment != 0.001 || wf.Unit != "V" {
		t.Fatalf("header %+v", wf)
	}
	assertEqual(t, wf.Y, []float64{0.1, 0.2, 0.3, 0.4})
}

func TestStreamStringUnsupported(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeString)
	_, err := ch.Stream(2)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if err := ch.Write([]string{"a", "bb"}); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []string{"a", "bb"})
}

func TestStreamBoolAndTimestamp(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	b := mustCh(t, w, "g", "b", TypeBool)
	ts := mustCh(t, w, "g", "t", TypeTimestamp)
	sb, err := b.Stream(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.Write(b, []bool{true, false, true}); err != nil || sb.Finish() != nil {
		t.Fatal(err)
	}
	when := []Timestamp{{Seconds: 1, Fraction: 2}, {Seconds: 3, Fraction: 4}}
	st, err := ts.Stream(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ts, when); err != nil || st.Finish() != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	cb, _ := g.Channel("b")
	ct, _ := g.Channel("t")
	assertEqual(t, mustValues(t, cb), []bool{true, false, true})
	assertEqual(t, mustValues(t, ct), when)
}

func TestStreamPropertiesMidStream(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	s, err := ch.Stream(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("unit_string", "V"); err == nil || !errors.Is(err, ErrArgument) {
		t.Fatalf("set during stream: %v", err)
	}
	if err := w.SetProperty("title", "x"); err == nil || !errors.Is(err, ErrArgument) {
		t.Fatalf("file prop: %v", err)
	}
	if err := ch.Write([]float64{1}); err == nil || !errors.Is(err, ErrArgument) {
		t.Fatalf("buffered write: %v", err)
	}
	if err := w.Flush(); err == nil || !errors.Is(err, ErrArgument) {
		t.Fatalf("flush: %v", err)
	}
	if err := s.Write(ch, []float64{1, 2}); err != nil || s.Finish() != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("note", "after"); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	if _, ok := c.Property("unit_string"); ok {
		t.Fatal("property set during the stream was stored")
	}
	p, ok := c.Property("note")
	if !ok || p.Value != "after" {
		t.Fatalf("note %+v %v", p, ok)
	}
}

func TestStreamDoesNotRetainChunk(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	s, err := ch.Stream(2)
	if err != nil {
		t.Fatal(err)
	}
	chunk := []float64{1, 2}
	if err := s.Write(ch, chunk); err != nil {
		t.Fatal(err)
	}
	chunk[0] = 99
	if err := s.Finish(); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2})
}

func TestStreamWritesBeforeFinish(t *testing.T) {
	cw := &countBuf{}
	w, err := NewWriter(cw, WithWriteBuffer(1024))
	if err != nil {
		t.Fatal(err)
	}
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	const n = 4096 // 32 KiB, well past the 1 KiB buffer
	s, err := ch.Stream(n)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]float64, 512) // 4 KiB
	for i := 0; i < n; i += len(chunk) {
		if err := s.Write(ch, chunk); err != nil {
			t.Fatal(err)
		}
	}
	before := cw.n
	// The 1 KiB buffer must have spilled sample bytes during Write. Holding
	// the segment until Finish would still show only a partial buffer here.
	if before < 1024+n*8/2 {
		t.Fatalf("only %d bytes reached the writer before Finish; payload is %d", before, n*8)
	}
	if err := s.Finish(); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	if cw.n < before {
		t.Fatalf("count went backwards %d -> %d", before, cw.n)
	}
	if cw.b.Len() != cw.n {
		t.Fatalf("count %d buffer %d", cw.n, cw.b.Len())
	}
	f := openBytes(t, cw.b.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	if c.Len() != n {
		t.Fatalf("len %d", c.Len())
	}
}

func TestStreamShortSeekableTruncates(t *testing.T) {
	f, err := os.CreateTemp("", "tdms-short-*.tdms")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	defer os.Remove(name)
	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	s, err := ch.Stream(8)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	err = s.Finish()
	if err == nil || !errors.Is(err, ErrArgument) {
		t.Fatalf("finish: %v", err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("close succeeded after a short stream")
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint64(raw[12:20]) != incompleteSegment {
		t.Fatalf("short stream was patched closed: %x", raw[12:20])
	}
	rf, err := Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	g, _ := rf.Group("g")
	c, _ := g.Channel("c")
	if c.Len() != 4 {
		t.Fatalf("len %d, want the values that were written", c.Len())
	}
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3, 4})
}

func TestStreamShortNonSeekableTruncated(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	s, err := ch.Stream(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err == nil || !errors.Is(err, ErrArgument) {
		t.Fatalf("finish: %v", err)
	}
	if _, err := NewReader(bytes.NewReader(buf.Bytes())); err == nil || !errors.Is(err, ErrTruncated) {
		t.Fatalf("reader: %v", err)
	}
}

func TestStreamWriterError(t *testing.T) {
	lw := &limitWriter{lim: 1500}
	w, err := NewWriter(lw, WithWriteBuffer(256))
	if err != nil {
		t.Fatal(err)
	}
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	s, err := ch.Stream(4096)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]float64, 128)
	var werr error
	for i := 0; i < 20 && werr == nil; i++ {
		werr = s.Write(ch, chunk)
	}
	if werr == nil {
		t.Fatal("expected a write error")
	}
	if err := s.Write(ch, chunk); err == nil {
		t.Fatal("write after failure succeeded")
	}
	if err := s.Finish(); err == nil {
		t.Fatal("finish after failure succeeded")
	}
	if _, err := NewReader(bytes.NewReader(lw.b.Bytes())); err == nil {
		// A torn non-seekable segment must not open as the full declared channel.
		t.Fatal("torn stream opened as a valid file")
	}
}

func TestStreamCloseFinishes(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeInt16)
	s, err := ch.Stream(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []int16{7, 8, 9}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []int16{7, 8, 9})
}

func TestStreamPendingBlocks(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write([]float64{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Stream(2); err == nil || !errors.Is(err, ErrArgument) {
		t.Fatalf("stream with pending data: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	s, err := ch.Stream(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ch, []float64{2}); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2})
}

func TestStreamCountOverflow(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	_, err := ch.Stream(math.MaxInt/8 + 1)
	if err == nil || !errors.Is(err, ErrArgument) {
		t.Fatal(err)
	}
}

func TestStreamSetInterleavedDoesNotRetarget(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	a := mustCh(t, w, "g", "a", TypeFloat64)
	b := mustCh(t, w, "g", "b", TypeFloat64)
	s, err := w.Stream([]*ChannelWriter{a, b}, []int{2, 1})
	if err != nil {
		t.Fatal(err)
	}
	w.SetInterleaved(true)
	if err := s.WriteInterleaved([]float64{1}, []float64{2}); err == nil {
		t.Fatal("contiguous stream accepted WriteInterleaved after SetInterleaved")
	}
	if err := s.Write(a, []float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(b, []float64{3}); err != nil || s.Finish() != nil || w.Close() != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	ca, _ := g.Channel("a")
	cb, _ := g.Channel("b")
	assertEqual(t, mustValues(t, ca), []float64{1, 2})
	assertEqual(t, mustValues(t, cb), []float64{3})
}

type countBuf struct {
	n int
	b bytes.Buffer
}

func (c *countBuf) Write(p []byte) (int, error) {
	n, err := c.b.Write(p)
	c.n += n
	return n, err
}

type limitWriter struct {
	n, lim int
	b      bytes.Buffer
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n >= l.lim {
		return 0, io.ErrShortWrite
	}
	if l.n+len(p) > l.lim {
		p = p[:l.lim-l.n]
		n, err := l.b.Write(p)
		l.n += n
		if err != nil {
			return n, err
		}
		return n, io.ErrShortWrite
	}
	n, err := l.b.Write(p)
	l.n += n
	return n, err
}
