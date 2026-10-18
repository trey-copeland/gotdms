package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRoundTripTypes(t *testing.T) {
	ext1 := ExtendedFromFloat64(1)
	ext2 := ExtendedFromFloat64(-2)
	ts := []Timestamp{
		{Seconds: 0, Fraction: 0},
		{Seconds: -1, Fraction: 1 << 63},
		{Seconds: 2082844800, Fraction: 0},
		{Seconds: math.MinInt64 + 3_000_000_000, Fraction: ^uint64(0)},
	}
	samples := []struct {
		dt     DataType
		values any
	}{
		{TypeInt8, []int8{-128, -1, 0, 127}},
		{TypeInt16, []int16{-32768, -1, 0, 32767}},
		{TypeInt32, []int32{math.MinInt32, -2, 0, 1, math.MaxInt32}},
		{TypeInt64, []int64{math.MinInt64, -1, 0, math.MaxInt64}},
		{TypeUint8, []uint8{0, 1, 255}},
		{TypeUint16, []uint16{0, 1, 65535}},
		{TypeUint32, []uint32{0, 1, math.MaxUint32}},
		{TypeUint64, []uint64{0, 1, math.MaxUint64}},
		{TypeFloat32, []float32{float32(math.Inf(-1)), -0.5, 0, 1, float32(math.MaxFloat32)}},
		{TypeFloat64, []float64{math.Inf(1), math.Copysign(0, -1), 1.5, math.MaxFloat64}},
		{TypeBool, []bool{true, false, true}},
		{TypeString, []string{"", "a", "ab", "héllo", "你好", "a\x00b"}},
		{TypeTimestamp, ts},
		{TypeExtendedFloat, []Extended{ext1, ext2, {}}},
		{TypeFloat32WithUnit, []float32{1, 2, 3}},
		{TypeFloat64WithUnit, []float64{1.25, 2.5}},
		{TypeExtendedFloatWithUnit, []Extended{ext1}},
		{TypeComplex64, []complex64{complex(1, -2), complex(0, 0)}},
		{TypeComplex128, []complex128{complex(1.5, 2.5)}},
	}
	for _, s := range samples {
		s := s
		t.Run(s.dt.String(), func(t *testing.T) {
			for _, interleaved := range []bool{false, true} {
				if interleaved && s.dt == TypeString {
					// Multi-channel string interleave is rejected. A single
					// string channel may carry the flag; that case is separate.
					continue
				}
				t.Run(layoutName(interleaved), func(t *testing.T) {
					for _, segs := range []int{1, 2} {
						buf := writeSample(t, s.dt, s.values, interleaved, segs)
						f := openBytes(t, buf)
						got := readSample(t, f, s.dt, interleaved)
						want := s.values
						if segs == 2 {
							want = twice(s.values)
						}
						assertEqual(t, got, want)
						if interleaved {
							g, _ := f.Group("g")
							other, _ := g.Channel("other")
							if other.Len() != sliceLen(want) {
								t.Fatalf("other len %d", other.Len())
							}
						}
					}
				})
			}
		})
	}
}

