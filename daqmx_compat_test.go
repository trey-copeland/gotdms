package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Layouts and scale chains from the PR #15 review. The expected values are
// what npTDMS returns for the same bytes; testdata/nptdms/harness.py checks
// the same cases against npTDMS in CI.

func cursorAll[T any](t *testing.T, c *Channel, batch int) []T {
	t.Helper()
	cur := c.Cursor(batch)
	defer cur.Close()
	var out []T
	for cur.Next() {
		out = append(out, cur.Values().([]T)...)
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func multiChunkTwoBuffers() []byte {
	widths := []uint32{4, 2}
	objs := []dmObj{
		chDAQmx("/'g'/'a'", 3, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 0}}),
		chDAQmx("/'g'/'b'", 3, 0, widths, []dmScaler{{typ: 3, off: 2, buf: 0}}),
		chDAQmx("/'g'/'c'", 2, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 1}}),
	}
	var raw []byte
	raw = append(raw, i16Raw(1, 101, 2, 102, 3, 103, 201, 202)...)
	raw = append(raw, i16Raw(4, 104, 5, 105, 6, 106, 203, 204)...)
	return buildDAQmx(daqTOC, objs, raw)
}

func TestDAQmxMultiChunkTwoBuffers(t *testing.T) {
	f := openBytes(t, multiChunkTwoBuffers())
	a, b, c := daqCh(t, f, "g", "a"), daqCh(t, f, "g", "b"), daqCh(t, f, "g", "c")
	wantA := []int16{1, 2, 3, 4, 5, 6}
	wantB := []int16{101, 102, 103, 104, 105, 106}
	wantC := []int16{201, 202, 203, 204}
	assertEqual(t, mustValues(t, a), wantA)
	assertEqual(t, mustValues(t, b), wantB)
	assertEqual(t, mustValues(t, c), wantC)
	// Slices that cross the chunk boundary.
	s, err := c.Slice(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, s, []int16{202, 203})
	s, err = a.Slice(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, s, []int16{3, 4, 5})
	got, err := ReadValues([]*Channel{a, c, b})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], wantA)
	assertEqual(t, got[1], wantC)
	assertEqual(t, got[2], wantB)
	for _, batch := range []int{1, 3, 4, 1 << 16} {
		assertEqual(t, cursorAll[int16](t, c, batch), wantC)
		assertEqual(t, cursorAll[int16](t, b, batch), wantB)
	}
}

// A one-byte digital buffer has stride == value width, which takes the
// packed read and the cursor's direct ReadAt. The bit mask must still apply.
func TestDAQmxDigitalPackedBuffer(t *testing.T) {
	n := 70000
	raw := make([]byte, n)
	want := make([]uint8, n)
	for i := range raw {
		raw[i] = byte(i) | 0xF0
		want[i] = byte(i>>2) & 1
	}
	obj := chDAQmx("/'g'/'d'", uint64(n), RawIndexDAQmxDigitalObserved, []uint32{1}, []dmScaler{{typ: 0, off: 2}})
	f := openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, raw))
	d := daqCh(t, f, "g", "d")
	assertEqual(t, mustValues(t, d), want)
	for _, batch := range []int{7, 1 << 15, 1 << 17} {
		assertEqual(t, cursorAll[uint8](t, d, batch), want)
	}
	rv, err := ReadValues([]*Channel{d})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, rv[0], want)
}

// npTDMS reads a DAQmx index whose data type is the one scaler's type.
func TestDAQmxTypedIndex(t *testing.T) {
	o := daqI16(0, 2)
	o.forceType = true
	o.dataType = uint32(TypeInt16)
	f := openBytes(t, buildDAQmx(daqTOC, []dmObj{o}, i16Raw(7, 8)))
	c := daqCh(t, f, "g", "c")
	if c.DataType() != TypeInt16 {
		t.Fatal(c.DataType())
	}
	assertEqual(t, mustValues(t, c), []int16{7, 8})
	if _, ok := c.DAQmx(); !ok {
		t.Fatal("typed index is still DAQmx")
	}
	dig := chDAQmx("/'g'/'d'", 2, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 4, off: 1}})
	dig.forceType = true
	dig.dataType = uint32(TypeUint32)
	f = openBytes(t, buildDAQmx(daqTOC, []dmObj{dig}, le32(2, 0)))
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "d")), []uint8{1, 0})
}

