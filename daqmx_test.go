package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Hand-built DAQmx segments. The bytes are the NI raw-data index
// (TDMS File Format Internal Structure, Nov 12 2024) plus the three index
// words this package accepts. They are not produced by Writer and not copied
// from npTDMS. testdata/nptdms/harness.py is the separate raw-value oracle.

const daqTOC = TocMetaData | TocNewObjList | TocRawData | TocDAQmxRawData

const (
	dmNone = iota
	dmMatch
	dmStandard
	dmDAQmx
)

const (
	propStr = iota
	propF64
	propU32
)

type dmProp struct {
	name string
	kind int
	str  string
	f64  float64
	u32  uint32
}

type dmScaler struct {
	id, typ, off, buf, bitmap uint32
}

type dmObj struct {
	path      string
	kind      int
	dt        DataType
	n         uint64
	header    uint32
	widths    []uint32
	scalers   []dmScaler
	props     []dmProp
	forceType bool
	dataType  uint32
	setDim    bool
	arrayDim  uint32
}

func chDAQmx(path string, n uint64, header uint32, widths []uint32, scalers []dmScaler, props ...dmProp) dmObj {
	return dmObj{path: path, kind: dmDAQmx, n: n, header: header, widths: widths, scalers: scalers, props: props}
}

func objNone(path string, props ...dmProp) dmObj {
	return dmObj{path: path, kind: dmNone, props: props}
}

func objMatch(path string) dmObj { return dmObj{path: path, kind: dmMatch} }

func objStd(path string, dt DataType, n uint64) dmObj {
	return dmObj{path: path, kind: dmStandard, dt: dt, n: n}
}

func buildDAQmx(toc uint32, objs []dmObj, raw []byte) []byte {
	var bo binary.ByteOrder = binary.LittleEndian
	if toc&TocBigEndian != 0 {
		bo = binary.BigEndian
	}
	var meta []byte
	if toc&TocMetaData != 0 {
		var b bytes.Buffer
		putU32(&b, bo, uint32(len(objs)))
		for _, o := range objs {
			writeDMObj(&b, bo, o)
		}
		meta = b.Bytes()
	}
	if toc&TocBigEndian != 0 {
		return beLead(toc, meta, raw)
	}
	file := make([]byte, 28, 28+len(meta)+len(raw))
	copy(file, encodeLeadIn(toc, uint64(len(meta)+len(raw)), uint64(len(meta))))
	file = append(file, meta...)
	file = append(file, raw...)
	return file
}

func writeDMObj(b *bytes.Buffer, bo binary.ByteOrder, o dmObj) {
	putU32(b, bo, uint32(len(o.path)))
	b.WriteString(o.path)
	switch o.kind {
	case dmMatch:
		putU32(b, bo, RawIndexMatchesPrevious)
	case dmNone:
		putU32(b, bo, RawIndexNoData)
	case dmStandard:
		putU32(b, bo, 20)
		putU32(b, bo, uint32(o.dt))
		putU32(b, bo, 1)
		putU64(b, bo, o.n)
	default:
		header := o.header
		if header == 0 {
			header = RawIndexDAQmxFormatChanging
		}
		putU32(b, bo, header)
		dt := uint32(TypeDAQmxRaw)
		if o.forceType {
			dt = o.dataType
		}
		putU32(b, bo, dt)
		dim := uint32(1)
		if o.setDim {
			dim = o.arrayDim
		}
		putU32(b, bo, dim)
		putU64(b, bo, o.n)
		putU32(b, bo, uint32(len(o.scalers)))
		digital := header == RawIndexDAQmxDigitalObserved
		for _, sc := range o.scalers {
			putU32(b, bo, sc.typ)
			putU32(b, bo, sc.buf)
			putU32(b, bo, sc.off)
			if digital {
				b.WriteByte(byte(sc.bitmap))
			} else {
				putU32(b, bo, sc.bitmap)
			}
			putU32(b, bo, sc.id)
		}
		putU32(b, bo, uint32(len(o.widths)))
		for _, w := range o.widths {
			putU32(b, bo, w)
		}
	}
	putU32(b, bo, uint32(len(o.props)))
	for _, p := range o.props {
		writeDMProp(b, bo, p)
	}
}

func writeDMProp(b *bytes.Buffer, bo binary.ByteOrder, p dmProp) {
	putU32(b, bo, uint32(len(p.name)))
	b.WriteString(p.name)
	switch p.kind {
	case propStr:
		putU32(b, bo, uint32(TypeString))
		putU32(b, bo, uint32(len(p.str)))
		b.WriteString(p.str)
	case propF64:
		putU32(b, bo, uint32(TypeFloat64))
		var buf [8]byte
		bo.PutUint64(buf[:], math.Float64bits(p.f64))
		b.Write(buf[:])
	case propU32:
		putU32(b, bo, uint32(TypeUint32))
		putU32(b, bo, p.u32)
	default:
		panic(p.kind)
	}
}

func putU32(b *bytes.Buffer, bo binary.ByteOrder, v uint32) {
	var buf [4]byte
	bo.PutUint32(buf[:], v)
	b.Write(buf[:])
}

func putU64(b *bytes.Buffer, bo binary.ByteOrder, v uint64) {
	var buf [8]byte
	bo.PutUint64(buf[:], v)
	b.Write(buf[:])
}

func i16Raw(vals ...int16) []byte {
	var b bytes.Buffer
	for _, v := range vals {
		var buf [2]byte
		binary.LittleEndian.PutUint16(buf[:], uint16(v))
		b.Write(buf[:])
	}
	return b.Bytes()
}

func u16Raw(vals ...uint16) []byte {
	var b bytes.Buffer
	for _, v := range vals {
		var buf [2]byte
		binary.LittleEndian.PutUint16(buf[:], v)
		b.Write(buf[:])
	}
	return b.Bytes()
}

func markIncomplete(file []byte) {
	for i := 12; i < 20; i++ {
		file[i] = 0xFF
	}
}

