package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The raw-data offset in the lead-in is where raw data starts. Bytes between
// the last declared object and that offset are skipped, as npTDMS does. NI DAQ
// captures (goban's HSAI04/HSAI05 files) leave a few KiB there; before this
// fix the open failed with "N unused metadata bytes".

// padSegments rewrites each segment of file so that pad bytes of fill follow
// its metadata. The lead-in offsets grow by pad. Raw-only segments (no
// kTocMetaData) are left alone, since a raw-data offset needs metadata.
func padSegments(t *testing.T, file []byte, pad int, fill byte) []byte {
	t.Helper()
	var out []byte
	pos := 0
	for pos < len(file) {
		if len(file)-pos < leadInSize {
			t.Fatalf("short lead-in at %d", pos)
		}
		lead, err := decodeLeadIn(file[pos : pos+leadInSize])
		if err != nil {
			t.Fatal(err)
		}
		bo := orderOf(lead.toc)
		end := pos + leadInSize + int(lead.next)
		metaEnd := pos + leadInSize + int(lead.raw)
		hdr := append([]byte(nil), file[pos:pos+leadInSize]...)
		p := 0
		if lead.toc&TocMetaData != 0 {
			p = pad
		}
		// The incomplete sentinel runs to EOF and stays the sentinel.
		if lead.next == incompleteSegment {
			end = len(file)
		} else {
			bo.PutUint64(hdr[12:20], lead.next+uint64(p))
		}
		bo.PutUint64(hdr[20:28], lead.raw+uint64(p))
		out = append(out, hdr...)
		out = append(out, file[pos+leadInSize:metaEnd]...)
		out = append(out, bytes.Repeat([]byte{fill}, p)...)
		out = append(out, file[metaEnd:end]...)
		pos = end
	}
	return out
}

func padLinearProps(slope, icpt float64) []dmProp {
	return []dmProp{
		{name: "NI_Scaling_Status", kind: propStr, str: "unscaled"},
		{name: "NI_Number_Of_Scales", kind: propU32, u32: 2},
		{name: "NI_Scale[1]_Scale_Type", kind: propStr, str: "Linear"},
		{name: "NI_Scale[1]_Linear_Slope", kind: propF64, f64: slope},
		{name: "NI_Scale[1]_Linear_Y_Intercept", kind: propF64, f64: icpt},
		{name: "NI_Scale[1]_Linear_Input_Source", kind: propU32, u32: 0},
		{name: "NI_ChannelName", kind: propStr, str: "ai"},
		{name: "unit_string", kind: propStr, str: "V"},
	}
}

// hsaiLike is root, group, and four int16 channels sharing one 8-byte
// stride, each with a linear NI_Scale, then a matches-previous segment and a
// raw-only segment.
func hsaiLike() []byte {
	widths := []uint32{8}
	var objs []dmObj
	objs = append(objs,
		objNone("/", dmProp{name: "name", kind: propStr, str: "Launch HSAI05 140121 063230"}),
		objNone("/'HSAI05'"))
	for i := 0; i < 4; i++ {
		objs = append(objs, chDAQmx("/'HSAI05'/'ai"+string(rune('0'+i))+"'", 2, 0, widths,
			[]dmScaler{{typ: 3, off: uint32(2 * i)}}, padLinearProps(0.5*float64(i+1), float64(i))...))
	}
	file := buildDAQmx(daqTOC, objs, i16Raw(0, 10, 20, 30, 1, 11, 21, 31))
	var match []dmObj
	for i := 0; i < 4; i++ {
		match = append(match, objMatch("/'HSAI05'/'ai"+string(rune('0'+i))+"'"))
	}
	file = append(file, buildDAQmx(TocMetaData|TocRawData|TocDAQmxRawData, match, i16Raw(2, 12, 22, 32, 3, 13, 23, 33))...)
	file = append(file, buildDAQmx(TocRawData|TocDAQmxRawData, nil, i16Raw(4, 14, 24, 34, 5, 15, 25, 35))...)
	return file
}

func checkHSAILike(t *testing.T, f *File) {
	t.Helper()
	for i := 0; i < 4; i++ {
		c := daqCh(t, f, "HSAI05", "ai"+string(rune('0'+i)))
		want := make([]int16, 6)
		wantScaled := make([]float64, 6)
		for s := range want {
			want[s] = int16(10*i + s)
			wantScaled[s] = 0.5*float64(i+1)*float64(want[s]) + float64(i)
		}
		assertEqual(t, mustValues(t, c), want)
		got, err := c.DAQmxScaledFloat64s()
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, got, wantScaled)
	}
}