// npTDMS decides the layout from the raw-data indexes, so standard
// channels in a segment that sets kTocDAQmxRawData read normally.
func TestDAQmxStandardIndexUnderToCFlag(t *testing.T) {
	f := openBytes(t, buildDAQmx(daqTOC, []dmObj{objStd("/'g'/'c'", TypeInt16, 2)}, i16Raw(7, 8)))
	c := daqCh(t, f, "g", "c")
	assertEqual(t, mustValues(t, c), []int16{7, 8})
	if _, ok := c.DAQmx(); ok {
		t.Fatal("standard channel reported DAQmx")
	}
}

func widenedFile() []byte {
	first := buildDAQmx(daqTOC, []dmObj{daqI16(0, 2)}, i16Raw(1, 2))
	objs := []dmObj{
		chDAQmx("/'g'/'c'", 2, 0, []uint32{4}, []dmScaler{{typ: 3, off: 0}}),
		chDAQmx("/'g'/'e'", 2, 0, []uint32{4}, []dmScaler{{typ: 3, off: 2}}),
	}
	second := buildDAQmx(daqTOC, objs, i16Raw(3, 30, 4, 40))
	return append(first, second...)
}

// A later acquisition adds a channel to the buffer. The first channel's
// stride changes from 2 to 4 between segments.
func TestDAQmxWidthChangeAcrossSegments(t *testing.T) {
	f := openBytes(t, widenedFile())
	c, e := daqCh(t, f, "g", "c"), daqCh(t, f, "g", "e")
	assertEqual(t, mustValues(t, c), []int16{1, 2, 3, 4})
	assertEqual(t, mustValues(t, e), []int16{30, 40})
	s, err := c.Slice(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, s, []int16{2, 3})
	assertEqual(t, cursorAll[int16](t, c, 3), []int16{1, 2, 3, 4})
	info, _ := c.DAQmx()
	if len(info.Widths) != 1 || info.Widths[0] != 4 {
		t.Fatalf("DAQmx reports the latest index: %+v", info)
	}
}

func TestDAQmxSegmentIndexMoreLayouts(t *testing.T) {
	for name, file := range map[string][]byte{
		"multi-chunk": multiChunkTwoBuffers(),
		"widened":     widenedFile(),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "daq.tdms")
			if err := os.WriteFile(path, file, 0o644); err != nil {
				t.Fatal(err)
			}
			side := SegmentIndexPath(path)
			f, err := Open(path, WithSegmentIndex(side))
			if err != nil {
				t.Fatal(err)
			}
			want := indexSnapshot(t, f)
			f.Close()
			f, err = Open(path, WithSegmentIndex(side))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if !f.SegmentIndexUsed() {
				t.Fatal("sidecar not used")
			}
			if got := indexSnapshot(t, f); got != want {
				t.Fatalf("%s\nwant\n%s", got, want)
			}
		})
	}
}