func daqCh(t *testing.T, f *File, group, name string) *Channel {
	t.Helper()
	g, ok := f.Group(group)
	if !ok {
		t.Fatalf("missing group %s", group)
	}
	c, ok := g.Channel(name)
	if !ok {
		t.Fatal("missing channel", name)
	}
	return c
}

func mustOpenErr(t *testing.T, file []byte, target error) {
	t.Helper()
	_, err := NewReader(bytes.NewReader(file))
	if !errors.Is(err, target) {
		t.Fatalf("got %v, want %v", err, target)
	}
}

func daqI16(header uint32, n uint64) dmObj {
	return chDAQmx("/'g'/'c'", n, header, []uint32{2}, []dmScaler{{id: 0, typ: 3, off: 0}})
}

// The first segment carries kTocDAQmxRawData. That is the Firefly HSAI shape:
// one raw buffer, format-changing scalers, native integer samples.
func TestDAQmxFormatChangingI16(t *testing.T) {
	raw := i16Raw(1, 2, -1, -2)
	for _, header := range []uint32{RawIndexDAQmxFormatChanging, RawIndexDAQmxDigitalLine} {
		file := buildDAQmx(daqTOC, []dmObj{daqI16(header, 4)}, raw)
		f := openBytes(t, file)
		if f.SegmentCount() != 1 {
			t.Fatalf("segments %d", f.SegmentCount())
		}
		c := daqCh(t, f, "g", "c")
		if c.DataType() != TypeInt16 || c.Len() != 4 {
			t.Fatalf("type %s len %d", c.DataType(), c.Len())
		}
		assertEqual(t, mustValues(t, c), []int16{1, 2, -1, -2})
		got, err := c.Slice(1, 2)
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, got, []int16{2, -1})
		f64, err := c.Float64s()
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, f64, []float64{1, 2, -1, -2})
		info, ok := c.DAQmx()
		if !ok || info.Header != header || info.Primary != 0 || len(info.Widths) != 1 || info.Widths[0] != 2 {
			t.Fatalf("daqmx %+v ok %v", info, ok)
		}
		sc := info.Scalers[0]
		if sc.Digital || sc.DataType != TypeInt16 || sc.ByteOffset != 0 || sc.FormatBitmap != 0 || sc.ScaleID != 0 {
			t.Fatalf("scaler %+v", sc)
		}
		cur := c.Cursor(3)
		var batched []int16
		for cur.Next() {
			batched = append(batched, append([]int16(nil), cur.Values().([]int16)...)...)
		}
		if err := cur.Err(); err != nil {
			t.Fatal(err)
		}
		cur.Close()
		assertEqual(t, batched, []int16{1, 2, -1, -2})
	}
}

func TestDAQmxNativeWidths(t *testing.T) {
	cases := []struct {
		name   string
		typ    uint32
		width  uint32
		raw    []byte
		want   any
		wantDT DataType
	}{
		{"u8", 0, 1, []byte{0, 1, 255}, []uint8{0, 1, 255}, TypeUint8},
		{"i8", 1, 1, []byte{1, 0xFF}, []int8{1, -1}, TypeInt8},
		{"u16", 2, 2, u16Raw(1, 2, 65535, 65534), []uint16{1, 2, 65535, 65534}, TypeUint16},
		{"i32", 5, 4, le32(1, 2, 0xFFFFFFFF, 0xFFFFFFFE), []int32{1, 2, -1, -2}, TypeInt32},
		{"u32", 4, 4, le32(1, 2, 0xFFFFFFFF, 0xFFFFFFFE), []uint32{1, 2, 0xFFFFFFFF, 0xFFFFFFFE}, TypeUint32},
		{"u64", 6, 8, le64(1, ^uint64(0)), []uint64{1, ^uint64(0)}, TypeUint64},
		{"i64", 7, 8, le64(1, ^uint64(0)), []int64{1, -1}, TypeInt64},
		{"f32", 8, 4, f32Raw(1.5, -2.25), []float32{1.5, -2.25}, TypeFloat32},
		{"f64", 9, 8, f64Raw(1.5, -2.25), []float64{1.5, -2.25}, TypeFloat64},
		{"time", 0xFFFFFFFF, 16, tsRaw(5), []Timestamp{{Seconds: 5}}, TypeTimestamp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := uint64(1)
			if sz, ok := tc.wantDT.Size(); ok && sz > 0 {
				n = uint64(len(tc.raw) / sz)
			}
			obj := chDAQmx("/'g'/'c'", n, RawIndexDAQmxFormatChanging, []uint32{tc.width}, []dmScaler{{typ: tc.typ}})
			f := openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, tc.raw))
			c := daqCh(t, f, "g", "c")
			if c.DataType() != tc.wantDT {
				t.Fatalf("type %s", c.DataType())
			}
			assertEqual(t, mustValues(t, c), tc.want)
		})
	}
}

func TestDAQmxInterleavedAndReadValues(t *testing.T) {
	// Two int16 channels share one 4-byte stride. kTocInterleavedData does
	// not change the DAQmx buffer layout. ReadValues splits that one buffer.
	raw := i16Raw(1, 17, 2, 18, 3, 19, 4, 20)
	ch1 := chDAQmx("/'g'/'a'", 4, 0, []uint32{4}, []dmScaler{{typ: 3, off: 0}})
	ch2 := chDAQmx("/'g'/'b'", 4, 0, []uint32{4}, []dmScaler{{typ: 3, off: 2}})
	toc := daqTOC | TocInterleavedData
	f := openBytes(t, buildDAQmx(toc, []dmObj{ch1, ch2}, raw))
	g, _ := f.Group("g")
	a, b := daqCh(t, f, "g", "a"), daqCh(t, f, "g", "b")
	assertEqual(t, mustValues(t, a), []int16{1, 2, 3, 4})
	assertEqual(t, mustValues(t, b), []int16{17, 18, 19, 20})
	got, err := ReadValues([]*Channel{a, b})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], []int16{1, 2, 3, 4})
	assertEqual(t, got[1], []int16{17, 18, 19, 20})
	all, err := g.Values()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, all[0], []int16{1, 2, 3, 4})
	assertEqual(t, all[1], []int16{17, 18, 19, 20})

	// Same bytes without the DAQmx ToC bit still use the raw-data index.
	f = openBytes(t, buildDAQmx(TocMetaData|TocNewObjList|TocRawData|TocInterleavedData, []dmObj{ch1, ch2}, raw))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "a")), []int16{1, 2, 3, 4})
}

