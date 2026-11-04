package tdms

// The checks that only bind on a 32-bit build compare against maxInt. These
// tests lower it so a 64-bit build runs each check at its boundary: a value
// exactly at the limit is accepted and one past it is refused. Without them
// the checks are dead code on amd64, and the nightly Gremlins run reports
// their boundary mutants as lived.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func withMaxInt(t *testing.T, v int) {
	t.Helper()
	old := maxInt
	maxInt = v
	t.Cleanup(func() { maxInt = old })
}

func TestFitsIntBoundary(t *testing.T) {
	withMaxInt(t, 80)
	for _, c := range []struct {
		n     int64
		width int
		ok    bool
	}{
		{10, 8, true}, {11, 8, false}, {0, 8, true}, {-1, 8, false}, {1, 0, false}, {80, 1, true}, {81, 1, false},
	} {
		if err := fitsInt(c.n, c.width); (err == nil) != c.ok || (err != nil && !errors.Is(err, ErrUnsupported)) {
			t.Errorf("fitsInt(%d, %d) = %v, want ok=%v", c.n, c.width, err, c.ok)
		}
	}
}

func TestMakeNBoundary(t *testing.T) {
	withMaxInt(t, 80)
	if v, err := makeN[float64](10); err != nil || len(v.([]float64)) != 10 {
		t.Fatalf("at limit: %v %v", v, err)
	}
	if _, err := makeN[float64](11); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("past limit: %v", err)
	}
	if v, err := makeN[int8](0); err != nil || len(v.([]int8)) != 0 {
		t.Fatalf("empty: %v %v", v, err)
	}
	if _, err := makeN[int8](-1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("negative: %v", err)
	}
	// A zero-size element takes no bytes, so any length fits.
	if v, err := makeN[struct{}](1000); err != nil || len(v.([]struct{})) != 1000 {
		t.Fatalf("zero-size: %v %v", v, err)
	}
}

func float64File(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	if err := mustCh(t, w, "g", "c", TypeFloat64).Write(make([]float64, n)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func channelC(t *testing.T, f *File) *Channel {
	t.Helper()
	g, ok := f.Group("g")
	if !ok {
		t.Fatal("no group g")
	}
	c, ok := g.Channel("c")
	if !ok {
		t.Fatal("no channel c")
	}
	return c
}

// metaLen is the first segment's raw-data offset: its metadata length.
func metaLen(b []byte) int { return int(binary.LittleEndian.Uint64(b[20:28])) }

// The channel total check at open, and the byte checks on a read.
func TestChannelAndReadBytesBoundary(t *testing.T) {
	const n = 1000
	file := float64File(t, n)
	if metaLen(file) >= n {
		t.Fatalf("metadata %d bytes; the test needs it below %d", metaLen(file), n)
	}

	withMaxInt(t, n-1)
	if _, err := NewReader(bytes.NewReader(file)); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "more values than int") {
		t.Fatalf("open past the limit: %v", err)
	}

	maxInt = n
	f := openBytes(t, file)
	c := channelC(t, f)
	if c.Len() != n {
		t.Fatalf("len %d", c.Len())
	}
	// n values fit; 8·n bytes do not.
	for name, read := range map[string]func() error{
		"Values":     func() error { _, err := c.Values(); return err },
		"Slice":      func() error { _, err := c.Slice(0, n); return err },
		"ReadValues": func() error { _, err := ReadValues([]*Channel{c}); return err },
		"Float64s":   func() error { _, err := c.Float64s(); return err },
		"Scaled":     func() error { _, err := ScaledWaveform[float64](c); return err },
	} {
		if err := read(); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s past the byte limit: %v", name, err)
		}
	}

	// Exactly at the byte limit every read is served.
	maxInt = 8 * n
	for name, read := range map[string]func() error{
		"Values":     func() error { _, err := c.Values(); return err },
		"Slice":      func() error { _, err := c.Slice(0, n); return err },
		"ReadValues": func() error { _, err := ReadValues([]*Channel{c}); return err },
		"Float64s":   func() error { _, err := c.Float64s(); return err },
		"Int64s":     func() error { _, err := c.Int64s(); return err },
		"Scaled":     func() error { _, err := ScaledWaveform[float64](c); return err },
	} {
		if err := read(); err != nil && !errors.Is(err, ErrType) {
			t.Errorf("%s at the byte limit: %v", name, err)
		}
	}
	// A smaller Slice still reads past the limit.
	maxInt = 8*n - 1
	if _, err := c.Slice(0, n); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Slice one byte past: %v", err)
	}
	if v, err := c.Slice(1, n-1); err != nil || len(v.([]float64)) != n-1 {
		t.Fatalf("Slice at the limit: %v %v", v, err)
	}
}

