package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"runtime"
	"testing"
	"time"
)

func TestDeinterleaveAlignmentOddCounts(t *testing.T) {
	sizes := []int{1, 2, 4, 8, 16}
	counts := []int{1, 2, 3, 7, 17}
	skews := []int{0, 1, 2, 3, 7, 9}
	for _, size := range sizes {
		for _, n := range counts {
			for _, skew := range skews {
				for _, extra := range []int{0, 1, 3, size} {
					stride := size + extra
					need := (n-1)*stride + size
					backing := make([]byte, skew+need+8)
					for i := range backing {
						backing[i] = byte(i*17 + size + n + skew)
					}
					src := backing[skew : skew+need]
					dst := make([]byte, n*size)
					want := make([]byte, n*size)
					refDeinterleave(want, src, size, stride, n)
					deinterleave(dst, src, size, stride, n)
					if !bytes.Equal(dst, want) {
						t.Fatalf("size %d n %d skew %d stride %d", size, n, skew, stride)
					}
				}
			}
		}
	}
}

func refDeinterleave(dst, src []byte, size, stride, n int) {
	for i := 0; i < n; i++ {
		copy(dst[i*size:(i+1)*size], src[i*stride:i*stride+size])
	}
}

func TestReadValuesOnePass(t *testing.T) {
	const n = 100
	const chans = 4
	cols := make([][]float64, chans)
	for c := 0; c < chans; c++ {
		cols[c] = make([]float64, n)
		for i := range cols[c] {
			cols[c][i] = float64(c*1000 + i)
		}
	}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	var ptrs []*Channel
	names := []string{"a", "b", "c", "d"}
	for c := 0; c < chans; c++ {
		ch := mustCh(t, w, "g", names[c], TypeFloat64)
		if err := ch.Write(cols[c]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	// A string channel cannot share an interleaved segment. It is a later
	// contiguous segment and must still round-trip through ReadValues.
	w.SetInterleaved(false)
	strCh := mustCh(t, w, "other", "s", TypeString)
	if err := strCh.Write([]string{"x", "yz"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	cr := &offsetReader{r: bytes.NewReader(raw), min: math.MaxInt64}
	f, err := NewReader(cr)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	for _, name := range names {
		ch, _ := g.Channel(name)
		ptrs = append(ptrs, ch)
	}
	cr.n = 0
	sep := int64(0)
	for c, ch := range ptrs {
		v, err := ch.Float64s()
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, v, cols[c])
		sep = cr.n
	}
	cr.n = 0
	got, err := ReadValues(ptrs)
	if err != nil {
		t.Fatal(err)
	}
	if cr.n >= sep/2 {
		t.Fatalf("one-pass read %d bytes, separate reads %d", cr.n, sep)
	}
	for c := range cols {
		assertEqual(t, got[c], cols[c])
	}
	// Duplicate request still returns independent slices from one read.
	dup, err := ReadValues([]*Channel{ptrs[0], ptrs[0]})
	if err != nil {
		t.Fatal(err)
	}
	dup[0].([]float64)[0] = -1
	if dup[1].([]float64)[0] == -1 {
		t.Fatal("duplicate ReadValues slices alias")
	}
	gv, err := g.Values()
	if err != nil || len(gv) != chans {
		t.Fatalf("group values %d %v", len(gv), err)
	}
	og, _ := f.Group("other")
	ov, err := og.Values()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, ov[0], []string{"x", "yz"})

	// Contiguous channels are not re-read as a multiplex.
	var cbuf bytes.Buffer
	cw := newBufWriter(t, &cbuf)
	ca := mustCh(t, cw, "g", "a", TypeInt16)
	cb := mustCh(t, cw, "g", "b", TypeFloat64)
	if err := ca.Write([]int16{1, 2, 3}); err != nil || cb.Write([]float64{4, 5}) != nil {
		t.Fatal(err)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}
	cf := openBytes(t, cbuf.Bytes())
	cg, _ := cf.Group("g")
	gotC, err := cg.Values()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, gotC[0], []int16{1, 2, 3})
	assertEqual(t, gotC[1], []float64{4, 5})

	if _, err := ReadValues([]*Channel{ptrs[0], nil}); !errors.Is(err, ErrArgument) {
		t.Fatalf("nil channel: %v", err)
	}
}

func TestMixedWidthOnePassOdd(t *testing.T) {
	// uint8 at column 0, int16 at column 1, float64 at column 3. Odd count.
	// The one-pass window slices at those columns, so the word path must fall
	// back rather than load an unaligned address.
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	a := mustCh(t, w, "g", "a", TypeUint8)
	b := mustCh(t, w, "g", "b", TypeInt16)
	c := mustCh(t, w, "g", "c", TypeFloat64)
	if err := a.Write([]uint8{9, 8, 7}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]int16{-1, 2, 300}); err != nil {
		t.Fatal(err)
	}
	if err := c.Write([]float64{1.5, -2.5, 3.5}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	ca, _ := g.Channel("a")
	cb, _ := g.Channel("b")
	cc, _ := g.Channel("c")
	got, err := ReadValues([]*Channel{ca, cb, cc})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], []uint8{9, 8, 7})
	assertEqual(t, got[1], []int16{-1, 2, 300})
	assertEqual(t, got[2], []float64{1.5, -2.5, 3.5})
	assertEqual(t, mustValues(t, cb), []int16{-1, 2, 300})
}