func TestDAQmxMixedWidthsOneBuffer(t *testing.T) {
	// Stride 7: int8 at 0, int16 at 1, int32 at 3.
	var raw []byte
	for i := byte(1); i <= 4; i++ {
		raw = append(raw, i, 0x10+i, 0x00, 0x20+i, 0, 0, 0)
	}
	objs := []dmObj{
		chDAQmx("/'g'/'a'", 4, 0, []uint32{7}, []dmScaler{{typ: 1, off: 0}}),
		chDAQmx("/'g'/'b'", 4, 0, []uint32{7}, []dmScaler{{typ: 3, off: 1}}),
		chDAQmx("/'g'/'c'", 4, 0, []uint32{7}, []dmScaler{{typ: 5, off: 3}}),
	}
	f := openBytes(t, buildDAQmx(daqTOC, objs, raw))
	a, b, c := daqCh(t, f, "g", "a"), daqCh(t, f, "g", "b"), daqCh(t, f, "g", "c")
	got, err := ReadValues([]*Channel{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], []int8{1, 2, 3, 4})
	assertEqual(t, got[1], []int16{17, 18, 19, 20})
	assertEqual(t, got[2], []int32{33, 34, 35, 36})
}

func TestDAQmxMultipleBuffers(t *testing.T) {
	raw := u16Raw(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16)
	widths := []uint32{4, 4}
	objs := []dmObj{
		chDAQmx("/'g'/'a'", 4, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 0}}),
		chDAQmx("/'g'/'b'", 4, 0, widths, []dmScaler{{typ: 3, off: 2, buf: 0}}),
		chDAQmx("/'g'/'c'", 4, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 1}}),
		chDAQmx("/'g'/'d'", 4, 0, widths, []dmScaler{{typ: 3, off: 2, buf: 1}}),
	}
	f := openBytes(t, buildDAQmx(daqTOC, objs, raw))
	chs := []*Channel{
		daqCh(t, f, "g", "a"), daqCh(t, f, "g", "b"),
		daqCh(t, f, "g", "c"), daqCh(t, f, "g", "d"),
	}
	got, err := ReadValues(chs)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], []int16{1, 3, 5, 7})
	assertEqual(t, got[1], []int16{2, 4, 6, 8})
	assertEqual(t, got[2], []int16{9, 11, 13, 15})
	assertEqual(t, got[3], []int16{10, 12, 14, 16})
	info, _ := chs[2].DAQmx()
	if info.Scalers[0].RawBuffer != 1 || info.Scalers[0].ByteOffset != 0 {
		t.Fatalf("%+v", info)
	}
}

func TestDAQmxBuffersDifferentWidthsAndLengths(t *testing.T) {
	var buf0, buf1 []byte
	for i := uint16(1); i <= 12; i++ {
		buf0 = append(buf0, u16Raw(i)...)
	}
	for i := int32(13); i <= 20; i++ {
		buf1 = append(buf1, le32(uint32(i))...)
	}
	raw := append(buf0, buf1...)
	widths := []uint32{6, 8}
	objs := []dmObj{
		chDAQmx("/'g'/'a'", 4, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 0}}),
		chDAQmx("/'g'/'b'", 4, 0, widths, []dmScaler{{typ: 3, off: 2, buf: 0}}),
		chDAQmx("/'g'/'c'", 4, 0, widths, []dmScaler{{typ: 3, off: 4, buf: 0}}),
		chDAQmx("/'g'/'d'", 4, 0, widths, []dmScaler{{typ: 5, off: 0, buf: 1}}),
		chDAQmx("/'g'/'e'", 4, 0, widths, []dmScaler{{typ: 5, off: 4, buf: 1}}),
	}
	f := openBytes(t, buildDAQmx(daqTOC, objs, raw))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "a")), []int16{1, 4, 7, 10})
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "b")), []int16{2, 5, 8, 11})
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "c")), []int16{3, 6, 9, 12})
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "d")), []int32{13, 15, 17, 19})
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "e")), []int32{14, 16, 18, 20})
}

func TestDAQmxTwoScalersOneChannel(t *testing.T) {
	raw := i16Raw(1, 17, 2, 18, 3, 19, 4, 20)
	// Scale id 0 is not the first scaler. Values follows scale id 0.
	obj := chDAQmx("/'g'/'c'", 4, RawIndexDAQmxDigitalLine, []uint32{4}, []dmScaler{
		{id: 5, typ: 3, off: 0},
		{id: 0, typ: 3, off: 2},
	})
	f := openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, raw))
	c := daqCh(t, f, "g", "c")
	info, ok := c.DAQmx()
	if !ok || info.Primary != 1 || info.Header != RawIndexDAQmxDigitalLine {
		t.Fatalf("%+v", info)
	}
	assertEqual(t, mustValues(t, c), []int16{17, 18, 19, 20})
	other, err := c.DAQmxSlice(5, 0, c.Len())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, other, []int16{1, 2, 3, 4})
	primary, err := c.DAQmxSlice(0, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, primary, []int16{18, 19})
	if _, err := c.DAQmxSlice(9, 0, 1); !errors.Is(err, ErrArgument) {
		t.Fatalf("missing id: %v", err)
	}

	dup := chDAQmx("/'g'/'c'", 2, 0, []uint32{4}, []dmScaler{
		{id: 0, typ: 3, off: 0},
		{id: 0, typ: 3, off: 2},
	})
	f = openBytes(t, buildDAQmx(daqTOC, []dmObj{dup}, i16Raw(1, 2, 3, 4)))
	c = daqCh(t, f, "g", "c")
	assertEqual(t, mustValues(t, c), []int16{1, 3})
	if _, err := c.DAQmxSlice(0, 0, 1); !errors.Is(err, ErrDAQmx) {
		t.Fatalf("duplicate id: %v", err)
	}
}

