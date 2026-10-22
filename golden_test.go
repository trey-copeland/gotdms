package tdms

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

// unhex decodes a hex dump. Spaces and newlines are ignored. The bytes are
// assembled here, not by Writer, so a shared encode/decode bug cannot hide.
func unhex(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(stripHex(s))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func stripHex(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// The first writing iteration from NI's "TDMS File Format Internal Structure"
// (Optimization / Incremental Meta Information). Hex is copied from that
// article; this test never calls Writer.
func TestGoldenNIFirstSegment(t *testing.T) {
	buf := unhex(t, `
		54 44 53 6D 0E 00 00 00 69 12 00 00 8F 00 00 00 00 00 00 00 77 00 00 00 00 00 00 00
		02 00 00 00
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 31 27
		14 00 00 00 03 00 00 00 01 00 00 00 03 00 00 00 00 00 00 00
		01 00 00 00
		04 00 00 00 70 72 6F 70 20 00 00 00 05 00 00 00 76 61 6C 69 64
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 32 27
		14 00 00 00 03 00 00 00 01 00 00 00 03 00 00 00 00 00 00 00
		00 00 00 00
		01 00 00 00 02 00 00 00 03 00 00 00
		04 00 00 00 05 00 00 00 06 00 00 00
	`)
	f := openBytes(t, buf)
	if f.Version() != Version20 || f.SegmentCount() != 1 {
		t.Fatalf("version %d segments %d", f.Version(), f.SegmentCount())
	}
	g, ok := f.Group("group")
	if !ok {
		t.Fatal("missing group")
	}
	c1, ok := g.Channel("channel1")
	if !ok {
		t.Fatal("missing channel1")
	}
	c2, _ := g.Channel("channel2")
	assertEqual(t, mustValues(t, c1), []int32{1, 2, 3})
	assertEqual(t, mustValues(t, c2), []int32{4, 5, 6})
	p, ok := c1.Property("prop")
	if !ok || p.Type != TypeString || p.Value != "valid" {
		t.Fatalf("prop = %+v ok=%v", p, ok)
	}
	if _, ok := c2.Property("prop"); ok {
		t.Fatal("channel2 should have no properties")
	}
}

// The article's second writing iteration appends one raw chunk to the same
// segment and patches the next-segment offset from 0x8F to 0xA7.
func TestGoldenNITwoChunksOneSegment(t *testing.T) {
	buf := unhex(t, `
		54 44 53 6D 0E 00 00 00 69 12 00 00 8F 00 00 00 00 00 00 00 77 00 00 00 00 00 00 00
		02 00 00 00
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 31 27
		14 00 00 00 03 00 00 00 01 00 00 00 03 00 00 00 00 00 00 00
		01 00 00 00
		04 00 00 00 70 72 6F 70 20 00 00 00 05 00 00 00 76 61 6C 69 64
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 32 27
		14 00 00 00 03 00 00 00 01 00 00 00 03 00 00 00 00 00 00 00
		00 00 00 00
		01 00 00 00 02 00 00 00 03 00 00 00
		04 00 00 00 05 00 00 00 06 00 00 00
	`)
	extra := unhex(t, `
		01 00 00 00 02 00 00 00 03 00 00 00
		04 00 00 00 05 00 00 00 06 00 00 00
	`)
	buf = append(buf, extra...)
	binary.LittleEndian.PutUint64(buf[12:20], 0xA7)
	f := openBytes(t, buf)
	g, _ := f.Group("group")
	c1, _ := g.Channel("channel1")
	c2, _ := g.Channel("channel2")
	assertEqual(t, mustValues(t, c1), []int32{1, 2, 3, 1, 2, 3})
	assertEqual(t, mustValues(t, c2), []int32{4, 5, 6, 4, 5, 6})
	if f.SegmentCount() != 1 {
		t.Fatalf("segments %d", f.SegmentCount())
	}
}

// The five writing iterations in NI's "TDMS File Format Internal Structure"
// (Optimization / Incremental Meta Information), concatenated. The first
// segment is the article's updated lead-in (next offset 0xA7, two chunks).
// Hex is copied from that article. The file is not produced by Writer, and
// this repository has no LabVIEW to regenerate it.
func TestGoldenNIFiveSegments(t *testing.T) {
	buf := unhex(t, `
		54 44 53 6D 0E 00 00 00 69 12 00 00 A7 00 00 00 00 00 00 00 77 00 00 00 00 00 00 00
		02 00 00 00
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 31 27
		14 00 00 00 03 00 00 00 01 00 00 00 03 00 00 00 00 00 00 00
		01 00 00 00
		04 00 00 00 70 72 6F 70 20 00 00 00 05 00 00 00 76 61 6C 69 64
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 32 27
		14 00 00 00 03 00 00 00 01 00 00 00 03 00 00 00 00 00 00 00
		00 00 00 00
		01 00 00 00 02 00 00 00 03 00 00 00
		04 00 00 00 05 00 00 00 06 00 00 00
		01 00 00 00 02 00 00 00 03 00 00 00
		04 00 00 00 05 00 00 00 06 00 00 00

		54 44 53 6D 0A 00 00 00 69 12 00 00 50 00 00 00 00 00 00 00 38 00 00 00 00 00 00 00
		01 00 00 00
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 31 27
		00 00 00 00
		01 00 00 00
		04 00 00 00 70 72 6F 70 20 00 00 00 05 00 00 00 65 72 72 6F 72
		01 00 00 00 02 00 00 00 03 00 00 00
		04 00 00 00 05 00 00 00 06 00 00 00

		54 44 53 6D 0A 00 00 00 69 12 00 00 5E 00 00 00 00 00 00 00 32 00 00 00 00 00 00 00
		01 00 00 00
		12 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 76 6F 6C 74 61 67 65 27
		14 00 00 00 03 00 00 00 01 00 00 00 05 00 00 00 00 00 00 00
		00 00 00 00
		01 00 00 00 02 00 00 00 03 00 00 00
		04 00 00 00 05 00 00 00 06 00 00 00
		07 00 00 00 08 00 00 00 09 00 00 00 0A 00 00 00 0B 00 00 00

		54 44 53 6D 0A 00 00 00 69 12 00 00 BF 00 00 00 00 00 00 00 33 00 00 00 00 00 00 00
		01 00 00 00
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 32 27
		14 00 00 00 03 00 00 00 01 00 00 00 1B 00 00 00 00 00 00 00
		00 00 00 00
		01 00 00 00 02 00 00 00 03 00 00 00
		01 00 00 00 02 00 00 00 03 00 00 00 04 00 00 00 05 00 00 00 06 00 00 00
		07 00 00 00 08 00 00 00 09 00 00 00 0A 00 00 00 0B 00 00 00 0C 00 00 00
		0D 00 00 00 0E 00 00 00 0F 00 00 00 10 00 00 00 11 00 00 00 12 00 00 00
		13 00 00 00 14 00 00 00 15 00 00 00 16 00 00 00 17 00 00 00 18 00 00 00
		19 00 00 00 1A 00 00 00 1B 00 00 00
		07 00 00 00 08 00 00 00 09 00 00 00 0A 00 00 00 0B 00 00 00

		54 44 53 6D 0E 00 00 00 69 12 00 00 61 00 00 00 00 00 00 00 41 00 00 00 00 00 00 00
		02 00 00 00
		13 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 63 68 61 6E 6E 65 6C 31 27
		00 00 00 00 00 00 00 00
		12 00 00 00 2F 27 67 72 6F 75 70 27 2F 27 76 6F 6C 74 61 67 65 27
		00 00 00 00 00 00 00 00
		01 00 00 00 02 00 00 00 03 00 00 00
		07 00 00 00 08 00 00 00 09 00 00 00 0A 00 00 00 0B 00 00 00
	`)
	f := openBytes(t, buf)
	if f.SegmentCount() != 5 {
		t.Fatalf("segments %d", f.SegmentCount())
	}
	g, ok := f.Group("group")
	if !ok {
		t.Fatal("group")
	}
	c1, _ := g.Channel("channel1")
	c2, _ := g.Channel("channel2")
	cv, _ := g.Channel("voltage")
	var want1 []int32
	for i := 0; i < 6; i++ {
		want1 = append(want1, 1, 2, 3)
	}
	assertEqual(t, mustValues(t, c1), want1)
	var want2 []int32
	for i := 0; i < 4; i++ {
		want2 = append(want2, 4, 5, 6)
	}
	for i := int32(1); i <= 27; i++ {
		want2 = append(want2, i)
	}
	assertEqual(t, mustValues(t, c2), want2)
	var wantV []int32
	for i := 0; i < 3; i++ {
		wantV = append(wantV, 7, 8, 9, 10, 11)
	}
	assertEqual(t, mustValues(t, cv), wantV)
	p, ok := c1.Property("prop")
	if !ok || p.Value != "error" {
		t.Fatalf("prop = %+v ok=%v", p, ok)
	}
}

func TestGoldenStringAndTimestamp(t *testing.T) {
	// Built with encoding/binary only: one string channel "ab","c","" and one
	// timestamp channel at the TDMS epoch and one second later.
	var meta bytes.Buffer
	putU32 := func(v uint32) { binary.Write(&meta, binary.LittleEndian, v) }
	putU64 := func(v uint64) { binary.Write(&meta, binary.LittleEndian, v) }
	putStr := func(s string) { putU32(uint32(len(s))); meta.WriteString(s) }
	strings := []string{"ab", "c", ""}
	var payload int
	for _, s := range strings {
		payload += len(s)
	}
	total := uint64(4*len(strings) + payload)
	putU32(2)
	putStr("/'g'/'s'")
	putU32(28)
	putU32(uint32(TypeString))
	putU32(1)
	putU64(uint64(len(strings)))
	putU64(total)
	putU32(0)
	putStr("/'g'/'t'")
	putU32(20)
	putU32(uint32(TypeTimestamp))
	putU32(1)
	putU64(2)
	putU32(0)

	var raw bytes.Buffer
	off := uint32(0)
	offs := make([]byte, 4*len(strings))
	for i, s := range strings {
		off += uint32(len(s))
		binary.LittleEndian.PutUint32(offs[i*4:], off)
	}
	raw.Write(offs)
	for _, s := range strings {
		raw.WriteString(s)
	}
	var ts [32]byte
	// value 0: epoch. value 1: fraction 0, seconds 1.
	binary.LittleEndian.PutUint64(ts[16:24], 0)
	binary.LittleEndian.PutUint64(ts[24:32], 1)
	raw.Write(ts[:])

	var file bytes.Buffer
	lead := make([]byte, 28)
	copy(lead[0:4], "TDSm")
	binary.LittleEndian.PutUint32(lead[4:8], TocMetaData|TocNewObjList|TocRawData)
	binary.LittleEndian.PutUint32(lead[8:12], Version20)
	binary.LittleEndian.PutUint64(lead[12:20], uint64(meta.Len()+raw.Len()))
	binary.LittleEndian.PutUint64(lead[20:28], uint64(meta.Len()))
	file.Write(lead)
	file.Write(meta.Bytes())
	file.Write(raw.Bytes())

	f := openBytes(t, file.Bytes())
	g, ok := f.Group("g")
	if !ok {
		t.Fatal("group")
	}
	cs, _ := g.Channel("s")
	assertEqual(t, mustValues(t, cs), []string{"ab", "c", ""})
	ct, _ := g.Channel("t")
	got := mustValues(t, ct).([]Timestamp)
	if len(got) != 2 || got[0].Seconds != 0 || got[0].Fraction != 0 || got[1].Seconds != 1 || got[1].Fraction != 0 {
		t.Fatalf("%+v", got)
	}
	times, err := ct.Times()
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	if !times[0].Equal(epoch) || !times[1].Equal(epoch.Add(time.Second)) {
		t.Fatalf("%v", times)
	}
}

func TestGoldenBigEndian(t *testing.T) {
	// One big-endian int32 channel holding 0x01020304, plus a timestamp of
	// exactly one second past the epoch. Field order for the timestamp follows
	// the choice documented on Timestamp: seconds, then fraction.
	var meta bytes.Buffer
	bo := binary.BigEndian
	putU32 := func(v uint32) { binary.Write(&meta, bo, v) }
	putU64 := func(v uint64) { binary.Write(&meta, bo, v) }
	putStr := func(s string) {
		binary.Write(&meta, bo, uint32(len(s)))
		meta.WriteString(s)
	}
	putU32(2)
	putStr("/'g'/'n'")
	putU32(20)
	putU32(uint32(TypeInt32))
	putU32(1)
	putU64(1)
	putU32(0)
	putStr("/'g'/'t'")
	putU32(20)
	putU32(uint32(TypeTimestamp))
	putU32(1)
	putU64(1)
	putU32(0)

	var raw bytes.Buffer
	binary.Write(&raw, bo, int32(0x01020304))
	binary.Write(&raw, bo, int64(1))  // seconds
	binary.Write(&raw, bo, uint64(0)) // fraction

	file := make([]byte, 28)
	copy(file[0:4], "TDSm")
	binary.LittleEndian.PutUint32(file[4:8], TocMetaData|TocNewObjList|TocRawData|TocBigEndian)
	bo.PutUint32(file[8:12], Version20)
	bo.PutUint64(file[12:20], uint64(meta.Len()+raw.Len()))
	bo.PutUint64(file[20:28], uint64(meta.Len()))
	file = append(file, meta.Bytes()...)
	file = append(file, raw.Bytes()...)

	f := openBytes(t, file)
	g, _ := f.Group("g")
	n, _ := g.Channel("n")
	assertEqual(t, mustValues(t, n), []int32{0x01020304})
	cur := n.Cursor(1)
	if !cur.Next() {
		t.Fatal(cur.Err())
	}
	assertEqual(t, cur.Values(), []int32{0x01020304})
	ts, _ := g.Channel("t")
	got := mustValues(t, ts).([]Timestamp)
	if got[0].Seconds != 1 || got[0].Fraction != 0 {
		t.Fatalf("%+v", got[0])
	}
}

func TestGoldenExtendedAndInterleaved(t *testing.T) {
	// Interleaved int16 channel [1,2] and float64 channel [3.5, 4.5], one chunk.
	// Plus a separate check is not needed; this file is only the interleaved pair.
	var meta bytes.Buffer
	putU32 := func(v uint32) { binary.Write(&meta, binary.LittleEndian, v) }
	putU64 := func(v uint64) { binary.Write(&meta, binary.LittleEndian, v) }
	putStr := func(s string) { putU32(uint32(len(s))); meta.WriteString(s) }
	putU32(2)
	for _, spec := range []struct {
		path string
		dt   DataType
	}{
		{"/'g'/'a'", TypeInt16},
		{"/'g'/'b'", TypeFloat64},
	} {
		putStr(spec.path)
		putU32(20)
		putU32(uint32(spec.dt))
		putU32(1)
		putU64(2)
		putU32(0)
	}
	var raw bytes.Buffer
	writeSample := func(i int16, f float64) {
		binary.Write(&raw, binary.LittleEndian, i)
		binary.Write(&raw, binary.LittleEndian, f)
	}
	writeSample(1, 3.5)
	writeSample(2, 4.5)
	file := finishSegment(t, TocMetaData|TocNewObjList|TocRawData|TocInterleavedData, meta.Bytes(), raw.Bytes())
	f := openBytes(t, file)
	g, _ := f.Group("g")
	a, _ := g.Channel("a")
	b, _ := g.Channel("b")
	assertEqual(t, mustValues(t, a), []int16{1, 2})
	assertEqual(t, mustValues(t, b), []float64{3.5, 4.5})
}

func TestGoldenQuotedName(t *testing.T) {
	// Group "Dr. T's Events", channel "Time", one uint8. Path bytes are the
	// article's escaping example, not Writer's quoteName.
	path := "/'Dr. T''s Events'/'Time'"
	var meta bytes.Buffer
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint32(len(path)))
	meta.WriteString(path)
	binary.Write(&meta, binary.LittleEndian, uint32(20))
	binary.Write(&meta, binary.LittleEndian, uint32(TypeUint8))
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint64(1))
	binary.Write(&meta, binary.LittleEndian, uint32(0))
	file := finishSegment(t, TocMetaData|TocNewObjList|TocRawData, meta.Bytes(), []byte{0x2A})
	f := openBytes(t, file)
	g, ok := f.Group("Dr. T's Events")
	if !ok {
		t.Fatalf("groups %+v", names(f))
	}
	ch, ok := g.Channel("Time")
	if !ok {
		t.Fatal("channel")
	}
	assertEqual(t, mustValues(t, ch), []uint8{0x2A})
}