func TestRoundTripSingleStringInterleaved(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	ch := mustCh(t, w, "g", "s", TypeString)
	if err := ch.Write([]string{"one", "", "three"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(buf.Bytes()[4:8])&TocInterleavedData == 0 {
		t.Fatal("expected interleaved flag on a single string channel")
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("s")
	assertEqual(t, mustValues(t, c), []string{"one", "", "three"})
}

func TestInterleavedStringsRejected(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	a := mustCh(t, w, "g", "a", TypeString)
	b := mustCh(t, w, "g", "b", TypeString)
	if err := a.Write([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]string{"b"}); err != nil {
		t.Fatal(err)
	}
	err := w.Close()
	if !errors.Is(err, ErrLayout) {
		t.Fatalf("got %v", err)
	}
}

func TestPropertiesAllLevels(t *testing.T) {
	start := time.Date(2020, 4, 22, 21, 43, 16, 609444000, time.UTC)
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	if err := w.SetProperty("author", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := w.SetProperty("NI_Number", int32(3)); err != nil {
		t.Fatal(err)
	}
	g := w.Group("Dr. T's Events")
	if err := g.SetProperty("title", "demo"); err != nil {
		t.Fatal(err)
	}
	ch := mustCh(t, w, "Dr. T's Events", "温度", TypeFloat64)
	if err := ch.SetProperty(PropUnitString, "V"); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropIncrement, 0.001); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropStartTime, start); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropSamples, uint32(4)); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("NI_Scale[1]_Scale_Type", "Linear"); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("NI_Scale[1]_Linear_Slope", 1.693433e-9); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("NI_Scale[1]_Linear_Y_Intercept", 0.0); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("NI_Scale[1]_Linear_Input_Source", uint32(0)); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("flag", true); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	// Second segment overwrites the file author and appends samples.
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.SetProperty("author", "grace"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{5}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	author, ok := f.Property("author")
	if !ok || author.Value != "grace" {
		t.Fatalf("author %+v", author)
	}
	if n, _ := f.Property("NI_Number"); n.Value != int32(3) {
		t.Fatalf("NI_Number %+v", n)
	}
	gr, ok := f.Group("Dr. T's Events")
	if !ok {
		t.Fatal("group")
	}
	if title, _ := gr.Property("title"); title.Value != "demo" {
		t.Fatalf("title %+v", title)
	}
	c, ok := gr.Channel("温度")
	if !ok {
		t.Fatal("channel")
	}
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3, 4, 5})
	if u, ok := c.Unit(); !ok || u != "V" {
		t.Fatalf("unit %q %v", u, ok)
	}
	if inc, ok := c.Increment(); !ok || inc != 0.001 {
		t.Fatalf("increment %v %v", inc, ok)
	}
	gotStart, ok := c.StartTime()
	if !ok || !gotStart.Equal(start) {
		t.Fatalf("start %v %v", gotStart, ok)
	}
	if n, ok := c.SampleCount(); !ok || n != 4 {
		t.Fatalf("samples %d %v", n, ok)
	}
	sc, ok := c.LinearScale()
	if !ok || sc.Index != 1 || sc.Intercept != 0 || sc.Slope != 1.693433e-9 {
		t.Fatalf("scale %+v %v", sc, ok)
	}
	if sc.Apply(2) != 2*sc.Slope {
		t.Fatal(sc.Apply(2))
	}
	if flag, _ := c.Property("flag"); flag.Value != true {
		t.Fatalf("flag %+v", flag)
	}
}

func TestEmptyObjects(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	if f.SegmentCount() != 1 || len(f.Groups()) != 0 {
		t.Fatalf("empty file segments %d groups %d", f.SegmentCount(), len(f.Groups()))
	}

	buf.Reset()
	w = newBufWriter(t, &buf)
	w.Group("alone")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f = openBytes(t, buf.Bytes())
	g, ok := f.Group("alone")
	if !ok || len(g.Channels()) != 0 {
		t.Fatalf("empty group ok=%v channels=%d", ok, len(g.Channels()))
	}

	buf.Reset()
	w = newBufWriter(t, &buf)
	if _, err := w.Group("g").Channel("empty", TypeInt32); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f = openBytes(t, buf.Bytes())
	g, _ = f.Group("g")
	c, ok := g.Channel("empty")
	if !ok || c.Len() != 0 {
		t.Fatalf("empty channel ok=%v len=%d", ok, c.Len())
	}
	v, err := c.Values()
	if err != nil || v != nil {
		t.Fatalf("values %#v err %v", v, err)
	}
}