func TestDAQmxDigitalLines(t *testing.T) {
	raw := []byte{
		0, 0, 0, 0,
		1, 0, 0, 0,
		0, 0, 0, 0,
		1, 0, 0, 0,
	}
	obj := chDAQmx("/'g'/'c'", 4, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 0, off: 0}})
	f := openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, raw))
	c := daqCh(t, f, "g", "c")
	if c.DataType() != TypeUint8 {
		t.Fatal(c.DataType())
	}
	assertEqual(t, mustValues(t, c), []uint8{0, 1, 0, 1})
	info, _ := c.DAQmx()
	if !info.Scalers[0].Digital || info.Scalers[0].BitOffset != 0 || info.Scalers[0].ByteOffset != 0 {
		t.Fatalf("%+v", info.Scalers[0])
	}

	// Bits 0, 1, and 2 of the byte at offset 1 inside a 4-byte stride.
	samples := make([]byte, 8*4)
	for i := 0; i < 8; i++ {
		samples[i*4+1] = byte(i)
	}
	dig := []dmObj{
		chDAQmx("/'g'/'b0'", 8, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 0, off: 8}}),
		chDAQmx("/'g'/'b1'", 8, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 0, off: 9}}),
		chDAQmx("/'g'/'b2'", 8, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 0, off: 10}}),
	}
	f = openBytes(t, buildDAQmx(daqTOC, dig, samples))
	chs := []*Channel{daqCh(t, f, "g", "b0"), daqCh(t, f, "g", "b1"), daqCh(t, f, "g", "b2")}
	got, err := ReadValues(chs)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], []uint8{0, 1, 0, 1, 0, 1, 0, 1})
	assertEqual(t, got[1], []uint8{0, 0, 1, 1, 0, 0, 1, 1})
	assertEqual(t, got[2], []uint8{0, 0, 0, 0, 1, 1, 1, 1})
	info, _ = chs[2].DAQmx()
	if info.Scalers[0].BitOffset != 10 || info.Scalers[0].ByteOffset != 1 {
		t.Fatalf("%+v", info.Scalers[0])
	}
	sl, err := chs[0].Slice(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, sl, []uint8{1, 0})
}

func TestDAQmxMultiSegment(t *testing.T) {
	first := buildDAQmx(daqTOC, []dmObj{daqI16(0, 2)}, i16Raw(1, 2))
	match := buildDAQmx(daqTOC, []dmObj{objMatch("/'g'/'c'")}, i16Raw(3, 4))
	rawOnly := buildDAQmx(TocRawData|TocDAQmxRawData, nil, i16Raw(5, 6))
	file := append(append(first, match...), rawOnly...)
	f := openBytes(t, file)
	c := daqCh(t, f, "g", "c")
	assertEqual(t, mustValues(t, c), []int16{1, 2, 3, 4, 5, 6})
	got, err := c.Slice(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []int16{5, 6})

	// Chunk size may change. The scaler layout may not.
	grown := buildDAQmx(daqTOC, []dmObj{daqI16(0, 3)}, i16Raw(3, 4, 5))
	f = openBytes(t, append(first, grown...))
	c = daqCh(t, f, "g", "c")
	assertEqual(t, mustValues(t, c), []int16{1, 2, 3, 4, 5})
	mid, err := c.Slice(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, mid, []int16{2, 3, 4})

	// The buffer width may change (npTDMS reads this). The scaler may not.
	wider := chDAQmx("/'g'/'c'", 1, 0, []uint32{4}, []dmScaler{{typ: 3, off: 0}})
	f = openBytes(t, append(first, buildDAQmx(daqTOC, []dmObj{wider}, i16Raw(1, 2))...))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "c")), []int16{1, 2, 1})
	for name, sc := range map[string]dmScaler{
		"type":     {typ: 5, off: 0},
		"scale id": {id: 3, typ: 3, off: 0},
	} {
		changed := chDAQmx("/'g'/'c'", 1, 0, []uint32{4}, []dmScaler{sc})
		_, err = NewReader(bytes.NewReader(append(first, buildDAQmx(daqTOC, []dmObj{changed}, le32(1))...)))
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("scaler %s change: %v", name, err)
		}
	}
	digital := chDAQmx("/'g'/'c'", 1, RawIndexDAQmxDigitalObserved, []uint32{2}, []dmScaler{{typ: 0, off: 0}})
	_, err = NewReader(bytes.NewReader(append(first, buildDAQmx(daqTOC, []dmObj{digital}, i16Raw(1))...)))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("analog to digital: %v", err)
	}
	// A digital scaler's decoded values are uint8 either way. The DAQmx type
	// code still has to match, which is what npTDMS compares.
	digFirst := buildDAQmx(daqTOC, []dmObj{
		chDAQmx("/'g'/'d'", 1, RawIndexDAQmxDigitalObserved, []uint32{1}, []dmScaler{{typ: 0, off: 0}}),
	}, []byte{0x04})
	digWider := chDAQmx("/'g'/'d'", 1, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 4, off: 0}})
	_, err = NewReader(bytes.NewReader(append(digFirst, buildDAQmx(daqTOC, []dmObj{digWider}, le32(1))...)))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("digital type code change: %v", err)
	}
	std := buildDAQmx(TocMetaData|TocNewObjList|TocRawData, []dmObj{objStd("/'g'/'c'", TypeInt16, 1)}, i16Raw(9))
	_, err = NewReader(bytes.NewReader(append(first, std...)))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("daqmx to standard: %v", err)
	}
	plain := buildDAQmx(TocMetaData|TocNewObjList|TocRawData, []dmObj{objStd("/'g'/'c'", TypeInt16, 1)}, i16Raw(1))
	_, err = NewReader(bytes.NewReader(append(plain, first...)))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("standard to daqmx: %v", err)
	}
}