func TestWidthFileRoundTripOdd(t *testing.T) {
	cases := []struct {
		dt   DataType
		vals any
	}{
		{TypeUint8, []uint8{1, 2, 3}},
		{TypeInt16, []int16{-4, 5, 6}},
		{TypeInt32, []int32{-7, 8, 9}},
		{TypeFloat64, []float64{1.25, 2.5, 3.75}},
		{TypeComplex128, []complex128{complex(1, 2), complex(3, 4), complex(5, 6)}},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		w := newBufWriter(t, &buf)
		w.SetInterleaved(true)
		lead := mustCh(t, w, "g", "lead", TypeUint8)
		ch := mustCh(t, w, "g", "v", tc.dt)
		if err := lead.Write([]uint8{0, 0, 0}); err != nil {
			t.Fatal(err)
		}
		if err := ch.Write(tc.vals); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		f := openBytes(t, buf.Bytes())
		g, _ := f.Group("g")
		gotC, _ := g.Channel("v")
		all, err := ReadValues([]*Channel{g.channels[0], gotC})
		if err != nil {
			t.Fatalf("%s: %v", tc.dt, err)
		}
		assertEqual(t, all[1], tc.vals)
		assertEqual(t, mustValues(t, gotC), tc.vals)
	}
}

func TestEndianMatrix(t *testing.T) {
	ts := Timestamp{Seconds: 5, Fraction: 99}
	ext := Extended{Significand: 0x8000000000000001, Exponent: 0x3FFF}
	type row struct {
		dt   DataType
		vals any
		be   func() []byte
	}
	rows := []row{
		{TypeInt8, []int8{-1, 2}, func() []byte { return []byte{0xFF, 0x02} }},
		{TypeUint8, []uint8{0, 255}, func() []byte { return []byte{0x00, 0xFF} }},
		{TypeBool, []bool{true, false}, func() []byte { return []byte{0x02, 0x00} }},
		{TypeInt16, []int16{-2, 300}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, int16(-2))
			binary.Write(&b, binary.BigEndian, int16(300))
			return b.Bytes()
		}},
		{TypeUint16, []uint16{1, 400}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, uint16(1))
			binary.Write(&b, binary.BigEndian, uint16(400))
			return b.Bytes()
		}},
		{TypeInt32, []int32{-3, 0x01020304}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, int32(-3))
			binary.Write(&b, binary.BigEndian, int32(0x01020304))
			return b.Bytes()
		}},
		{TypeUint32, []uint32{5, 0x0A0B0C0D}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, uint32(5))
			binary.Write(&b, binary.BigEndian, uint32(0x0A0B0C0D))
			return b.Bytes()
		}},
		{TypeFloat32, []float32{-1.5, 2.5}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, float32(-1.5))
			binary.Write(&b, binary.BigEndian, float32(2.5))
			return b.Bytes()
		}},
		{TypeInt64, []int64{-4, 0x0102030405060708}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, int64(-4))
			binary.Write(&b, binary.BigEndian, int64(0x0102030405060708))
			return b.Bytes()
		}},
		{TypeUint64, []uint64{6, 0x0102030405060708}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, uint64(6))
			binary.Write(&b, binary.BigEndian, uint64(0x0102030405060708))
			return b.Bytes()
		}},
		{TypeFloat64, []float64{-3.5, 4.5}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, float64(-3.5))
			binary.Write(&b, binary.BigEndian, float64(4.5))
			return b.Bytes()
		}},
		{TypeComplex64, []complex64{complex(1, -2)}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, float32(1))
			binary.Write(&b, binary.BigEndian, float32(-2))
			return b.Bytes()
		}},
		{TypeComplex128, []complex128{complex(3, 4)}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, float64(3))
			binary.Write(&b, binary.BigEndian, float64(4))
			return b.Bytes()
		}},
		{TypeTimestamp, []Timestamp{ts}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, int64(5))
			binary.Write(&b, binary.BigEndian, uint64(99))
			return b.Bytes()
		}},
		{TypeExtendedFloat, []Extended{ext}, func() []byte {
			var b bytes.Buffer
			binary.Write(&b, binary.BigEndian, uint16(0x3FFF))
			binary.Write(&b, binary.BigEndian, uint64(0x8000000000000001))
			return b.Bytes()
		}},
	}
	for _, r := range rows {
		var buf bytes.Buffer
		w := newBufWriter(t, &buf)
		ch := mustCh(t, w, "g", "c", r.dt)
		if err := ch.Write(r.vals); err != nil {
			t.Fatalf("le %s: %v", r.dt, err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		f := openBytes(t, buf.Bytes())
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		assertEqual(t, mustValues(t, c), r.vals)

		file := beChannelFile(t, r.dt, 2, r.be())
		if r.dt == TypeComplex64 || r.dt == TypeComplex128 || r.dt == TypeTimestamp || r.dt == TypeExtendedFloat {
			file = beChannelFile(t, r.dt, 1, r.be())
		}
		bf := openBytes(t, file)
		bg, _ := bf.Group("g")
		bc, _ := bg.Channel("c")
		assertEqual(t, mustValues(t, bc), r.vals)
		cur := bc.Cursor(1)
		if !cur.Next() {
			t.Fatalf("be cursor %s: %v", r.dt, cur.Err())
		}
		_ = cur.Values()
		cur.Close()
	}

	// Big-endian interleaved int16 + float64, odd count, float64 column unaligned.
	var raw bytes.Buffer
	for i, pair := range []struct {
		i int16
		f float64
	}{{-1, 1.5}, {2, -2.5}, {300, 3.5}} {
		_ = i
		binary.Write(&raw, binary.BigEndian, pair.i)
		binary.Write(&raw, binary.BigEndian, pair.f)
	}
	meta := beObjects(t, []beObj{
		{"/'g'/'a'", TypeInt16, 3},
		{"/'g'/'b'", TypeFloat64, 3},
	})
	file := beLead(TocMetaData|TocNewObjList|TocRawData|TocInterleavedData|TocBigEndian, meta, raw.Bytes())
	f := openBytes(t, file)
	g, _ := f.Group("g")
	a, _ := g.Channel("a")
	b, _ := g.Channel("b")
	got, err := ReadValues([]*Channel{a, b})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], []int16{-1, 2, 300})
	assertEqual(t, got[1], []float64{1.5, -2.5, 3.5})
	assertEqual(t, mustValues(t, b), []float64{1.5, -2.5, 3.5})
}