func TestMetadataPaddingDAQmx(t *testing.T) {
	plain := hsaiLike()
	checkHSAILike(t, openBytes(t, plain))
	for _, tc := range []struct {
		pad  int
		fill byte
	}{{2621, 0}, {3607, 0}, {1, 0}, {3607, 0xFF}, {64, 0x5A}} {
		file := padSegments(t, plain, tc.pad, tc.fill)
		if len(file) <= len(plain) {
			t.Fatal("padding did not grow the file")
		}
		checkHSAILike(t, openBytes(t, file))
	}
}

func TestMetadataPaddingDigitalAndMultiBuffer(t *testing.T) {
	widths := []uint32{4, 2}
	objs := []dmObj{
		chDAQmx("/'g'/'a'", 2, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 0}, {id: 1, typ: 3, off: 2, buf: 0}}),
		chDAQmx("/'g'/'c'", 2, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 1}}),
	}
	file := padSegments(t, buildDAQmx(daqTOC, objs, append(i16Raw(1, 101, 2, 102), i16Raw(201, 202)...)), 1000, 0)
	f := openBytes(t, file)
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "a")), []int16{1, 2})
	assertEqual(t, mustValues(t, daqCh(t, f, "g", "c")), []int16{201, 202})
	s, err := daqCh(t, f, "g", "a").DAQmxSlice(1, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, s, []int16{101, 102})

	dig := []dmObj{
		{path: "/'d'/'l0'", kind: dmDAQmx, n: 3, header: RawIndexDAQmxDigitalObserved, widths: []uint32{1}, scalers: []dmScaler{{typ: 0, off: 0}}},
		{path: "/'d'/'l2'", kind: dmDAQmx, n: 3, header: RawIndexDAQmxDigitalObserved, widths: []uint32{1}, scalers: []dmScaler{{typ: 0, off: 2}}},
	}
	f = openBytes(t, padSegments(t, buildDAQmx(daqTOC, dig, []byte{0b101, 0b001, 0b100}), 777, 0))
	assertEqual(t, mustValues(t, daqCh(t, f, "d", "l0")), []uint8{1, 1, 0})
	assertEqual(t, mustValues(t, daqCh(t, f, "d", "l2")), []uint8{1, 0, 1})
}

func TestMetadataPaddingStandardAndMetaOnly(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	cw, err := w.Group("g").Channel("x", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	step := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	step(cw.Write([]float64{1.5, 2.5}))
	step(w.Flush())
	step(cw.SetProperty("unit_string", "V"))
	step(w.Flush()) // metadata-only segment
	step(cw.Write([]float64{3.5}))
	step(w.Close())
	if n := len(leadInOffsets(t, buf.Bytes())); n < 3 {
		t.Fatalf("writer produced %d segments, want 3", n)
	}
	file := padSegments(t, buf.Bytes(), 333, 0xEE)
	f := openBytes(t, file)
	g, _ := f.Group("g")
	c, _ := g.Channel("x")
	assertEqual(t, mustValues(t, c), []float64{1.5, 2.5, 3.5})
	if p, ok := c.Property("unit_string"); !ok || p.Value != "V" {
		t.Fatalf("unit_string = %v, %v", p, ok)
	}
}

func TestMetadataPaddingBigEndian(t *testing.T) {
	file := beChannelFile(t, TypeInt32, 2, []byte{0, 0, 0, 7, 0, 0, 0, 9})
	f := openBytes(t, padSegments(t, file, 50, 0))
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []int32{7, 9})
}

// The sidecar path builds and reuses an index over a padded file.
func TestMetadataPaddingSegmentIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hsai.tdms")
	if err := os.WriteFile(path, padSegments(t, hsaiLike(), 2621, 0), 0o644); err != nil {
		t.Fatal(err)
	}
	side, err := BuildSegmentIndexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.SegmentIndexUsed() {
		t.Fatal("sidecar not used")
	}
	checkHSAILike(t, f)
}