func TestDAQmxPartialTails(t *testing.T) {
	// Index promises 4 int16 samples (8 bytes). Five bytes are on disk:
	// two whole samples and one unused byte.
	file := buildDAQmx(daqTOC, []dmObj{daqI16(0, 4)}, append(i16Raw(1, 2), 0x00))
	// The builder's next offset matches the short file, which is a finished
	// segment whose raw length is not a chunk. That is corrupt.
	mustOpenErr(t, file, ErrCorrupt)

	inc := append([]byte(nil), file...)
	markIncomplete(inc)
	f := openBytes(t, inc)
	c := daqCh(t, f, "g", "c")
	assertEqual(t, mustValues(t, c), []int16{1, 2})
	st := f.Status()
	if !st.Incomplete || st.Complete || !st.PartialTail || st.UnusedTail != 1 {
		t.Fatalf("%#v", st)
	}

	// Declare 8 raw bytes while only 5 exist.
	lenient := append([]byte(nil), file...)
	next := binary.LittleEndian.Uint64(lenient[12:20])
	binary.LittleEndian.PutUint64(lenient[12:20], next+3)
	if _, err := NewReader(bytes.NewReader(lenient)); !errors.Is(err, ErrTruncated) {
		t.Fatalf("strict tail: %v", err)
	}
	lf, err := NewReader(bytes.NewReader(lenient), WithLenientTail())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lf.Close() })
	c = daqCh(t, lf, "g", "c")
	assertEqual(t, mustValues(t, c), []int16{1, 2})
	st = lf.Status()
	if !st.Truncated || st.Incomplete || !st.PartialTail || st.UnusedTail != 1 {
		t.Fatalf("%#v", st)
	}

	// Different buffer lengths, incomplete third chunk. Whole samples only.
	widths := []uint32{4, 4, 4}
	var chunk []byte
	for i := 0; i < 4; i++ {
		chunk = append(chunk, u16Raw(1, 2)...)
	}
	for i := 0; i < 2; i++ {
		chunk = append(chunk, u16Raw(3, 4)...)
	}
	chunk = append(chunk, u16Raw(5, 6)...)
	raw := append([]byte(nil), chunk...)
	raw = append(raw, chunk...)
	for i := 0; i < 4; i++ {
		raw = append(raw, u16Raw(1, 2)...)
	}
	raw = append(raw, u16Raw(3, 4)...)
	objs := []dmObj{
		chDAQmx("/'g'/'a'", 4, 0, widths, []dmScaler{{id: 0xFFFFFFFF, typ: 2, off: 0, buf: 0}}),
		chDAQmx("/'g'/'b'", 4, 0, widths, []dmScaler{{id: 0xFFFFFFFF, typ: 2, off: 2, buf: 0}}),
		chDAQmx("/'g'/'c'", 2, 0, widths, []dmScaler{{id: 0xFFFFFFFF, typ: 2, off: 0, buf: 1}}),
		chDAQmx("/'g'/'d'", 2, 0, widths, []dmScaler{{id: 0xFFFFFFFF, typ: 2, off: 2, buf: 1}}),
		chDAQmx("/'g'/'e'", 1, 0, widths, []dmScaler{{id: 0xFFFFFFFF, typ: 2, off: 0, buf: 2}}),
		chDAQmx("/'g'/'f'", 1, 0, widths, []dmScaler{{id: 0xFFFFFFFF, typ: 2, off: 2, buf: 2}}),
	}
	// The npTDMS case for this tail omits kTocDAQmxRawData. The index words
	// still select the DAQmx layout.
	toc := TocMetaData | TocNewObjList | TocRawData
	partial := buildDAQmx(toc, objs, raw)
	markIncomplete(partial)
	f = openBytes(t, partial)
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "a")), repeatU16(1, 12))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "b")), repeatU16(2, 12))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "c")), repeatU16(3, 5))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "d")), repeatU16(4, 5))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "e")), repeatU16(5, 2))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "f")), repeatU16(6, 2))
}

func TestDAQmxBigEndian(t *testing.T) {
	raw := []byte{0x00, 0x01, 0xFF, 0xFE}
	f := openBytes(t, buildDAQmx(daqTOC|TocBigEndian, []dmObj{daqI16(0, 2)}, raw))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "c")), []int16{1, -2})
}