// Each case's expected output is npTDMS channel[:] for the same file.
func TestDAQmxScaleChainsMatchNpTDMS(t *testing.T) {
	nscales := func(n uint32) []dmProp {
		return []dmProp{
			{name: "NI_Number_Of_Scales", kind: propU32, u32: n},
			{name: "NI_Scaling_Status", kind: propStr, str: "unscaled"},
		}
	}
	cat := func(pp ...[]dmProp) []dmProp {
		var o []dmProp
		for _, p := range pp {
			o = append(o, p...)
		}
		return o
	}
	one := []dmScaler{{id: 0, typ: 3}}
	two := []dmScaler{{id: 0, typ: 3, off: 0}, {id: 1, typ: 3, off: 2}}
	oneRaw := i16Raw(1, 2, 3)
	twoRaw := i16Raw(1, 10, 2, 20, 3, 30)
	polyIn := []dmProp{{name: "NI_Scale[1]_Polynomial_Input_Source", kind: propU32, u32: 0}}
	cases := []struct {
		name    string
		scalers []dmScaler
		width   uint32
		props   []dmProp
		raw     []byte
		want    []float64
	}{
		// NI_Scale[1] reads NI_Scale[0], which has no type: DAQmx scaler 0.
		{"linear on scaler 0", one, 2, cat(nscales(2), linearProps(1, 2, 1, u32ptr(0))), oneRaw, []float64{3, 5, 7}},
		{"polynomial then linear", one, 2, cat(nscales(3), polyProps(1, []float64{1, 2, 3}, u32ptr(3)), polyIn, linearProps(2, 10, 0, u32ptr(1))), oneRaw, []float64{60, 170, 340}},
		// The input names scaler 1, not the primary scaler 0.
		{"input is scaler 1", two, 4, cat(nscales(3), linearProps(2, 2, 0, u32ptr(1))), twoRaw, []float64{20, 40, 60}},
		// The output is the last index, not the scale nothing reads.
		{"last index is output", one, 2, cat(nscales(3), linearProps(1, 2, 0, u32ptr(2)), linearProps(2, 3, 0, u32ptr(0))), oneRaw, []float64{3, 6, 9}},
		{"two scales read raw", one, 2, cat(nscales(3), linearProps(1, 2, 0, u32ptr(0)), linearProps(2, 3, 0, u32ptr(0))), oneRaw, []float64{3, 6, 9}},
		// NI_Number_Of_Scales puts the output at index 1, scaler 1's raw data.
		{"count past typed scales", two, 4, cat(nscales(2), linearProps(0, 2, 0, nil)), twoRaw, []float64{10, 20, 30}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := chDAQmx("/'g'/'c'", 3, 0, []uint32{tc.width}, tc.scalers, tc.props...)
			f := openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, tc.raw))
			c := daqCh(t, f, "g", "c")
			got, err := c.DAQmxScaledFloat64s()
			if err != nil {
				t.Fatal(err)
			}
			assertEqual(t, got, tc.want)
			assertEqual(t, mustValues(t, c), []int16{1, 2, 3})
		})
	}

	// Two scalers and an input naming neither is not guessed.
	obj := chDAQmx("/'g'/'c'", 3, 0, []uint32{4}, two, cat(nscales(3), linearProps(2, 2, 0, u32ptr(7)))...)
	c := daqCh(t, openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, twoRaw)), "g", "c")
	if _, err := c.DAQmxScaledFloat64s(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown scaler: %v", err)
	}
	if got := c.DAQmxScaling(); !got.HasCount || got.Count != 3 || len(got.Scales) != 1 {
		t.Fatalf("%+v", got)
	}
	// One scaler is not a stand-in for a different scale id. npTDMS raises
	// KeyError for this file; channel[:] is not the other scaler scaled.
	obj = chDAQmx("/'g'/'c'", 3, 0, []uint32{2}, []dmScaler{{id: 5, typ: 3}}, cat(nscales(2), linearProps(1, 2, 0, u32ptr(0)))...)
	c = daqCh(t, openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, oneRaw)), "g", "c")
	if _, err := c.DAQmxScaledFloat64s(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("scaler id 5, input 0: %v", err)
	}
	// NI_Number_Of_Scales 0 means there is no scale, even if a formula is present.
	obj = chDAQmx("/'g'/'c'", 3, 0, []uint32{2}, one, cat(
		[]dmProp{{name: "NI_Number_Of_Scales", kind: propU32, u32: 0}, {name: "NI_Scaling_Status", kind: propStr, str: "unscaled"}},
		linearProps(0, 2, 0, nil),
	)...)
	c = daqCh(t, openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, oneRaw)), "g", "c")
	got, err := c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, []float64{1, 2, 3})
	if meta := c.DAQmxScaling(); !meta.HasCount || meta.Count != 0 {
		t.Fatalf("%+v", meta)
	}
	// Duplicate scale ids cannot be resolved.
	dup := []dmScaler{{id: 0, typ: 3, off: 0}, {id: 1, typ: 3, off: 2}, {id: 1, typ: 3, off: 2}}
	obj = chDAQmx("/'g'/'c'", 3, 0, []uint32{4}, dup, cat(nscales(3), linearProps(2, 2, 0, u32ptr(1)))...)
	c = daqCh(t, openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, twoRaw)), "g", "c")
	if _, err := c.DAQmxScaledFloat64s(); !errors.Is(err, ErrDAQmx) {
		t.Fatalf("duplicate scaler: %v", err)
	}
	// A cycle through the file's scales.
	obj = chDAQmx("/'g'/'c'", 3, 0, []uint32{2}, one, cat(nscales(3), linearProps(1, 1, 0, u32ptr(2)), linearProps(2, 1, 0, u32ptr(1)))...)
	c = daqCh(t, openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, oneRaw)), "g", "c")
	if _, err := c.DAQmxScaledFloat64s(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("cycle: %v", err)
	}
}