func TestGoldenIncompleteSentinel(t *testing.T) {
	// Index promises 4 int32 values. The segment is the final one (next offset
	// all 0xFF) and only two values were flushed to disk.
	var meta bytes.Buffer
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	p := "/'g'/'c'"
	binary.Write(&meta, binary.LittleEndian, uint32(len(p)))
	meta.WriteString(p)
	binary.Write(&meta, binary.LittleEndian, uint32(20))
	binary.Write(&meta, binary.LittleEndian, uint32(TypeInt32))
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint64(4))
	binary.Write(&meta, binary.LittleEndian, uint32(0))
	raw := make([]byte, 8)
	binary.LittleEndian.PutUint32(raw[0:4], 1)
	binary.LittleEndian.PutUint32(raw[4:8], 2)
	file := finishSegment(t, TocMetaData|TocNewObjList|TocRawData, meta.Bytes(), raw)
	binary.LittleEndian.PutUint64(file[12:20], incompleteSegment)
	f := openBytes(t, file)
	ch, _ := f.Group("g")
	c, _ := ch.Channel("c")
	assertEqual(t, mustValues(t, c), []int32{1, 2})
	st := f.Status()
	if !st.Incomplete || st.Complete || !st.PartialTail || st.UnusedTail != 0 {
		t.Fatalf("status %#v", st)
	}
}