func TestDAQmxRejectedLayouts(t *testing.T) {
	i16 := []dmScaler{{typ: 3}}
	base := func(o dmObj, raw []byte) []byte {
		return buildDAQmx(daqTOC, []dmObj{o}, raw)
	}
	bmp := chDAQmx("/'g'/'c'", 1, 0, []uint32{2}, []dmScaler{{typ: 3, bitmap: 1}})
	mustOpenErr(t, base(bmp, i16Raw(1)), ErrDAQmx)
	dig := chDAQmx("/'g'/'c'", 1, RawIndexDAQmxDigitalObserved, []uint32{1}, []dmScaler{{typ: 0, bitmap: 1}})
	mustOpenErr(t, base(dig, []byte{1}), ErrDAQmx)
	unk := chDAQmx("/'g'/'c'", 1, 0, []uint32{2}, []dmScaler{{typ: 42}})
	mustOpenErr(t, base(unk, i16Raw(1)), ErrDAQmx)
	floatDig := chDAQmx("/'g'/'c'", 1, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 8}})
	mustOpenErr(t, base(floatDig, []byte{0, 0, 0, 0}), ErrDAQmx)
	stdRaw := objStd("/'g'/'c'", TypeDAQmxRaw, 0)
	mustOpenErr(t, buildDAQmx(TocMetaData|TocNewObjList|TocRawData, []dmObj{stdRaw}, nil), ErrDAQmx)
	mixed := []dmObj{
		chDAQmx("/'g'/'a'", 1, 0, []uint32{2}, i16),
		objStd("/'g'/'b'", TypeInt16, 1),
	}
	mustOpenErr(t, buildDAQmx(daqTOC, mixed, i16Raw(1, 2)), ErrDAQmx)
	empty := chDAQmx("/'g'/'c'", 0, 0, nil, nil)
	mustOpenErr(t, base(empty, nil), ErrCorrupt)
	zeroW := chDAQmx("/'g'/'c'", 1, 0, []uint32{0}, i16)
	mustOpenErr(t, base(zeroW, nil), ErrCorrupt)
	past := chDAQmx("/'g'/'c'", 1, 0, []uint32{2}, []dmScaler{{typ: 3, off: 2}})
	mustOpenErr(t, base(past, i16Raw(1)), ErrCorrupt)
	orphan := chDAQmx("/'g'/'c'", 1, 0, []uint32{2, 2}, []dmScaler{{typ: 3, buf: 0}})
	mustOpenErr(t, base(orphan, i16Raw(1)), ErrDAQmx)
	// A typed DAQmx index must name the one scaler's own type.
	badType := chDAQmx("/'g'/'c'", 1, 0, []uint32{2}, i16)
	badType.forceType = true
	badType.dataType = uint32(TypeInt32)
	mustOpenErr(t, base(badType, i16Raw(1)), ErrDAQmx)
	strType := chDAQmx("/'g'/'c'", 1, 0, []uint32{2}, i16)
	strType.forceType = true
	strType.dataType = uint32(TypeString)
	mustOpenErr(t, base(strType, i16Raw(1)), ErrDAQmx)
	typedTwo := chDAQmx("/'g'/'c'", 1, 0, []uint32{4}, []dmScaler{{typ: 3, off: 0}, {id: 1, typ: 3, off: 2}})
	typedTwo.forceType = true
	typedTwo.dataType = uint32(TypeInt16)
	mustOpenErr(t, base(typedTwo, i16Raw(1, 2)), ErrDAQmx)
	badDim := chDAQmx("/'g'/'c'", 1, 0, []uint32{2}, i16)
	badDim.setDim = true
	badDim.arrayDim = 2
	mustOpenErr(t, base(badDim, i16Raw(1)), ErrUnsupported)
	widths := []dmObj{
		chDAQmx("/'g'/'a'", 1, 0, []uint32{2}, []dmScaler{{typ: 3, off: 0}}),
		chDAQmx("/'g'/'b'", 1, 0, []uint32{4}, []dmScaler{{typ: 3, off: 2}}),
	}
	mustOpenErr(t, buildDAQmx(daqTOC, widths, i16Raw(1, 2)), ErrCorrupt)
	counts := []dmObj{
		chDAQmx("/'g'/'a'", 2, 0, []uint32{2}, i16),
		chDAQmx("/'g'/'b'", 4, 0, []uint32{2}, []dmScaler{{typ: 3, off: 0}}),
	}
	mustOpenErr(t, buildDAQmx(daqTOC, counts, i16Raw(1, 2, 3, 4)), ErrCorrupt)
	bit := chDAQmx("/'g'/'c'", 1, RawIndexDAQmxDigitalObserved, []uint32{1}, []dmScaler{{typ: 0, off: 8}})
	mustOpenErr(t, base(bit, []byte{0}), ErrCorrupt)
}

func TestDAQmxSegmentIndex(t *testing.T) {
	raw := i16Raw(1, 17, 2, 18, 3, 19, 4, 20)
	analog := chDAQmx("/'g'/'c'", 4, RawIndexDAQmxFormatChanging, []uint32{4}, []dmScaler{
		{id: 5, typ: 3, off: 0},
		{id: 0, typ: 3, off: 2},
	})
	samples := make([]byte, 4*4)
	for i := 0; i < 4; i++ {
		samples[i*4] = byte(i)
	}
	digital := chDAQmx("/'g'/'d'", 4, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 0, off: 1}})
	multi := []dmObj{
		chDAQmx("/'g'/'a'", 4, 0, []uint32{4, 4}, []dmScaler{{typ: 3, off: 0, buf: 0}}),
		chDAQmx("/'g'/'b'", 4, 0, []uint32{4, 4}, []dmScaler{{typ: 3, off: 0, buf: 1}}),
	}
	multiRaw := u16Raw(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16)
	cases := [][]byte{
		buildDAQmx(daqTOC, []dmObj{analog}, raw),
		buildDAQmx(daqTOC, []dmObj{digital}, samples),
		buildDAQmx(daqTOC, multi, multiRaw),
	}
	for i, file := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "daq.tdms")
		if err := os.WriteFile(path, file, 0o644); err != nil {
			t.Fatal(err)
		}
		side := SegmentIndexPath(path)
		before := segmentParses.Load()
		f, err := Open(path, WithSegmentIndex(side))
		if err != nil {
			t.Fatal(err)
		}
		if f.SegmentIndexUsed() || segmentParses.Load() != before+1 {
			t.Fatalf("case %d first open cache=%v parses %d", i, f.SegmentIndexUsed(), segmentParses.Load()-before)
		}
		want := indexSnapshot(t, f)
		f.Close()
		before = segmentParses.Load()
		f, err = Open(path, WithSegmentIndex(side))
		if err != nil {
			t.Fatal(err)
		}
		if !f.SegmentIndexUsed() || segmentParses.Load() != before {
			t.Fatalf("case %d second open cache=%v", i, f.SegmentIndexUsed())
		}
		got := indexSnapshot(t, f)
		if got != want {
			t.Fatalf("case %d\n%s\nwant\n%s", i, got, want)
		}
		f.Close()
	}
}

func indexSnapshot(t *testing.T, f *File) string {
	t.Helper()
	var b bytes.Buffer
	for _, g := range f.Groups() {
		for _, c := range g.Channels() {
			v, err := c.Values()
			if err != nil {
				t.Fatal(err)
			}
			info, ok := c.DAQmx()
			if !ok {
				t.Fatal("not daqmx")
			}
			fmtSnap(&b, c.Path(), c.DataType(), v, info)
			for _, sc := range info.Scalers {
				sl, err := c.DAQmxSlice(sc.ScaleID, 0, c.Len())
				if err != nil {
					// Duplicate scale ids fail the slice. Values already
					// checked the primary scaler.
					if !errors.Is(err, ErrDAQmx) {
						t.Fatal(err)
					}
					continue
				}
				fmtSnap(&b, c.Path(), sc.DataType, sl, DAQmxRaw{})
			}
		}
	}
	return b.String()
}