func TestDAQmxSidecarRejectsBadScalerType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daq.tdms")
	if err := os.WriteFile(path, multiChunkTwoBuffers(), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	payload, err := encodeIndexPayload(f)
	if err != nil {
		t.Fatal(err)
	}
	// The DAQmx block for /'g'/'a' starts with the header word, a width
	// count, two widths, a scaler count, then the scaler's data type.
	var hdr [4]byte
	putLE32(hdr[:], RawIndexDAQmxFormatChanging)
	at := bytes.Index(payload, hdr[:])
	if at < 0 {
		t.Fatal("no DAQmx block")
	}
	typeAt := at + 4 + 4 + 2*4 + 4
	r := &ixr{b: payload, off: at}
	if _, err := r.daqmxBlock(f.byPath["/'g'/'a'"].spans, f.byPath["/'g'/'a'"].total, TypeInt16); err != nil {
		t.Fatalf("unmodified block: %v", err)
	}
	bad := append([]byte(nil), payload...)
	putLE32(bad[typeAt:], uint32(TypeString))
	r = &ixr{b: bad, off: at}
	if _, err := r.daqmxBlock(f.byPath["/'g'/'a'"].spans, f.byPath["/'g'/'a'"].total, TypeInt16); err == nil {
		t.Fatal("string scaler type accepted")
	}
	r = &ixr{b: payload, off: at}
	if _, err := r.daqmxBlock(f.byPath["/'g'/'a'"].spans, f.byPath["/'g'/'a'"].total, TypeInt32); err == nil {
		t.Fatal("primary type mismatch accepted")
	}
}

func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func daqmxSeedTyped() []byte {
	o := daqI16(0, 2)
	o.forceType = true
	o.dataType = uint32(TypeInt16)
	return buildDAQmx(daqTOC, []dmObj{o}, i16Raw(7, 8))
}

func daqmxSeedScaled() []byte {
	props := append([]dmProp{{name: "NI_Number_Of_Scales", kind: propU32, u32: 3}}, linearProps(2, 2, 0, u32ptr(1))...)
	obj := chDAQmx("/'g'/'c'", 3, 0, []uint32{4}, []dmScaler{{id: 0, typ: 3}, {id: 1, typ: 3, off: 2}}, props...)
	return buildDAQmx(daqTOC, []dmObj{obj}, i16Raw(1, 10, 2, 20, 3, 30))
}

func daqmxSeedPartialTwoBuffers() []byte {
	b := multiChunkTwoBuffers()
	b = b[:len(b)-3]
	markIncomplete(b)
	return b
}

// Every prefix of a DAQmx metadata block is rejected without a panic.
func TestDAQmxTruncatedMetadata(t *testing.T) {
	for name, file := range map[string][]byte{
		"format-changing": daqmxSeedScaled(),
		"digital":         daqmxSeedDigital(),
		"two buffers":     multiChunkTwoBuffers(),
	} {
		metaLen := int(binary.LittleEndian.Uint64(file[20:28]))
		meta := file[28 : 28+metaLen]
		for cut := 1; cut < metaLen; cut++ {
			short := make([]byte, 28, 28+cut)
			copy(short, encodeLeadIn(daqTOC, uint64(cut), uint64(cut)))
			short = append(short, meta[:cut]...)
			if _, err := NewReader(bytes.NewReader(short)); err == nil {
				t.Fatalf("%s: metadata cut at %d of %d opened", name, cut, metaLen)
			}
		}
	}
}