func TestGoldenVersion10(t *testing.T) {
	var meta bytes.Buffer
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	p := "/'g'/'c'"
	binary.Write(&meta, binary.LittleEndian, uint32(len(p)))
	meta.WriteString(p)
	binary.Write(&meta, binary.LittleEndian, uint32(20))
	binary.Write(&meta, binary.LittleEndian, uint32(TypeInt8))
	binary.Write(&meta, binary.LittleEndian, uint32(1))
	binary.Write(&meta, binary.LittleEndian, uint64(1))
	binary.Write(&meta, binary.LittleEndian, uint32(0))
	file := finishSegment(t, TocMetaData|TocNewObjList|TocRawData, meta.Bytes(), []byte{7})
	binary.LittleEndian.PutUint32(file[8:12], Version10)
	f := openBytes(t, file)
	if f.Version() != Version10 {
		t.Fatal(f.Version())
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	assertEqual(t, mustValues(t, c), []int8{7})
}

func TestMalformedNoPanic(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		[]byte("nope"),
		[]byte("TDSh"),
		[]byte("TDSm"),
		bytes.Repeat([]byte{0xff}, 27),
		bytes.Repeat([]byte{0xff}, 28),
		append([]byte("TDSm"), bytes.Repeat([]byte{0}, 24)...),
	}
	// A lead-in that claims a huge metadata section past EOF.
	huge := make([]byte, 28)
	copy(huge, "TDSm")
	binary.LittleEndian.PutUint32(huge[4:8], TocMetaData)
	binary.LittleEndian.PutUint32(huge[8:12], Version20)
	binary.LittleEndian.PutUint64(huge[12:20], 1000)
	binary.LittleEndian.PutUint64(huge[20:28], 1000)
	cases = append(cases, huge)

	for i, c := range cases {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("case %d panicked: %v", i, rec)
				}
			}()
			f, err := NewReader(bytes.NewReader(c))
			if err != nil {
				return
			}
			defer f.Close()
			_ = f.Properties()
			for _, g := range f.Groups() {
				for _, ch := range g.Channels() {
					_, _ = ch.Values()
				}
			}
		}()
	}
	if _, err := NewReader(bytes.NewReader(nil)); err == nil {
		t.Fatal("empty file should fail")
	}
	_, err := NewReader(bytes.NewReader([]byte("TDSh" + string(bytes.Repeat([]byte{0}, 24)))))
	// Tag check happens after a full lead-in. A short TDSh is truncated; a full one is ErrIndexFile.
	lead := make([]byte, 28)
	copy(lead, "TDSh")
	binary.LittleEndian.PutUint32(lead[8:12], Version20)
	_, err = NewReader(bytes.NewReader(lead))
	if !errors.Is(err, ErrIndexFile) {
		t.Fatalf("index file: %v", err)
	}
	_, err = NewReader(bytes.NewReader([]byte("XXXX" + string(bytes.Repeat([]byte{0}, 24)))))
	if !errors.Is(err, ErrBadTag) {
		t.Fatalf("bad tag: %v", err)
	}
	_, err = NewReader(bytes.NewReader(huge))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("huge meta: %v", err)
	}
	badVer := append([]byte(nil), lead...)
	copy(badVer[0:4], "TDSm")
	binary.LittleEndian.PutUint32(badVer[4:8], TocMetaData|TocNewObjList)
	binary.LittleEndian.PutUint32(badVer[8:12], 1)
	binary.LittleEndian.PutUint64(badVer[12:20], 4)
	binary.LittleEndian.PutUint64(badVer[20:28], 4)
	badVer = append(badVer, 0, 0, 0, 0)
	_, err = NewReader(bytes.NewReader(badVer))
	if !errors.Is(err, ErrVersion) {
		t.Fatalf("version: %v", err)
	}
}

