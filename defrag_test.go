package tdms

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// fragmented builds a multi-segment file that exercises contiguous and
// unequal-length channels, several groups, channels that appear and disappear,
// properties overwritten across segments, and a range of fixed-size types.
func fragmented(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	if err := w.SetProperty("author", "test"); err != nil {
		t.Fatal(err)
	}
	a := mustCh(t, w, "sensors", "a", TypeInt32)
	b := mustCh(t, w, "sensors", "b", TypeFloat64)
	if err := a.SetProperty("unit", "count"); err != nil {
		t.Fatal(err)
	}
	g2 := w.Group("meta")
	if err := g2.SetProperty("site", "lab"); err != nil {
		t.Fatal(err)
	}
	ts := mustCh(t, w, "meta", "clock", TypeTimestamp)
	bl := mustCh(t, w, "meta", "flag", TypeBool)

	// Segment 1: all four channels, unequal lengths.
	if err := a.Write([]int32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]float64{1.5, 2.5}); err != nil {
		t.Fatal(err)
	}
	if err := ts.Write([]Timestamp{{Seconds: 10, Fraction: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := bl.Write([]bool{true, false, true}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	// Segment 2: only a and b; b's property overwritten.
	if err := a.Write([]int32{4, 5}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetProperty("unit", "ticks"); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]float64{3.5}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	// Segment 3: a new channel appears, clock reappears.
	c := mustCh(t, w, "sensors", "c", TypeUint16)
	if err := a.Write([]int32{6}); err != nil {
		t.Fatal(err)
	}
	if err := c.Write([]uint16{100, 200}); err != nil {
		t.Fatal(err)
	}
	if err := ts.Write([]Timestamp{{Seconds: 20, Fraction: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// assertSameFile checks that two TDMS files expose the same tree, order,
// values, and final property values.
func assertSameFile(t *testing.T, in, out *File) {
	t.Helper()
	assertSameProps(t, "/", in.Properties(), out.Properties())
	ig, og := in.Groups(), out.Groups()
	if len(ig) != len(og) {
		t.Fatalf("groups: in %d, out %d", len(ig), len(og))
	}
	for i := range ig {
		if ig[i].Name() != og[i].Name() {
			t.Fatalf("group %d: in %q, out %q", i, ig[i].Name(), og[i].Name())
		}
		assertSameProps(t, ig[i].Path(), ig[i].Properties(), og[i].Properties())
		ic, oc := ig[i].Channels(), og[i].Channels()
		if len(ic) != len(oc) {
			t.Fatalf("group %q channels: in %d, out %d", ig[i].Name(), len(ic), len(oc))
		}
		for j := range ic {
			if ic[j].Name() != oc[j].Name() {
				t.Fatalf("channel %d: in %q, out %q", j, ic[j].Name(), oc[j].Name())
			}
			if ic[j].DataType() != oc[j].DataType() {
				t.Fatalf("channel %q type: in %s, out %s", ic[j].Path(), ic[j].DataType(), oc[j].DataType())
			}
			iv, err := ic[j].Values()
			if err != nil {
				t.Fatal(err)
			}
			ov, err := oc[j].Values()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(iv, ov) {
				t.Fatalf("channel %q values:\n in  %#v\n out %#v", ic[j].Path(), iv, ov)
			}
			assertSameProps(t, ic[j].Path(), ic[j].Properties(), oc[j].Properties())
		}
	}
}

func assertSameProps(t *testing.T, path string, in, out []Property) {
	t.Helper()
	im := map[string]Property{}
	for _, p := range in {
		im[p.Name] = p
	}
	om := map[string]Property{}
	for _, p := range out {
		om[p.Name] = p
	}
	if len(im) != len(om) {
		t.Fatalf("%s props: in %v, out %v", path, in, out)
	}
	for name, ip := range im {
		op, ok := om[name]
		if !ok {
			t.Fatalf("%s: property %q missing from output", path, name)
		}
		if ip.Type != op.Type || !reflect.DeepEqual(ip.Value, op.Value) {
			t.Fatalf("%s property %q: in %+v, out %+v", path, name, ip, op)
		}
	}
}

func TestDefragmentRoundTrip(t *testing.T) {
	src := fragmented(t)
	in := openBytes(t, src)
	if in.SegmentCount() < 2 {
		t.Fatalf("fixture has %d segments, want several", in.SegmentCount())
	}

	var out bytes.Buffer
	if err := Defragment(&out, bytes.NewReader(src)); err != nil {
		t.Fatalf("defragment: %v", err)
	}

	of := openBytes(t, out.Bytes())
	if of.SegmentCount() != 1 {
		t.Fatalf("defragmented file has %d segments, want 1", of.SegmentCount())
	}
	assertSameFile(t, in, of)
}

func TestDefragmentStrings(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	num := mustCh(t, w, "g", "num", TypeInt32)
	str := mustCh(t, w, "g", "label", TypeString)
	if err := num.Write([]int32{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := str.Write([]string{"alpha", "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := num.Write([]int32{3}); err != nil {
		t.Fatal(err)
	}
	if err := str.Write([]string{"gamma"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	src := buf.Bytes()
	in := openBytes(t, src)

	var out bytes.Buffer
	if err := Defragment(&out, bytes.NewReader(src)); err != nil {
		t.Fatalf("defragment: %v", err)
	}
	of := openBytes(t, out.Bytes())
	if of.SegmentCount() != 1 {
		t.Fatalf("segments %d, want 1", of.SegmentCount())
	}
	assertSameFile(t, in, of)
}

func TestDefragmentMetadataOnly(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	if err := w.SetProperty("note", "empty"); err != nil {
		t.Fatal(err)
	}
	g := w.Group("g")
	if err := g.SetProperty("k", int32(7)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	src := buf.Bytes()
	in := openBytes(t, src)

	var out bytes.Buffer
	if err := Defragment(&out, bytes.NewReader(src)); err != nil {
		t.Fatalf("defragment: %v", err)
	}
	of := openBytes(t, out.Bytes())
	assertSameFile(t, in, of)
}

// TestDefragmentVoidChannel confirms a channel that carries properties but no
// samples survives as a metadata object alongside channels that do have data.
func TestDefragmentVoidChannel(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	data := mustCh(t, w, "g", "data", TypeInt32)
	empty := mustCh(t, w, "g", "empty", TypeFloat64)
	if err := empty.SetProperty("note", "no samples"); err != nil {
		t.Fatal(err)
	}
	if err := data.Write([]int32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	// empty is never written.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	src := buf.Bytes()
	in := openBytes(t, src)

	// Sanity: the source already sees empty as a void, zero-length channel.
	g, _ := in.Group("g")
	ec, _ := g.Channel("empty")
	if ec == nil || ec.Len() != 0 {
		t.Fatalf("source empty channel: %+v", ec)
	}

	var out bytes.Buffer
	if err := Defragment(&out, bytes.NewReader(src)); err != nil {
		t.Fatalf("defragment: %v", err)
	}
	of := openBytes(t, out.Bytes())
	assertSameFile(t, in, of)
}

func TestDefragmentRejectsDAQmx(t *testing.T) {
	file := buildDAQmx(daqTOC, []dmObj{daqI16(RawIndexDAQmxFormatChanging, 4)}, i16Raw(1, 2, 3, 4))
	var out bytes.Buffer
	err := Defragment(&out, bytes.NewReader(file))
	if !errors.Is(err, ErrDAQmx) {
		t.Fatalf("err = %v, want ErrDAQmx", err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %d bytes before rejecting", out.Len())
	}
}

func TestDefragmentRejectsBigEndian(t *testing.T) {
	// Big-endian int16 channel, two samples.
	file := beChannelFile(t, TypeInt16, 2, []byte{0xFF, 0xFE, 0x01, 0x2C})
	var out bytes.Buffer
	err := Defragment(&out, bytes.NewReader(file))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %d bytes before rejecting", out.Len())
	}
}

func TestDefragmentFileInPlace(t *testing.T) {
	src := fragmented(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	in := openBytes(t, src)

	if err := DefragmentFile(path); err != nil {
		t.Fatalf("defragment file: %v", err)
	}
	of, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer of.Close()
	if of.SegmentCount() != 1 {
		t.Fatalf("segments %d, want 1", of.SegmentCount())
	}
	assertSameFile(t, in, of)

	// No temporary files left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "run.tdms" {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestDefragmentFileRejectLeavesOriginal(t *testing.T) {
	file := beChannelFile(t, TypeInt16, 2, []byte{0xFF, 0xFE, 0x01, 0x2C})
	dir := t.TempDir()
	path := filepath.Join(dir, "be.tdms")
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DefragmentFile(path); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, file) {
		t.Fatal("original file was modified")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestDefragmentToFile(t *testing.T) {
	src := fragmented(t)
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "in.tdms")
	dstPath := filepath.Join(dir, "out.tdms")
	if err := os.WriteFile(srcPath, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DefragmentToFile(dstPath, srcPath); err != nil {
		t.Fatalf("defragment: %v", err)
	}
	in := openBytes(t, src)
	of, err := Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer of.Close()
	assertSameFile(t, in, of)
}

// TestDefragmentTimeProperty confirms a time-valued property survives, since
// the writer maps time.Time to a Timestamp.
func TestDefragmentTimeProperty(t *testing.T) {
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	ch := mustCh(t, w, "g", "c", TypeFloat64)
	when := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := ch.SetProperty("captured", when); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	src := buf.Bytes()
	in := openBytes(t, src)
	var out bytes.Buffer
	if err := Defragment(&out, bytes.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	of := openBytes(t, out.Bytes())
	assertSameFile(t, in, of)
}