// The skip covers only bytes after a complete object list. An object list
// that runs out inside the declared metadata, or a raw-data offset past EOF,
// still fails the open.
func TestMetadataPaddingStillRejectsCorruptMetadata(t *testing.T) {
	padded := padSegments(t, hsaiLike(), 2621, 0)
	if _, err := NewReader(bytes.NewReader(padded)); err != nil {
		t.Fatal(err)
	}
	// The first segment's object count is the u32 after its lead-in. One more
	// object than declared would be read from the zero gap: an empty path.
	for _, fill := range []byte{0, 0xFF} {
		file := padSegments(t, hsaiLike(), 2621, fill)
		n := binary.LittleEndian.Uint32(file[leadInSize:])
		binary.LittleEndian.PutUint32(file[leadInSize:], n+1)
		if _, err := NewReader(bytes.NewReader(file)); !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrTruncated) {
			t.Fatalf("fill %#x: object count past the list: %v", fill, err)
		}
	}
	// An object list cut short inside its own metadata still errors: shrink
	// the declared metadata so it ends inside the last object.
	plain := hsaiLike()
	lead, err := decodeLeadIn(plain[:leadInSize])
	if err != nil {
		t.Fatal(err)
	}
	cut := append([]byte(nil), plain...)
	binary.LittleEndian.PutUint64(cut[20:28], lead.raw-3)
	if _, err := NewReader(bytes.NewReader(cut)); err == nil {
		t.Fatal("metadata cut inside an object opened")
	}
	// A raw-data offset past EOF is a truncated file, padded or not. A
	// lenient open drops that segment as a torn tail and keeps the ones
	// before it.
	offs := leadInOffsets(t, padded)
	last := offs[len(offs)-2] // the matches-previous segment has metadata
	past := append([]byte(nil), padded[:last+leadInSize+100]...)
	if _, err := NewReader(bytes.NewReader(past)); !errors.Is(err, ErrTruncated) {
		t.Fatalf("raw-data offset past EOF: %v", err)
	}
	lf, err := NewReader(bytes.NewReader(past), WithLenientTail())
	if err != nil {
		t.Fatalf("lenient raw-data offset past EOF: %v", err)
	}
	st := lf.Status()
	if !st.Truncated || st.DroppedTail != int64(len(past)-last) || !errors.Is(st.TailError, ErrTruncated) || lf.SegmentCount() != len(offs)-2 {
		t.Fatalf("lenient torn tail: %#v segments %d", st, lf.SegmentCount())
	}
}

// A padded final segment under the incomplete sentinel, and a padded final
// segment whose raw bytes run past EOF under WithLenientTail.
func TestMetadataPaddingIncompleteAndLenientTail(t *testing.T) {
	plain := hsaiLike()
	offs := leadInOffsets(t, plain)
	mid := offs[1] // matches-previous segment: metadata and raw data
	live := append([]byte(nil), plain[:offs[2]]...)
	binary.LittleEndian.PutUint64(live[mid+12:mid+20], incompleteSegment)
	file := padSegments(t, live, 3607, 0)
	f := openBytes(t, file)
	if st := f.Status(); !st.Incomplete {
		t.Fatalf("status %+v", st)
	}
	c := daqCh(t, f, "HSAI05", "ai1")
	assertEqual(t, mustValues(t, c), []int16{10, 11, 12, 13})

	padded := padSegments(t, plain[:offs[2]], 3607, 0)
	short := padded[:len(padded)-4] // half of the second segment's last sample row
	if _, err := NewReader(bytes.NewReader(short)); !errors.Is(err, ErrTruncated) {
		t.Fatalf("strict open of a short padded tail: %v", err)
	}
	f, err := NewReader(bytes.NewReader(short), WithLenientTail())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if st := f.Status(); !st.Truncated || st.UnusedTail != 4 {
		t.Fatalf("status %+v", st)
	}
	assertEqual(t, mustValues(t, daqCh(t, f, "HSAI05", "ai0")), []int16{0, 1, 2})
}

// The sidecar fingerprint covers the whole declared metadata, gap included.
// Changing a gap byte in place, with size and modification time restored,
// is a miss under the default check and still opens by parsing.
func TestMetadataPaddingSidecarFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hsai.tdms")
	file := padSegments(t, hsaiLike(), 2621, 0)
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}
	ok, err := SegmentIndexValid(path)
	if err != nil || !ok {
		t.Fatalf("fresh sidecar valid %v %v", ok, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	lead, err := decodeLeadIn(file[:leadInSize])
	if err != nil {
		t.Fatal(err)
	}
	// Last byte of the first segment's gap.
	at := leadInSize + int(lead.raw) - 1
	edited := append([]byte(nil), file...)
	edited[at] = 0x7F
	fh, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteAt(edited[at:at+1], int64(at)); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}
	mt := fi.ModTime()
	if err := os.Chtimes(path, time.Now(), mt); err != nil {
		t.Fatal(err)
	}
	ok, err = SegmentIndexValid(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("sidecar still valid after a gap byte changed")
	}
	f, err := Open(path, WithSegmentIndex(SegmentIndexPath(path)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.SegmentIndexUsed() {
		t.Fatal("stale sidecar used")
	}
	checkHSAILike(t, f)
}