func TestChangingSegments(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	a := mustCh(t, w, "g", "a", TypeInt32)
	b := mustCh(t, w, "g", "b", TypeInt32)
	if err := a.Write([]int32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]int32{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	// Identical layout: second segment has raw data and no metadata.
	if err := a.Write([]int32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]int32{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	// Property change keeps the object list and rewrites one index as "matches previous".
	if err := a.SetProperty("prop", "error"); err != nil {
		t.Fatal(err)
	}
	if err := a.Write([]int32{7, 8, 9}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]int32{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	// New channel appended. kTocNewObjList stays clear.
	c := mustCh(t, w, "g", "voltage", TypeInt32)
	if err := a.Write([]int32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]int32{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := c.Write([]int32{7, 8, 9, 10, 11}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	// Channel b disappears. This requires a new object list.
	if err := a.Write([]int32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := c.Write([]int32{7, 8, 9, 10, 11}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	leads := scanLeads(t, buf.Bytes())
	if len(leads) != 5 {
		t.Fatalf("segments %d", len(leads))
	}
	if leads[0].toc&TocNewObjList == 0 || leads[0].toc&TocMetaData == 0 || leads[0].toc&TocRawData == 0 {
		t.Fatalf("seg0 toc %#x", leads[0].toc)
	}
	if leads[1].toc != TocRawData {
		t.Fatalf("seg1 toc %#x, want raw only", leads[1].toc)
	}
	if leads[2].toc&TocNewObjList != 0 || leads[2].toc&TocMetaData == 0 {
		t.Fatalf("seg2 toc %#x", leads[2].toc)
	}
	if leads[3].toc&TocNewObjList != 0 {
		t.Fatalf("appended channel should reuse the object list, toc %#x", leads[3].toc)
	}
	if leads[4].toc&TocNewObjList == 0 {
		t.Fatalf("removed channel should set kTocNewObjList, toc %#x", leads[4].toc)
	}
	// Next-segment offsets were patched, not left as the incomplete sentinel.
	for i, ld := range leads {
		if ld.next == incompleteSegment {
			t.Fatalf("seg %d sentinel not patched", i)
		}
		if ld.off+leadInSize+int(ld.next) != ld.end {
			t.Fatalf("seg %d next %d does not reach %d (start %d)", i, ld.next, ld.end, ld.off)
		}
	}

	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	ca, _ := g.Channel("a")
	cb, _ := g.Channel("b")
	cc, _ := g.Channel("voltage")
	assertEqual(t, mustValues(t, ca), []int32{1, 2, 3, 1, 2, 3, 7, 8, 9, 1, 2, 3, 1, 2, 3})
	assertEqual(t, mustValues(t, cb), []int32{4, 5, 6, 4, 5, 6, 4, 5, 6, 4, 5, 6})
	assertEqual(t, mustValues(t, cc), []int32{7, 8, 9, 10, 11, 7, 8, 9, 10, 11})
	if p, _ := ca.Property("prop"); p.Value != "error" {
		t.Fatalf("prop %+v", p)
	}
}

func TestSeekableMatchesBuffer(t *testing.T) {
	write := func(w *Writer) {
		ch := mustCh(t, w, "g", "c", TypeInt32)
		if err := ch.SetProperty("u", "x"); err != nil {
			t.Fatal(err)
		}
		if err := ch.Write([]int32{1, 2, 3}); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := ch.Write([]int32{4, 5}); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	write(newBufWriter(t, &buf))
	mem := &memWS{}
	w, err := NewWriter(mem)
	if err != nil {
		t.Fatal(err)
	}
	if !w.seekable {
		t.Fatal("memWS should be seekable")
	}
	write(w)
	if !bytes.Equal(buf.Bytes(), mem.b) {
		t.Fatalf("seekable and buffered writers differ\nbuf %x\nmem %x", buf.Bytes(), mem.b)
	}
}

func TestCreateOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2})
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestCursorFloat64sAliasesBatch(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	vals := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	if err := ch.Write(vals); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c, _ := f.Group("g")
	chR, _ := c.Channel("c")
	cur := chR.Cursor(4)
	if !cur.Next() {
		t.Fatal(cur.Err())
	}
	held, err := cur.Float64s()
	if err != nil {
		t.Fatal(err)
	}
	if held[0] != 1 {
		t.Fatalf("first batch %v", held)
	}
	if !cur.Next() {
		t.Fatal(cur.Err())
	}
	// The cursor reuses one []float64. Callers that keep Float64s across Next
	// observe the next batch, which is why the doc says to copy.
	if held[0] == 1 {
		t.Fatal("float64 Float64s did not alias the reused batch")
	}
	if held[0] != 5 || held[3] != 8 {
		t.Fatalf("aliased batch = %v", held)
	}
}

func TestMixedWidthInterleaved(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	a := mustCh(t, w, "g", "a", TypeInt16)
	b := mustCh(t, w, "g", "b", TypeFloat64)
	if err := a.Write([]int16{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]float64{10, 20, 30, 40}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	gotA, _ := g.Channel("a")
	gotB, _ := g.Channel("b")
	assertEqual(t, mustValues(t, gotA), []int16{1, 2, 3, 4})
	assertEqual(t, mustValues(t, gotB), []float64{10, 20, 30, 40})
}

func TestCursorMatchesValues(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeInt32)
	vals := make([]int32, 1000)
	for i := range vals {
		vals[i] = int32(i)
	}
	// Two writes, one segment, then a second segment.
	if err := ch.Write(vals[:400]); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write(vals[400:]); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write(vals); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c, _ := f.Group("g")
	chR, _ := c.Channel("c")
	var got []int32
	cur := chR.Cursor(64)
	for cur.Next() {
		got = append(got, cur.Values().([]int32)...)
		f64, err := cur.Float64s()
		if err != nil || len(f64) != cur.Len() {
			t.Fatalf("batch %d err %v", cur.Len(), err)
		}
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	want := append(append([]int32{}, vals...), vals...)
	assertEqual(t, got, want)
	asF, err := chR.Float64s()
	if err != nil || len(asF) != len(want) || asF[10] != 10 {
		t.Fatalf("float64s %d %v", len(asF), err)
	}
}

func TestTimestampEdges(t *testing.T) {
	epoch := time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []time.Time{
		epoch,
		epoch.Add(time.Second),
		epoch.Add(-time.Second),
		epoch.Add(-500 * time.Millisecond),
		epoch.Add(1500 * time.Millisecond),
		time.Unix(0, 0).UTC(),
		time.Unix(0, 1).UTC(),
		time.Date(1800, 6, 15, 12, 30, 1, 1, time.UTC),
		time.Date(2200, 1, 1, 0, 0, 0, 999999999, time.UTC),
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cases = append(cases, time.Date(2024, 7, 4, 12, 0, 0, 123456789, loc))
	for _, tm := range cases {
		ts := NewTimestamp(tm)
		got, ok := ts.Time()
		if !ok || !got.Equal(tm.UTC()) {
			t.Fatalf("%s -> %+v -> %s ok=%v", tm.UTC(), ts, got, ok)
		}
	}
	// Unix epoch is 2_082_844_800 TDMS seconds. 66 years, 17 of them leap days.
	if ts := NewTimestamp(time.Unix(0, 0)); ts.Seconds != 2082844800 || ts.Fraction != 0 {
		t.Fatalf("unix epoch %+v", ts)
	}
	// Half a second before the TDMS epoch borrows a second.
	half := NewTimestamp(epoch.Add(-500 * time.Millisecond))
	if half.Seconds != -1 || half.Fraction != 1<<63 {
		t.Fatalf("half before epoch %+v", half)
	}
	// Raw fraction bits survive a file round trip. time.Time cannot hold them.
	raw := Timestamp{Seconds: -5, Fraction: 1}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "t", TypeTimestamp)
	if err := ch.Write([]Timestamp{raw}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("t")
	got, err := c.Timestamps()
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != raw {
		t.Fatalf("%+v", got[0])
	}
	// Every nanosecond in a few windows round-trips. The conversion nudges the
	// 2^-64 fraction so Time returns the same nanosecond.
	for _, base := range []int{0, 1, 500_000_000, 999_999_000} {
		for ns := base; ns < base+1000 && ns < 1_000_000_000; ns++ {
			in := epoch.Add(time.Duration(ns))
			out, ok := NewTimestamp(in).Time()
			if !ok || !out.Equal(in) {
				t.Fatalf("ns %d -> %s", ns, out)
			}
		}
	}
}

func TestExtendedFloat64(t *testing.T) {
	for _, f64 := range []float64{0, math.Copysign(0, -1), 1, -1, 2, -2, 0.5, math.MaxFloat64, math.SmallestNonzeroFloat64, math.Inf(1), math.Inf(-1)} {
		e := ExtendedFromFloat64(f64)
		got := e.Float64()
		if math.Float64bits(got) != math.Float64bits(f64) && !(math.IsNaN(got) && math.IsNaN(f64)) {
			t.Fatalf("%v -> %+v -> %v", f64, e, got)
		}
	}
	if !math.IsNaN(ExtendedFromFloat64(math.NaN()).Float64()) {
		t.Fatal("nan")
	}
}

func TestTypeMismatchAndClosed(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeInt32)
	if err := ch.Write([]int{1, 2}); !errors.Is(err, ErrType) {
		t.Fatalf("write []int: %v", err)
	}
	if _, err := w.Group("g").Channel("c", TypeFloat64); !errors.Is(err, ErrType) {
		t.Fatalf("retype: %v", err)
	}
	if err := ch.Write([]int32{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.SetProperty("a", "b"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	if _, err := c.Strings(); !errors.Is(err, ErrType) {
		t.Fatalf("strings: %v", err)
	}
	if _, err := c.Int64s(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := c.Values(); !errors.Is(err, ErrClosed) {
		t.Fatalf("read closed: %v", err)
	}
}

func TestDifferentWidthsAndCounts(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	a := mustCh(t, w, "g", "a", TypeInt16)
	b := mustCh(t, w, "g", "b", TypeFloat64)
	if err := a.Write([]int16{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]float64{1.5, 2.5, 3.5}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	ca, _ := g.Channel("a")
	cb, _ := g.Channel("b")
	assertEqual(t, mustValues(t, ca), []int16{1, 2, 3})
	assertEqual(t, mustValues(t, cb), []float64{1.5, 2.5, 3.5})

	buf.Reset()
	w = newBufWriter(t, &buf)
	a = mustCh(t, w, "g", "a", TypeInt32)
	b = mustCh(t, w, "g", "b", TypeInt32)
	if err := a.Write([]int32{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]int32{3, 4, 5, 6, 7}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f = openBytes(t, buf.Bytes())
	g, _ = f.Group("g")
	ca, _ = g.Channel("a")
	cb, _ = g.Channel("b")
	assertEqual(t, mustValues(t, ca), []int32{1, 2})
	assertEqual(t, mustValues(t, cb), []int32{3, 4, 5, 6, 7})
}

func TestUint64Int64Overflow(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeUint64)
	if err := ch.Write([]uint64{math.MaxUint64}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c, _ := f.Group("g")
	chR, _ := c.Channel("c")
	if _, err := chR.Int64s(); !errors.Is(err, ErrType) {
		t.Fatalf("%v", err)
	}
}

func TestPathRoundTrip(t *testing.T) {
	cases := []struct{ group, channel string }{
		{"", ""},
		{"a/b", "c/d"},
		{"it's", "x'y'z"},
		{" ", "channel 1"},
	}
	for _, c := range cases {
		kind, g, ch, err := parseObjectPath(channelPath(c.group, c.channel))
		if err != nil || kind != kindChannel || g != c.group || ch != c.channel {
			t.Fatalf("%q/%q -> %d %q %q %v", c.group, c.channel, kind, g, ch, err)
		}
	}
	if kind, _, _, err := parseObjectPath("/"); err != nil || kind != kindFile {
		t.Fatal(err)
	}
	if _, _, _, err := parseObjectPath("/'unterminated"); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestBoolNonZeroAndFixedPoint(t *testing.T) {
	var meta bytes.Buffer
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	p := "/'g'/'b'"
	binary.Write(&meta, binary.LittleEndian, uint32(len(p)))
	meta.WriteString(p)
	binary.Write(&meta, binary.LittleEndian, uint32(20))
	binary.Write(&meta, binary.LittleEndian, uint32(TypeBool))
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint64(1))
	binary.Write(&meta, binary.LittleEndian, uint32(0))
	file := finishSegment(t, TocMetaData|TocNewObjList|TocRawData, meta.Bytes(), []byte{2})
	f := openBytes(t, file)
	g, _ := f.Group("g")
	c, _ := g.Channel("b")
	assertEqual(t, mustValues(t, c), []bool{true})

	meta.Reset()
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint32(len(p)))
	meta.WriteString(p)
	binary.Write(&meta, binary.LittleEndian, uint32(20))
	binary.Write(&meta, binary.LittleEndian, uint32(TypeFixedPoint))
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint64(1))
	binary.Write(&meta, binary.LittleEndian, uint32(0))
	file = finishSegment(t, TocMetaData|TocNewObjList|TocRawData, meta.Bytes(), []byte{0, 0, 0, 0})
	_, err := NewReader(bytes.NewReader(file))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("fixed point: %v", err)
	}
}

func TestConstants(t *testing.T) {
	if TocMetaData != 1<<1 || TocNewObjList != 1<<2 || TocRawData != 1<<3 ||
		TocInterleavedData != 1<<5 || TocBigEndian != 1<<6 || TocDAQmxRawData != 1<<7 {
		t.Fatal("ToC bits")
	}
	if Version20 != 4713 || Version10 != 4712 {
		t.Fatal("version")
	}
	// 69 12 00 00 and 69 13 00 00 as little-endian uint32.
	if RawIndexDAQmxFormatChanging != 0x1269 || RawIndexDAQmxDigitalLine != 0x1369 {
		t.Fatal("daqmx markers")
	}
	if TypeString != 0x20 || TypeBool != 0x21 || TypeTimestamp != 0x44 {
		t.Fatal("type codes")
	}
	if TypeFloat32WithUnit != 0x19 || TypeFloat64WithUnit != 0x1A || TypeExtendedFloatWithUnit != 0x1B {
		t.Fatal("unit types")
	}
}

type leadScan struct {
	off  int
	toc  uint32
	ver  uint32
	next uint64
	raw  uint64
	end  int
}

func scanLeads(t *testing.T, b []byte) []leadScan {
	t.Helper()
	var out []leadScan
	for off := 0; off+leadInSize <= len(b); {
		if string(b[off:off+4]) != "TDSm" {
			t.Fatalf("tag at %d: %q", off, b[off:off+4])
		}
		ld := leadScan{
			off:  off,
			toc:  binary.LittleEndian.Uint32(b[off+4:]),
			ver:  binary.LittleEndian.Uint32(b[off+8:]),
			next: binary.LittleEndian.Uint64(b[off+12:]),
			raw:  binary.LittleEndian.Uint64(b[off+20:]),
		}
		if ld.ver != Version20 {
			t.Fatalf("ver %d", ld.ver)
		}
		if ld.next == incompleteSegment {
			ld.end = len(b)
			out = append(out, ld)
			break
		}
		ld.end = off + leadInSize + int(ld.next)
		if ld.end > len(b) || ld.end < off {
			t.Fatalf("bad end %d", ld.end)
		}
		out = append(out, ld)
		if ld.end == off {
			t.Fatal("zero-length advance")
		}
		off = ld.end
	}
	return out
}

func writeSample(t *testing.T, dt DataType, values any, interleaved bool, segs int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(interleaved)
	ch := mustCh(t, w, "g", "c", dt)
	var other *ChannelWriter
	if interleaved {
		other = mustCh(t, w, "g", "other", dt)
	}
	for i := 0; i < segs; i++ {
		if err := ch.Write(values); err != nil {
			t.Fatal(err)
		}
		if other != nil {
			if err := other.Write(values); err != nil {
				t.Fatal(err)
			}
		}
		if i+1 < segs {
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readSample(t *testing.T, f *File, dt DataType, interleaved bool) any {
	t.Helper()
	g, ok := f.Group("g")
	if !ok {
		t.Fatal("group")
	}
	c, ok := g.Channel("c")
	if !ok {
		t.Fatal("channel")
	}
	if c.DataType() != dt {
		t.Fatalf("type %s", c.DataType())
	}
	return mustValues(t, c)
}

func newBufWriter(t *testing.T, buf *bytes.Buffer) *Writer {
	t.Helper()
	w, err := NewWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	if w.seekable {
		t.Fatal("bytes.Buffer should not be seekable")
	}
	return w
}

func mustCh(t *testing.T, w *Writer, group, name string, dt DataType) *ChannelWriter {
	t.Helper()
	ch, err := w.Group(group).Channel(name, dt)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func layoutName(interleaved bool) string {
	if interleaved {
		return "interleaved"
	}
	return "contiguous"
}

func twice(v any) any {
	rv := reflect.ValueOf(v)
	out := reflect.MakeSlice(rv.Type(), rv.Len()*2, rv.Len()*2)
	reflect.Copy(out, rv)
	reflect.Copy(out.Slice(rv.Len(), out.Len()), rv)
	return out.Interface()
}

type memWS struct {
	b   []byte
	pos int
}

func (m *memWS) Write(p []byte) (int, error) {
	end := m.pos + len(p)
	if end > len(m.b) {
		m.b = append(m.b, make([]byte, end-len(m.b))...)
	}
	copy(m.b[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *memWS) Seek(off int64, whence int) (int64, error) {
	var np int
	switch whence {
	case io.SeekStart:
		np = int(off)
	case io.SeekCurrent:
		np = m.pos + int(off)
	case io.SeekEnd:
		np = len(m.b) + int(off)
	default:
		return 0, errors.New("bad whence")
	}
	if np < 0 {
		return 0, errors.New("negative seek")
	}
	m.pos = np
	return int64(np), nil
}