func fmtSnap(b *bytes.Buffer, path string, dt DataType, v any, info DAQmxRaw) {
	b.WriteString(path)
	b.WriteByte(' ')
	b.WriteString(dt.String())
	b.WriteByte(' ')
	fmt.Fprintf(b, "%v", v)
	if info.Header != 0 || len(info.Scalers) != 0 {
		b.WriteString(" header=")
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], info.Header)
		b.Write(buf[:])
	}
	b.WriteByte('\n')
}

func TestDAQmxScaling(t *testing.T) {
	rawIn := []float64{1, 2, 3}
	copied := append([]float64(nil), rawIn...)
	lin := func(index int, slope, icept float64, raw bool, src uint32) DAQmxScale {
		return DAQmxScale{
			Index: index, Type: "Linear", Slope: slope, Intercept: icept,
			InputSource: src, InputRaw: raw, linearOK: true,
		}
	}
	chain := []DAQmxScale{
		lin(0, 1, 1, true, 0xFFFFFFFF),
		lin(1, 2, 2, false, 0),
		lin(2, 3, 3, false, 1),
	}
	got, err := ApplyDAQmxScale(chain, copied)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{21, 27, 33})
	assertEqual(t, copied, rawIn)
	allRaw := []DAQmxScale{
		lin(0, 1, 1, true, 0xFFFFFFFF),
		lin(1, 2, 2, true, 0xFFFFFFFF),
		lin(2, 3, 3, true, 0xFFFFFFFF),
	}
	got, err = ApplyDAQmxScale(allRaw, rawIn)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{6, 9, 12})
	poly := []DAQmxScale{{
		Index: 0, Type: "Polynomial", InputRaw: true, polyOK: true,
		Coefficients: []float64{10, 1, 2, 3},
	}}
	got, err = ApplyDAQmxScale(poly, rawIn)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{16, 44, 112})
	zeros, err := ApplyDAQmxScale([]DAQmxScale{{
		Index: 0, Type: "Polynomial", InputRaw: true, polyOK: true,
	}}, rawIn)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, zeros, []float64{0, 0, 0})
	// The output is the highest index (npTDMS MultiScaling), even when
	// another scale is not read by anything.
	got, err = ApplyDAQmxScale([]DAQmxScale{
		lin(0, 2, 0, true, 0xFFFFFFFF),
		lin(1, 5, 0, true, 0xFFFFFFFF),
		lin(2, 3, 0, false, 0),
	}, rawIn)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{6, 12, 18})
	got, err = ApplyDAQmxScale([]DAQmxScale{
		lin(1, 2, 0, false, 2),
		lin(2, 3, 0, true, 0xFFFFFFFF),
	}, rawIn)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{3, 6, 9})
	if _, err := ApplyDAQmxScale([]DAQmxScale{lin(1, 1, 0, true, 0), lin(1, 2, 0, true, 0)}, rawIn); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("duplicate: %v", err)
	}
	got, err = ApplyDAQmxScale(nil, rawIn)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, rawIn)
	if _, err := ApplyDAQmxScale([]DAQmxScale{
		lin(2, 1, 0, false, 0),
		lin(0, 1, 0, false, 1),
		lin(1, 1, 0, false, 0),
	}, rawIn); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("cycle: %v", err)
	}
	if _, err := ApplyDAQmxScale([]DAQmxScale{{
		Index: 0, Type: "Linear", InputRaw: true,
	}}, rawIn); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("missing slope: %v", err)
	}
	if _, err := ApplyDAQmxScale([]DAQmxScale{{
		Index: 0, Type: "Thermocouple", InputRaw: true,
	}}, rawIn); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("thermocouple: %v", err)
	}

	samples := i16Raw(1, 2, 3)
	openScaled := func(t *testing.T, props []dmProp, group, fileProps []dmProp) *Channel {
		t.Helper()
		var objs []dmObj
		if len(fileProps) > 0 {
			objs = append(objs, objNone("/", fileProps...))
		}
		if len(group) > 0 {
			objs = append(objs, objNone("/'g'", group...))
		}
		objs = append(objs, chDAQmx("/'g'/'c'", 3, 0, []uint32{2}, []dmScaler{{typ: 3}}, props...))
		f := openBytes(t, buildDAQmx(daqTOC, objs, samples))
		return daqCh(t, f, "g", "c")
	}
	c := openScaled(t, linearProps(0, 2, 10, nil), nil, nil)
	assertEqual(t, mustValues(t, c), []int16{1, 2, 3})
	scaled, err := c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{12, 14, 16})
	meta := c.DAQmxScaling()
	if meta.Source != "channel" || len(meta.Scales) != 1 || meta.Scales[0].Slope != 2 || !meta.Scales[0].InputRaw {
		t.Fatalf("%+v", meta)
	}

	c = openScaled(t, append(linearProps(0, 2, 10, nil), dmProp{name: "NI_Scaling_Status", kind: propStr, str: "Scaled"}), nil, nil)
	scaled, err = c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{1, 2, 3})

	c = openScaled(t, nil, linearProps(0, 2, 10, nil), nil)
	if c.DAQmxScaling().Source != "group" {
		t.Fatal(c.DAQmxScaling().Source)
	}
	scaled, err = c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{12, 14, 16})

	c = openScaled(t, linearProps(0, 3, 0, nil), linearProps(0, 2, 10, nil), nil)
	scaled, err = c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{3, 6, 9})
	if c.DAQmxScaling().Source != "channel" {
		t.Fatal(c.DAQmxScaling().Source)
	}

	c = openScaled(t, nil, nil, linearProps(1, 2, 10, u32ptr(0)))
	meta = c.DAQmxScaling()
	if meta.Source != "file" || len(meta.Scales) != 1 || !meta.Scales[0].InputRaw || meta.Scales[0].Index != 1 {
		t.Fatalf("%+v", meta)
	}
	scaled, err = c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{12, 14, 16})

	var chainProps []dmProp
	chainProps = append(chainProps, linearProps(0, 1, 1, u32ptr(0xFFFFFFFF))...)
	chainProps = append(chainProps, linearProps(1, 2, 2, u32ptr(0))...)
	chainProps = append(chainProps, linearProps(2, 3, 3, u32ptr(1))...)
	c = openScaled(t, chainProps, nil, nil)
	scaled, err = c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{21, 27, 33})

	var allRawProps []dmProp
	allRawProps = append(allRawProps, linearProps(0, 1, 1, u32ptr(0xFFFFFFFF))...)
	allRawProps = append(allRawProps, linearProps(1, 2, 2, u32ptr(0xFFFFFFFF))...)
	allRawProps = append(allRawProps, linearProps(2, 3, 3, u32ptr(0xFFFFFFFF))...)
	c = openScaled(t, allRawProps, nil, nil)
	scaled, err = c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{6, 9, 12})

	c = openScaled(t, polyProps(0, []float64{10, 1, 2, 3}, nil), nil, nil)
	scaled, err = c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{16, 44, 112})
	assertEqual(t, mustValues(t, c), []int16{1, 2, 3})

	size0 := uint32(0)
	c = openScaled(t, polyProps(0, nil, &size0), nil, nil)
	scaled, err = c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, scaled, []float64{0, 0, 0})

	c = openScaled(t, []dmProp{{name: "NI_Scale[0]_Scale_Type", kind: propStr, str: "Thermocouple"}}, nil, nil)
	if _, err := c.DAQmxScaledFloat64s(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("thermocouple file: %v", err)
	}
	assertEqual(t, mustValues(t, c), []int16{1, 2, 3})
	if c.DAQmxScaling().Scales[0].Type != "Thermocouple" {
		t.Fatal(c.DAQmxScaling())
	}

	tooBig := uint32(33)
	parsed := parseDAQmxScales([]Property{
		{Name: "NI_Scale[0]_Scale_Type", Type: TypeString, Value: "Polynomial"},
		{Name: "NI_Scale[0]_Polynomial_Coefficients_Size", Type: TypeUint32, Value: tooBig},
	})
	if len(parsed) != 1 || parsed[0].polyOK {
		t.Fatalf("%+v", parsed)
	}
	if _, err := ApplyDAQmxScale(parsed, rawIn); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("cap: %v", err)
	}
}