type beObj struct {
	path string
	dt   DataType
	n    uint64
}

func beObjects(t *testing.T, objs []beObj) []byte {
	t.Helper()
	var meta bytes.Buffer
	bo := binary.BigEndian
	binary.Write(&meta, bo, uint32(len(objs)))
	for _, o := range objs {
		binary.Write(&meta, bo, uint32(len(o.path)))
		meta.WriteString(o.path)
		binary.Write(&meta, bo, uint32(20))
		binary.Write(&meta, bo, uint32(o.dt))
		binary.Write(&meta, bo, uint32(1))
		binary.Write(&meta, bo, o.n)
		binary.Write(&meta, bo, uint32(0))
	}
	return meta.Bytes()
}

func beChannelFile(t *testing.T, dt DataType, n uint64, raw []byte) []byte {
	t.Helper()
	return beLead(TocMetaData|TocNewObjList|TocRawData|TocBigEndian, beObjects(t, []beObj{{"/'g'/'c'", dt, n}}), raw)
}

func beLead(toc uint32, meta, raw []byte) []byte {
	file := make([]byte, 28, 28+len(meta)+len(raw))
	copy(file[0:4], "TDSm")
	binary.LittleEndian.PutUint32(file[4:8], toc)
	binary.BigEndian.PutUint32(file[8:12], Version20)
	binary.BigEndian.PutUint64(file[12:20], uint64(len(meta)+len(raw)))
	binary.BigEndian.PutUint64(file[20:28], uint64(len(meta)))
	file = append(file, meta...)
	file = append(file, raw...)
	return file
}