func TestOpenDoesNotReadRaw(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]float64, 100000)
	for i := range payload {
		payload[i] = float64(i)
	}
	if err := ch.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	cr := &countingReader{r: bytes.NewReader(buf.Bytes())}
	f, err := NewReader(cr)
	if err != nil {
		t.Fatal(err)
	}
	if cr.n > 64*1024 {
		t.Fatalf("metadata parse read %d bytes of a %d byte file", cr.n, buf.Len())
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	got, err := c.Float64s()
	if err != nil || len(got) != len(payload) || got[len(got)-1] != payload[len(payload)-1] {
		t.Fatalf("len %d err %v", len(got), err)
	}
	if cr.n < int64(buf.Len())/2 {
		t.Fatalf("value read did not touch raw data (%d of %d)", cr.n, buf.Len())
	}
}

type countingReader struct {
	r *bytes.Reader
	n int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.n += int64(len(p))
	return c.r.ReadAt(p, off)
}

func (c *countingReader) Size() int64 { return c.r.Size() }

func finishSegment(t *testing.T, toc uint32, meta, raw []byte) []byte {
	t.Helper()
	file := make([]byte, 28, 28+len(meta)+len(raw))
	copy(file, encodeLeadIn(toc, uint64(len(meta)+len(raw)), uint64(len(meta))))
	file = append(file, meta...)
	file = append(file, raw...)
	return file
}

func openBytes(t *testing.T, b []byte) *File {
	t.Helper()
	f, err := NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func mustValues(t *testing.T, c *Channel) any {
	t.Helper()
	v, err := c.Values()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func names(f *File) []string {
	var s []string
	for _, g := range f.Groups() {
		s = append(s, g.Name())
	}
	return s
}

func assertEqual(t *testing.T, got, want any) {
	t.Helper()
	if !deepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func deepEqual(a, b any) bool {
	if fa, ok := a.([]float64); ok {
		fb := b.([]float64)
		if len(fa) != len(fb) {
			return false
		}
		for i := range fa {
			if math.Float64bits(fa[i]) != math.Float64bits(fb[i]) {
				return false
			}
		}
		return true
	}
	if fa, ok := a.([]float32); ok {
		fb := b.([]float32)
		if len(fa) != len(fb) {
			return false
		}
		for i := range fa {
			if math.Float32bits(fa[i]) != math.Float32bits(fb[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