// An Extended value is 10 bytes on disk and larger in memory; the read
// bound uses the in-memory size.
func TestReadBytesExtendedUsesMemorySize(t *testing.T) {
	const n = 1000
	file := float64File(t, n)
	f := openBytes(t, file)
	c := channelC(t, f)
	// A float64 read at exactly 8·n bytes is not mistaken for Extended.
	withMaxInt(t, 8*n)
	if _, err := c.Values(); err != nil {
		t.Fatal(err)
	}
}

// Segment metadata larger than maxInt is refused at open, and the sidecar
// fingerprint walk refuses it too.
func TestMetadataBytesBoundary(t *testing.T) {
	file := float64File(t, 0)
	m := metaLen(file)
	withMaxInt(t, m-1)
	if _, err := NewReader(bytes.NewReader(file)); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "metadata bytes") {
		t.Fatalf("open past the limit: %v", err)
	}
	maxInt = m
	openBytes(t, file).Close()

	r := bytes.NewReader(file)
	if _, err := segmentFingerprint(r, int64(len(file)), false); err != nil {
		t.Fatalf("fingerprint at the limit: %v", err)
	}
	maxInt = m - 1
	if _, err := segmentFingerprint(r, int64(len(file)), false); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("fingerprint past the limit: %v", err)
	}
}

// A string window larger than maxInt bytes is refused; the window is the
// bytes between its first and last offset, not the end offset.
func TestStringWindowBoundary(t *testing.T) {
	long := strings.Repeat("x", 200)
	var buf bytes.Buffer
	w := newBufWriter(t, &buf)
	if err := mustCh(t, w, "g", "c", TypeString).Write([]string{long, long, long}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c := channelC(t, f)
	withMaxInt(t, 400)
	v, err := c.Slice(1, 2)
	if err != nil || len(v.([]string)) != 2 || v.([]string)[1] != long {
		t.Fatalf("window at the limit: %v", err)
	}
	maxInt = 399
	if _, err := c.Slice(1, 2); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("window past the limit: %v", err)
	}
}

func TestSegmentIndexSamplesBoundary(t *testing.T) {
	sp := span{dataPos: 0, chunkSize: 10, nChunks: 1, nPerChunk: 10, stride: 1, typeSize: 1}
	withMaxInt(t, 10)
	if err := spanListInFile([]span{sp}, nil, 10); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	maxInt = 9
	if err := spanListInFile([]span{sp}, nil, 10); err == nil {
		t.Fatal("past the limit: admitted")
	}
	// Two chunks of 5: no chunk passes the limit, the channel does.
	two := span{dataPos: 0, chunkSize: 5, nChunks: 2, nPerChunk: 5, stride: 1, typeSize: 1}
	if err := spanListInFile([]span{two}, nil, 10); err == nil {
		t.Fatal("channel past the limit: admitted")
	}
	maxInt = 10
	if _, err := spanInFile(two, nil, 10); err != nil {
		t.Fatalf("spanInFile: %v", err)
	}
}

func TestDAQmxCountBoundary(t *testing.T) {
	const n = 1000
	vals := make([]int16, n)
	obj := chDAQmx("/'g'/'c'", n, 0, []uint32{2}, []dmScaler{{typ: 3, buf: 0}})
	file := buildDAQmx(daqTOC, []dmObj{obj}, i16Raw(vals...))
	if metaLen(file) >= n {
		t.Fatalf("metadata %d bytes; the test needs it below %d", metaLen(file), n)
	}
	withMaxInt(t, n)
	f := openBytes(t, file)
	f.Close()
	maxInt = n - 1
	if _, err := NewReader(bytes.NewReader(file)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("past the limit: %v", err)
	}
}

// A scale chain whose input is a non-primary DAQmx scaler widens that
// scaler to []float64, so its count is bounded by maxInt/8.
func TestDAQmxScaleInputBoundary(t *testing.T) {
	two := []dmScaler{{id: 0, typ: 3, off: 0}, {id: 1, typ: 3, off: 2}}
	props := []dmProp{
		{name: "NI_Number_Of_Scales", kind: propU32, u32: 3},
		{name: "NI_Scaling_Status", kind: propStr, str: "unscaled"},
	}
	props = append(props, linearProps(2, 2, 0, u32ptr(1))...)
	obj := chDAQmx("/'g'/'c'", 3, 0, []uint32{4}, two, props...)
	c := daqCh(t, openBytes(t, buildDAQmx(daqTOC, []dmObj{obj}, i16Raw(1, 10, 2, 20, 3, 30))), "g", "c")
	withMaxInt(t, 3*8)
	got, err := c.DAQmxScaledFloat64s()
	if err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	assertEqual(t, got, []float64{20, 40, 60})
	maxInt = 3*8 - 1
	if _, err := c.DAQmxScaledFloat64s(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("past the limit: %v", err)
	}
}