func TestSliceAndCursorAt(t *testing.T) {
	const n = 1000
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
	// Second segment so a tail can start past the first span.
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	tail := make([]float64, 80)
	for i := range tail {
		tail[i] = float64(1000 + i)
	}
	if err := ch.Write(tail); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	cr := &offsetReader{r: bytes.NewReader(raw), min: math.MaxInt64}
	f, err := NewReader(cr)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	cr.n, cr.min = 0, math.MaxInt64
	got, err := c.Slice(len(data)+len(tail)-64, 64)
	if err != nil {
		t.Fatal(err)
	}
	all := append(append([]float64{}, data...), tail...)
	assertEqual(t, got, all[len(all)-64:])
	if cr.n > 64*8+32 {
		t.Fatalf("tail read %d bytes", cr.n)
	}
	if cr.min < int64(len(raw)/2) {
		t.Fatalf("tail min offset %d of %d", cr.min, len(raw))
	}
	if _, err := c.Slice(-1, 1); !errors.Is(err, ErrArgument) {
		t.Fatalf("neg: %v", err)
	}
	if _, err := c.Slice(n, n); !errors.Is(err, ErrArgument) {
		t.Fatalf("overflow range: %v", err)
	}

	cur := c.CursorAt(n+80-64, 64, 16)
	var streamed []float64
	for cur.Next() {
		streamed = append(streamed, cur.Values().([]float64)...)
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, streamed, got)
	bad := c.CursorAt(-1, 1, 4)
	if bad.Next() || !errors.Is(bad.Err(), ErrArgument) {
		t.Fatalf("bad cursor %v", bad.Err())
	}

	// Interleaved tail starts at the multiplexed window, not byte 0.
	const m = 200
	const k = 4
	var ib bytes.Buffer
	iw := newBufWriter(t, &ib)
	iw.SetInterleaved(true)
	for c := 0; c < k; c++ {
		col := make([]float64, m)
		for i := range col {
			col[i] = float64(c*m + i)
		}
		ch := mustCh(t, iw, "g", string(rune('a'+c)), TypeFloat64)
		if err := ch.Write(col); err != nil {
			t.Fatal(err)
		}
	}
	if err := iw.Close(); err != nil {
		t.Fatal(err)
	}
	iraw := ib.Bytes()
	ic := &offsetReader{r: bytes.NewReader(iraw), min: math.MaxInt64}
	inf, err := NewReader(ic)
	if err != nil {
		t.Fatal(err)
	}
	defer inf.Close()
	ig, _ := inf.Group("g")
	ich, _ := ig.Channel("c")
	ic.n, ic.min = 0, math.MaxInt64
	isl, err := ich.Slice(m-64, 64)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]float64, 64)
	for i := range want {
		want[i] = float64(2*m + (m - 64 + i))
	}
	assertEqual(t, isl, want)
	// One channel's window is about 64 * 4 * 8 bytes, not the whole span.
	if ic.n > 64*4*8+64 {
		t.Fatalf("interleaved tail read %d bytes", ic.n)
	}
	if ic.min < int64(len(iraw)/2) {
		t.Fatalf("interleaved tail min %d of %d", ic.min, len(iraw))
	}
}

func TestStringSliceSkipsPrefix(t *testing.T) {
	const n = 40
	vals := make([]string, n)
	for i := range vals {
		vals[i] = string(bytes.Repeat([]byte{byte('a' + i%26)}, 100))
	}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "s", TypeString)
	if err := ch.Write(vals[:20]); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write(vals[20:]); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	cr := &offsetReader{r: bytes.NewReader(raw), min: math.MaxInt64}
	f, err := NewReader(cr)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	c, _ := g.Channel("s")
	cr.n = 0
	got, err := c.Slice(n-2, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, vals[n-2:])
	// Two 100-byte strings plus a handful of offset entries, not both chunks.
	if cr.n > 1000 {
		t.Fatalf("string tail read %d bytes", cr.n)
	}
	all, err := c.Strings()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, all, vals)
}