func linearProps(index int, slope, icept float64, input *uint32) []dmProp {
	ps := []dmProp{
		{name: fmt.Sprintf("NI_Scale[%d]_Scale_Type", index), kind: propStr, str: "Linear"},
		{name: fmt.Sprintf("NI_Scale[%d]_Linear_Slope", index), kind: propF64, f64: slope},
		{name: fmt.Sprintf("NI_Scale[%d]_Linear_Y_Intercept", index), kind: propF64, f64: icept},
	}
	if input != nil {
		ps = append(ps, dmProp{name: fmt.Sprintf("NI_Scale[%d]_Linear_Input_Source", index), kind: propU32, u32: *input})
	}
	return ps
}

func polyProps(index int, coeffs []float64, size *uint32) []dmProp {
	ps := []dmProp{{name: fmt.Sprintf("NI_Scale[%d]_Scale_Type", index), kind: propStr, str: "Polynomial"}}
	if size != nil {
		ps = append(ps, dmProp{name: fmt.Sprintf("NI_Scale[%d]_Polynomial_Coefficients_Size", index), kind: propU32, u32: *size})
	}
	for i, c := range coeffs {
		ps = append(ps, dmProp{name: fmt.Sprintf("NI_Scale[%d]_Polynomial_Coefficients[%d]", index, i), kind: propF64, f64: c})
	}
	return ps
}

func u32ptr(v uint32) *uint32 { return &v }

func TestStandardChannelIsNotDAQmx(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeInt16)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]int16{4}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c := daqCh(t, f, "g", "c")
	if _, ok := c.DAQmx(); ok {
		t.Fatal("standard channel reported DAQmx")
	}
	if _, err := c.DAQmxSlice(0, 0, 1); !errors.Is(err, ErrArgument) {
		t.Fatalf("%v", err)
	}
	assertEqual(t, mustValues(t, c), []int16{4})
}

func daqmxSeedFormatChanging() []byte {
	return buildDAQmx(daqTOC, []dmObj{daqI16(RawIndexDAQmxFormatChanging, 4)}, i16Raw(1, 2, -1, -2))
}

func daqmxSeedFormatChangingAlt() []byte {
	return buildDAQmx(daqTOC, []dmObj{daqI16(RawIndexDAQmxDigitalLine, 4)}, i16Raw(1, 2, -1, -2))
}

func daqmxSeedDigital() []byte {
	obj := chDAQmx("/'g'/'c'", 4, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 0}})
	raw := []byte{0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0}
	return buildDAQmx(daqTOC, []dmObj{obj}, raw)
}

func daqmxSeedMultiBuffer() []byte {
	widths := []uint32{4, 4}
	objs := []dmObj{
		chDAQmx("/'g'/'a'", 2, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 0}}),
		chDAQmx("/'g'/'b'", 2, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 1}}),
	}
	return buildDAQmx(daqTOC, objs, u16Raw(1, 2, 3, 4, 5, 6, 7, 8))
}

func le32(vals ...uint32) []byte {
	var b bytes.Buffer
	for _, v := range vals {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], v)
		b.Write(buf[:])
	}
	return b.Bytes()
}

func le64(vals ...uint64) []byte {
	var b bytes.Buffer
	for _, v := range vals {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], v)
		b.Write(buf[:])
	}
	return b.Bytes()
}

func f32Raw(vals ...float32) []byte {
	var b bytes.Buffer
	for _, v := range vals {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], math.Float32bits(v))
		b.Write(buf[:])
	}
	return b.Bytes()
}

func f64Raw(vals ...float64) []byte {
	var b bytes.Buffer
	for _, v := range vals {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
		b.Write(buf[:])
	}
	return b.Bytes()
}

func tsRaw(seconds int64) []byte {
	var b [16]byte
	binary.LittleEndian.PutUint64(b[8:], uint64(seconds))
	return b[:]
}

func repeatU16(v uint16, n int) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = v
	}
	return out
}
