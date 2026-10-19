package tdms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// Hand-built segments. encodeObjects is not used, so a bug shared with the
// writer cannot satisfy these tests. Paths are the NI quoted form.

type le struct{ b []byte }

func (p *le) u32(v uint32) {
	var a [4]byte
	binary.LittleEndian.PutUint32(a[:], v)
	p.b = append(p.b, a[:]...)
}

func (p *le) u64(v uint64) {
	var a [8]byte
	binary.LittleEndian.PutUint64(a[:], v)
	p.b = append(p.b, a[:]...)
}

func (p *le) str(s string) {
	p.u32(uint32(len(s)))
	p.b = append(p.b, s...)
}

func (p *le) full(dt DataType, n uint64) {
	p.u32(20)
	p.u32(uint32(dt))
	p.u32(1)
	p.u64(n)
}

func (p *le) none()  { p.u32(RawIndexNoData) }
func (p *le) match() { p.u32(RawIndexMatchesPrevious) }
func (p *le) i32s(vs ...int32) {
	for _, v := range vs {
		p.u32(uint32(v))
	}
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// A segment that publishes a channel index and no raw bytes, then a raw-only
// segment. The index has to survive the metadata-only segment; otherwise the
// raw segment is "raw bytes but no channels".
func TestMetadataOnlyIndexThenRaw(t *testing.T) {
	var meta le
	meta.u32(1)
	meta.str("/'g'/'c'")
	meta.full(TypeInt32, 3)
	meta.u32(0)
	var raw le
	raw.i32s(1, 2, 3)
	file := cat(
		finishSegment(t, TocMetaData|TocNewObjList, meta.b, nil),
		finishSegment(t, TocRawData, nil, raw.b),
	)
	f := openBytes(t, file)
	if f.SegmentCount() != 2 {
		t.Fatalf("segments %d", f.SegmentCount())
	}
	g, ok := f.Group("g")
	if !ok {
		t.Fatal("group")
	}
	c, ok := g.Channel("c")
	if !ok {
		t.Fatal("channel")
	}
	assertEqual(t, mustValues(t, c), []int32{1, 2, 3})
}

// Metadata-only kTocNewObjList reverses the channel order. The following
// raw-only segment is in that new order. Keeping the old order would read
// channel a as [1, 300].
func TestMetadataOnlyReorder(t *testing.T) {
	var first le
	first.u32(2)
	first.str("/'g'/'a'")
	first.full(TypeInt32, 1)
	first.u32(0)
	first.str("/'g'/'b'")
	first.full(TypeInt32, 1)
	first.u32(0)
	var raw1 le
	raw1.i32s(1, 2)

	var mid le
	mid.u32(2)
	mid.str("/'g'/'b'")
	mid.match()
	mid.u32(0)
	mid.str("/'g'/'a'")
	mid.match()
	mid.u32(0)

	var raw2 le
	raw2.i32s(300, 100)

	file := cat(
		finishSegment(t, TocMetaData|TocNewObjList|TocRawData, first.b, raw1.b),
		finishSegment(t, TocMetaData|TocNewObjList, mid.b, nil),
		finishSegment(t, TocRawData, nil, raw2.b),
	)
	f := openBytes(t, file)
	g, _ := f.Group("g")
	a, _ := g.Channel("a")
	b, _ := g.Channel("b")
	assertEqual(t, mustValues(t, a), []int32{1, 100})
	assertEqual(t, mustValues(t, b), []int32{2, 300})
}

// Dropping a channel without kTocNewObjList and without a 0xFFFFFFFF index,
// then supplying a raw section sized for the remaining channel, is not a
// legal chunk of the previous order. The file is rejected.
func TestDropoutWithoutSignalIsCorrupt(t *testing.T) {
	var first le
	first.u32(2)
	first.str("/'g'/'a'")
	first.full(TypeInt32, 1)
	first.u32(0)
	first.str("/'g'/'b'")
	first.full(TypeInt32, 1)
	first.u32(0)
	var raw1 le
	raw1.i32s(1, 2)
	var raw2 le
	raw2.i32s(100)
	file := cat(
		finishSegment(t, TocMetaData|TocNewObjList|TocRawData, first.b, raw1.b),
		finishSegment(t, TocRawData, nil, raw2.b),
	)
	_, err := NewReader(bytes.NewReader(file))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
}

// 0xFFFFFFFF removes the channel from the raw order and keeps the index, so
// a later matches-previous can resume it. The raw bytes that follow belong
// to the channels that remain.
func TestIdxNoneDropsAndIdxMatchResumes(t *testing.T) {
	var first le
	first.u32(2)
	first.str("/'g'/'a'")
	first.full(TypeInt32, 1)
	first.u32(0)
	first.str("/'g'/'b'")
	first.full(TypeInt32, 1)
	first.u32(0)
	var raw1 le
	raw1.i32s(1, 2)

	var drop le
	drop.u32(2)
	drop.str("/'g'/'a'")
	drop.none()
	drop.u32(0)
	drop.str("/'g'/'b'")
	drop.match()
	drop.u32(0)
	var rawDrop le
	rawDrop.i32s(100)

	var back le
	back.u32(1)
	back.str("/'g'/'a'")
	back.match()
	back.u32(0)
	var rawBack le
	rawBack.i32s(7, 8, 9)

	file := cat(
		finishSegment(t, TocMetaData|TocNewObjList|TocRawData, first.b, raw1.b),
		finishSegment(t, TocMetaData|TocRawData, drop.b, rawDrop.b),
		finishSegment(t, TocMetaData|TocNewObjList|TocRawData, back.b, rawBack.b),
	)
	f := openBytes(t, file)
	g, _ := f.Group("g")
	a, _ := g.Channel("a")
	b, _ := g.Channel("b")
	assertEqual(t, mustValues(t, a), []int32{1, 7, 8, 9})
	assertEqual(t, mustValues(t, b), []int32{2, 100})
}

func TestTrailingNULIgnoredOnRead(t *testing.T) {
	var meta le
	meta.u32(2)
	meta.str("/'g'")
	meta.none()
	meta.u32(1)
	meta.str("note")
	meta.u32(uint32(TypeString))
	meta.str("ab\x00")
	meta.str("/'g'/'s'")
	meta.u32(28)
	meta.u32(uint32(TypeString))
	meta.u32(1)
	meta.u64(3)
	strings := []string{"ab\x00", "a\x00b", "\x00"}
	var payload int
	for _, s := range strings {
		payload += len(s)
	}
	total := uint64(4*len(strings) + payload)
	meta.u64(total)
	meta.u32(0)

	var raw le
	off := uint32(0)
	offs := make([]byte, 4*len(strings))
	for i, s := range strings {
		off += uint32(len(s))
		binary.LittleEndian.PutUint32(offs[i*4:], off)
	}
	raw.b = append(raw.b, offs...)
	for _, s := range strings {
		raw.b = append(raw.b, s...)
	}
	f := openBytes(t, finishSegment(t, TocMetaData|TocNewObjList|TocRawData, meta.b, raw.b))
	g, ok := f.Group("g")
	if !ok {
		t.Fatal("group")
	}
	p, ok := g.Property("note")
	if !ok || p.Value != "ab" {
		t.Fatalf("property %+v ok=%v", p, ok)
	}
	ch, _ := g.Channel("s")
	assertEqual(t, mustValues(t, ch), []string{"ab", "a\x00b", ""})

	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetProperty("p", "ab\x00"); err != nil {
		t.Fatal(err)
	}
	cw, err := w.Group("g").Channel("s", TypeString)
	if err != nil {
		t.Fatal(err)
	}
	if err := cw.Write([]string{"ab\x00", "a\x00b", "\x00"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rf := openBytes(t, buf.Bytes())
	rp, ok := rf.Property("p")
	if !ok || rp.Value != "ab" {
		t.Fatalf("writer property %+v", rp)
	}
	rg, _ := rf.Group("g")
	rc, _ := rg.Channel("s")
	assertEqual(t, mustValues(t, rc), []string{"ab", "a\x00b", ""})
}