func TestFormatLimits(t *testing.T) {
	// Offset table longer than 4 GiB: n > MaxUint32/4. No raw payload.
	nTab := uint64(math.MaxUint32/4) + 1
	file := stringIndexFile(t, nTab, nTab*4)
	if _, err := NewReader(bytes.NewReader(file)); err == nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("offset table: %v", err)
	}
	// Payload longer than 4 GiB. total = 4*n + payload.
	file = stringIndexFile(t, 1, uint64(math.MaxUint32)+1+4)
	if _, err := NewReader(bytes.NewReader(file)); err == nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("string block: %v", err)
	}
	// Exactly 4 GiB of payload is the uint32 limit and must not be reported as over.
	file = stringIndexFile(t, 1, uint64(math.MaxUint32)+4)
	if _, err := NewReader(bytes.NewReader(file)); err != nil {
		t.Fatalf("exact 4 GiB payload: %v", err)
	}

	// n * width does not fit in int64. Header only; nothing is allocated for samples.
	huge := uint64(math.MaxInt64/4) + 1
	file = fixedIndexFile(t, TypeInt32, huge, false, nil)
	if _, err := NewReader(bytes.NewReader(file)); err == nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("overflow chunk: %v", err)
	}

	// 2^31 float64 samples described in the index, three present in an
	// incomplete segment. A 32-bit n*8 would wrap; Len must be 3.
	samples := []float64{1, 2, 3}
	var raw bytes.Buffer
	binary.Write(&raw, binary.LittleEndian, samples)
	file = fixedIndexFile(t, TypeFloat64, 1<<31, true, raw.Bytes())
	f, err := NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	if c.Len() != 3 {
		t.Fatalf("len %d", c.Len())
	}
	assertEqual(t, mustValues(t, c), samples)
	sl, err := c.Slice(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, sl, []float64{2, 3})
}

func stringIndexFile(t *testing.T, n, total uint64) []byte {
	t.Helper()
	var meta bytes.Buffer
	p := "/'g'/'s'"
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint32(len(p)))
	meta.WriteString(p)
	binary.Write(&meta, binary.LittleEndian, uint32(28))
	binary.Write(&meta, binary.LittleEndian, uint32(TypeString))
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, n)
	binary.Write(&meta, binary.LittleEndian, total)
	binary.Write(&meta, binary.LittleEndian, uint32(0))
	return finishSegment(t, TocMetaData|TocNewObjList, meta.Bytes(), nil)
}

func fixedIndexFile(t *testing.T, dt DataType, n uint64, incomplete bool, raw []byte) []byte {
	t.Helper()
	var meta bytes.Buffer
	p := "/'g'/'c'"
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint32(len(p)))
	meta.WriteString(p)
	binary.Write(&meta, binary.LittleEndian, uint32(20))
	binary.Write(&meta, binary.LittleEndian, uint32(dt))
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, n)
	binary.Write(&meta, binary.LittleEndian, uint32(0))
	if incomplete {
		file := make([]byte, 28, 28+meta.Len()+len(raw))
		copy(file, encodeLeadIn(TocMetaData|TocNewObjList|TocRawData, incompleteSegment, uint64(meta.Len())))
		file = append(file, meta.Bytes()...)
		file = append(file, raw...)
		return file
	}
	return finishSegment(t, TocMetaData|TocNewObjList, meta.Bytes(), nil)
}

func TestCursorPoolLifecycle(t *testing.T) {
	const n = 200
	data := make([]float64, n)
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	f := openBytes(t, raw)
	g, _ := f.Group("g")
	c, _ := g.Channel("c")

	// Early Close of many cursors returns every buffer. The counter is the
	// signal: sync.Pool may drop a buffer at GC, so a later Get is not proof.
	const many = 32
	puts := readBufPuts.Load()
	curs := make([]*Cursor, many)
	for i := range curs {
		curs[i] = c.Cursor(16)
		if !curs[i].Next() {
			t.Fatal(curs[i].Err())
		}
	}
	if readBufPuts.Load() != puts {
		t.Fatal("buffer returned before Close")
	}
	for _, cur := range curs {
		cur.Close()
	}
	if got := readBufPuts.Load() - puts; got != many {
		t.Fatalf("close returned %d buffers, want %d", got, many)
	}

	// Running off the end returns the buffer.
	puts = readBufPuts.Load()
	cur := c.Cursor(n)
	if !cur.Next() || cur.Next() {
		t.Fatalf("end err %v", cur.Err())
	}
	if readBufPuts.Load()-puts != 1 {
		t.Fatalf("end returned %d", readBufPuts.Load()-puts)
	}

	// A failed Next returns the buffer.
	puts = readBufPuts.Load()
	f.r = boomReader{}
	cur = c.Cursor(16)
	if cur.Next() || cur.Err() == nil {
		t.Fatal("expected read error")
	}
	if cur.seq != nil {
		t.Fatal("seq still held after error")
	}
	if readBufPuts.Load()-puts != 1 {
		t.Fatalf("error returned %d", readBufPuts.Load()-puts)
	}
	f.r = bytes.NewReader(raw)

	// Dropping a cursor still returns the buffer from the finalizer.
	puts = readBufPuts.Load()
	func() {
		cur := c.Cursor(16)
		if !cur.Next() {
			t.Fatal(cur.Err())
		}
		runtime.KeepAlive(cur)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		if readBufPuts.Load() > puts {
			return
		}
	}
	t.Fatal("finalizer did not return the buffer")
}

type boomReader struct{}

func (boomReader) ReadAt([]byte, int64) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestFloat64sCopySurvivesNext(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write([]float64{1, 2, 3, 4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	cur := c.Cursor(3)
	if !cur.Next() {
		t.Fatal(cur.Err())
	}
	owned, err := cur.Float64sCopy()
	if err != nil {
		t.Fatal(err)
	}
	alias, err := cur.Float64s()
	if err != nil {
		t.Fatal(err)
	}
	if &owned[0] == &alias[0] {
		t.Fatal("copy shares the batch")
	}
	if !cur.Next() {
		t.Fatal(cur.Err())
	}
	if owned[0] != 1 || owned[2] != 3 {
		t.Fatalf("copy changed to %v", owned)
	}
	if alias[0] == 1 {
		t.Fatal("aliasing Float64s did not follow the batch")
	}
	cur.Close()
}

func TestScaledWaveformDoesNotMutate(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.SetProperty("NI_Scale[1]_Scale_Type", "Linear"); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("NI_Scale[1]_Linear_Slope", 2.0); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty("NI_Scale[1]_Linear_Y_Intercept", 0.5); err != nil {
		t.Fatal(err)
	}
	in := []float64{1, 2, 3}
	if err := ch.Write(in); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c := mustChannel(t, f, "g", "c")
	before, err := c.Float64s()
	if err != nil {
		t.Fatal(err)
	}
	sw, err := ScaledWaveform[float64](c)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, sw.Y, []float64{2.5, 4.5, 6.5})
	sw.Y[0] = 99
	after, err := c.Float64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, after, before)
	raw, err := ReadWaveform[float64](c)
	if err != nil {
		t.Fatal(err)
	}
	y0 := raw.Y[0]
	scaled := raw.ApplyScale(LinearScale{Slope: 2, Intercept: 0.5})
	if raw.Y[0] != y0 {
		t.Fatal("ApplyScale mutated Y")
	}
	if scaled.Y[0] == y0 && y0 != 0 {
		t.Fatal("scale was a no-op")
	}
	// Integer channel: one conversion, source unchanged.
	var ib bytes.Buffer
	iw := newBufWriter(t, &ib)
	ich := mustCh(t, iw, "g", "c", TypeInt16)
	if err := ich.Write([]int16{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if err := iw.Close(); err != nil {
		t.Fatal(err)
	}
	ic := mustChannel(t, openBytes(t, ib.Bytes()), "g", "c")
	got, err := ScaledWaveform[int16](ic)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got.Y, []float64{4, 5, 6})
	rawI, err := ic.Values()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, rawI, []int16{4, 5, 6})
}

func TestWriteCopiesSlice(t *testing.T) {
	data := []float64{1, 2, 3}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.Write(data); err != nil {
		t.Fatal(err)
	}
	data[0] = 9
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	more := []float64{5, 6}
	extra := []float64{7}
	if err := ch.Write(more); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write(extra); err != nil {
		t.Fatal(err)
	}
	more[0] = 0
	extra[0] = 0
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 3, 5, 6, 7})

	// Bool values are copied at Write.
	var bb bytes.Buffer
	bw := newBufWriter(t, &bb)
	bc := mustCh(t, bw, "g", "b", TypeBool)
	flags := []bool{true, false}
	if err := bc.Write(flags); err != nil {
		t.Fatal(err)
	}
	flags[0] = false
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	bf := openBytes(t, bb.Bytes())
	bg, _ := bf.Group("g")
	bch, _ := bg.Channel("b")
	assertEqual(t, mustValues(t, bch), []bool{true, false})
}

// TestWriteRefillDoesNotCorrupt is the buffer-reuse case that used to keep
// the caller's first slice: refilling it and writing again replaced the
// already buffered chunk, so the file read back as [10 11 12 10 11 12 20 21 22].
func TestWriteRefillDoesNotCorrupt(t *testing.T) {
	buf := []int32{0, 1, 2}
	var out bytes.Buffer
	w := newBufWriter(t, &out)
	ch := mustCh(t, w, "g", "c", TypeInt32)
	for _, base := range []int32{0, 10, 20} {
		for i := range buf {
			buf[i] = base + int32(i)
		}
		if err := ch.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, out.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []int32{0, 1, 2, 10, 11, 12, 20, 21, 22})
}

func TestWriteNoCopyRetainsUntilFlush(t *testing.T) {
	data := []float64{1, 2, 3}
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	if err := ch.WriteNoCopy(data); err != nil {
		t.Fatal(err)
	}
	// The first slice is retained. Mutating it before the next write changes
	// the buffered chunk.
	data[0] = 9
	more := []float64{5, 6}
	if err := ch.WriteNoCopy(more); err != nil {
		t.Fatal(err)
	}
	// The second call copied both slices into an owned buffer.
	data[0] = 4
	more[0] = 0
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{9, 2, 3, 5, 6})
}

func TestMetaCacheSkipsRebuild(t *testing.T) {
	before := metaBuilds.Load()
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	const flushes = 8
	for i := 0; i < flushes; i++ {
		if err := ch.Write([]float64{1, 2, 3, 4}); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	builds := metaBuilds.Load() - before
	// The first segment publishes the object list. A second segment may
	// still differ (emitted flags). Later identical chunks reuse the encoding.
	if builds >= flushes {
		t.Fatalf("encoded metadata %d times across %d identical flushes", builds, flushes)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	if c.Len() != flushes*4 {
		t.Fatalf("len %d", c.Len())
	}
}

func TestMetaCacheKeepsProperties(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	for i := 0; i < 3; i++ {
		if err := ch.Write([]float64{1, 2}); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := ch.SetProperty("unit_string", "V"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []float64{1, 2, 1, 2, 1, 2, 1, 2})
	u, ok := c.Unit()
	if !ok || u != "V" {
		t.Fatalf("unit %q %v", u, ok)
	}
}

func TestCrossGroupOnePass(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	w.SetInterleaved(true)
	a := mustCh(t, w, "g1", "a", TypeFloat32)
	b := mustCh(t, w, "g2", "b", TypeFloat32)
	if err := a.Write([]float32{1, 2, 3}); err != nil || b.Write([]float32{4, 5, 6}) != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g1, _ := f.Group("g1")
	g2, _ := f.Group("g2")
	ca, _ := g1.Channel("a")
	cb, _ := g2.Channel("b")
	got, err := ReadValues([]*Channel{ca, cb})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], []float32{1, 2, 3})
	assertEqual(t, got[1], []float32{4, 5, 6})
}

type offsetReader struct {
	r   *bytes.Reader
	n   int64
	min int64
}

func (o *offsetReader) ReadAt(p []byte, off int64) (int, error) {
	if off < o.min {
		o.min = off
	}
	o.n += int64(len(p))
	return o.r.ReadAt(p, off)
}

func (o *offsetReader) Size() int64 { return o.r.Size() }
