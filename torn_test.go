package tdms

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// syncedFile writes three segments of g/c through a WithSync writer and a
// fourth segment that is written but, for the power-loss model, never
// synced. It returns the bytes durable after the third segment (synced),
// the fourth segment's bytes in its pre-patch state (sentinel lead-in) and
// its final state (patched lead-in), and the values of the synced segments.
func syncedFile(t *testing.T, interleaved bool) (synced, sentinel, patched []byte, want []float64) {
	t.Helper()
	f := &syncFile{}
	w, err := NewWriter(f, WithSync())
	if err != nil {
		t.Fatal(err)
	}
	w.SetInterleaved(interleaved)
	g := w.Group("g")
	c, _ := g.Channel("c", TypeFloat64)
	d, _ := g.Channel("d", TypeFloat64)
	for seg := 0; seg < 4; seg++ {
		vals := []float64{float64(10*seg + 1), float64(10*seg + 2), float64(10*seg + 3)}
		if err := c.Write(vals); err != nil {
			t.Fatal(err)
		}
		if err := d.Write(vals); err != nil {
			t.Fatal(err)
		}
		if seg < 3 {
			want = append(want, vals...)
		}
		if seg == 3 {
			// A property change makes the last segment carry metadata, so
			// cuts land inside a lead-in, metadata, and raw data.
			c.SetProperty("note", "fourth")
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	// Two syncs per segment: durable[5] is after segment 3's patch,
	// durable[6] is segment 4 before its patch, durable[7] after it.
	if len(f.durable) != 8 {
		t.Fatalf("%d syncs", len(f.durable))
	}
	return f.durable[5], f.durable[6], f.durable[7], want
}

func floatsOf(t *testing.T, f *File, name string) []float64 {
	t.Helper()
	g, ok := f.Group("g")
	if !ok {
		t.Fatal("group g missing")
	}
	c, ok := g.Channel(name)
	if !ok {
		t.Fatalf("channel %s missing", name)
	}
	v, err := c.Values()
	if err != nil {
		t.Fatal(err)
	}
	return v.([]float64)
}

// Cut the unsynced fourth segment at every byte, in both lead-in states,
// and fill it with zeros at every length. Strict Open behaves as before;
// a lenient open always opens and keeps exactly the synced segments as
// committed samples, with the dropped bytes reported.
func TestLenientTailRecoversSyncedSegmentsAtEveryCut(t *testing.T) {
	for _, interleaved := range []bool{false, true} {
		synced, sentinel, patched, want := syncedFile(t, interleaved)
		L := len(synced)
		if !bytes.Equal(sentinel[:L], synced) || !bytes.Equal(patched[:L], synced) {
			t.Fatal("later writes changed synced bytes")
		}
		check := func(what string, file []byte, droppedWant int64) {
			t.Helper()
			strictErr := error(nil)
			if sf, err := NewReader(bytes.NewReader(file)); err != nil {
				strictErr = err
			} else {
				st := sf.Status()
				if st.DroppedTail != 0 || st.TailError != nil {
					t.Fatalf("%s: strict open reports a dropped tail %#v", what, st)
				}
			}
			f, err := NewReader(bytes.NewReader(file), WithLenientTail())
			if err != nil {
				t.Fatalf("%s: lenient open: %v", what, err)
			}
			st := f.Status()
			for _, name := range []string{"c", "d"} {
				got := floatsOf(t, f, name)
				if len(got) < len(want) || !equalF(got[:len(want)], want) {
					t.Fatalf("%s: %s = %v, want prefix %v", what, name, got, want)
				}
				g, _ := f.Group("g")
				ch, _ := g.Channel(name)
				if n := ch.committed(); n != int64(len(want)) && !(st.Complete && n == int64(len(want)+3)) {
					t.Fatalf("%s: %s committed %d, want %d (%#v)", what, name, n, len(want), st)
				}
			}
			if droppedWant >= 0 {
				if st.DroppedTail != droppedWant || !st.Truncated || st.Complete || st.Incomplete || st.TailError == nil ||
					f.SegmentCount() != 3 || strictErr == nil || !errors.Is(strictErr, st.TailError) && strictErr.Error() != st.TailError.Error() {
					t.Fatalf("%s: torn status %#v segments %d strict %v", what, st, f.SegmentCount(), strictErr)
				}
				// The documented repair: truncate to size minus DroppedTail.
				if _, err := NewReader(bytes.NewReader(file[:int64(len(file))-st.DroppedTail])); err != nil {
					t.Fatalf("%s: truncated repair does not open strictly: %v", what, err)
				}
			} else if st.DroppedTail != 0 {
				t.Fatalf("%s: dropped %d, want a recovered tail %#v", what, st.DroppedTail, st)
			}
		}
		for cut := L + 1; cut < len(patched); cut++ {
			for _, state := range []struct {
				name string
				b    []byte
			}{{"sentinel", sentinel}, {"patched", patched}} {
				file := state.b[:cut]
				// Lead-in plus all of the metadata: the existing sentinel or
				// past-EOF handling keeps the segment. Otherwise it is torn.
				var dropped int64 = int64(cut - L)
				if cut-L >= leadInSize {
					lead, err := decodeLeadIn(state.b[L : L+leadInSize])
					if err != nil {
						t.Fatal(err)
					}
					if int64(cut-L-leadInSize) >= int64(lead.raw) {
						dropped = -1
					}
				}
				check(state.name+" cut", file, dropped)
			}
			zeros := append(append([]byte(nil), synced...), make([]byte, cut-L)...)
			check("zeros", zeros, int64(cut-L))
		}
		full, err := NewReader(bytes.NewReader(patched), WithLenientTail())
		if err != nil || !full.Status().Complete || full.SegmentCount() != 4 {
			t.Fatalf("whole file: %v %#v", err, full.Status())
		}
	}
}

// Each torn shape from the WithSync review: strict errors are unchanged and
// lenient keeps the two whole segments.
func TestLenientTailTornShapes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.tdms")
	w, err := Create(p, WithSync())
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := w.Group("g").Channel("c", TypeFloat64)
	_ = ch.Write([]float64{1, 2, 3})
	_ = w.Flush()
	_ = ch.Write([]float64{4, 5})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	garbage := make([]byte, 300)
	rng.Read(garbage)
	tagged := append([]byte("TDSm"), garbage...)
	badVersion := append([]byte(nil), good[:leadInSize]...)
	badVersion[8], badVersion[9] = 0x01, 0x02
	// A sentinel lead-in, as a WithSync writer publishes it before the
	// first fsync, followed by metadata that never reached disk.
	badMeta := append([]byte(nil), good[:leadInSize]...)
	binary.LittleEndian.PutUint64(badMeta[12:20], incompleteSegment)
	badMeta = append(badMeta, bytes.Repeat([]byte{0xFF}, int(len(good)))...)
	cases := []struct {
		name string
		tail []byte
		want error
	}{
		{"zeros28", make([]byte, 28), ErrBadTag},
		{"zeros4k", make([]byte, 4096), ErrBadTag},
		{"partial lead-in", good[:10], ErrTruncated},
		{"sentinel lead-in, metadata cut", append(append([]byte(nil), good[:28]...), good[28:40]...), ErrTruncated},
		{"garbage", garbage, ErrBadTag},
		{"TDSm then garbage", tagged, nil},
		{"bad version", badVersion, ErrVersion},
		{"metadata garbage", badMeta, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := append(append([]byte(nil), good...), tc.tail...)
			_, strict := NewReader(bytes.NewReader(file))
			if strict == nil || (tc.want != nil && !errors.Is(strict, tc.want)) {
				t.Fatalf("strict open: %v, want %v", strict, tc.want)
			}
			f, err := NewReader(bytes.NewReader(file), WithLenientTail())
			if err != nil {
				t.Fatalf("lenient open: %v", err)
			}
			st := f.Status()
			if !st.Truncated || st.DroppedTail != int64(len(tc.tail)) || st.TailError == nil || st.TailError.Error() != strict.Error() || f.SegmentCount() != 2 {
				t.Fatalf("status %#v segments %d strict %v", st, f.SegmentCount(), strict)
			}
			if got := floatsOf(t, f, "c"); !equalF(got, []float64{1, 2, 3, 4, 5}) {
				t.Fatalf("values %v", got)
			}
			if st.String() != "truncated" {
				t.Fatal(st.String())
			}
		})
	}
	// No whole segment before the torn bytes: nothing to keep, strict error.
	for _, file := range [][]byte{good[:10], good[:40], make([]byte, 64)} {
		_, strict := NewReader(bytes.NewReader(file))
		if _, err := NewReader(bytes.NewReader(file), WithLenientTail()); err == nil || err.Error() != strict.Error() {
			t.Fatalf("no whole segment: lenient %v strict %v", err, strict)
		}
	}
}

// An I/O error is returned, never dropped as a torn tail.
type failAfter struct {
	r   *bytes.Reader
	at  int64
	err error
}

func (f failAfter) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.at {
		return 0, f.err
	}
	return f.r.ReadAt(p, off)
}

func (f failAfter) Size() int64 { return f.r.Size() }

func TestLenientTailKeepsIOErrors(t *testing.T) {
	synced, _, patched, _ := syncedFile(t, false)
	boom := errors.New("disk on fire")
	// Fail inside the last segment's metadata, which Open reads.
	r := failAfter{r: bytes.NewReader(patched), at: int64(len(synced) + leadInSize + 4), err: boom}
	if _, err := NewReader(r, WithLenientTail()); !errors.Is(err, boom) {
		t.Fatalf("lenient open with an I/O error: %v", err)
	}
}

// A torn tail is never cached in a sidecar, and BuildSegmentIndex says why.
func TestTornTailSegmentIndex(t *testing.T) {
	synced, _, patched, want := syncedFile(t, false)
	dir := t.TempDir()
	p := filepath.Join(dir, "torn.tdms")
	if err := os.WriteFile(p, patched[:len(synced)+20], 0o644); err != nil {
		t.Fatal(err)
	}
	side := SegmentIndexPath(p)
	f, err := Open(p, WithLenientTail(), WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	if got := floatsOf(t, f, "c"); !equalF(got, want) {
		t.Fatalf("values %v", got)
	}
	f.Close()
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatalf("sidecar written for a torn file: %v", err)
	}
	if err := BuildSegmentIndex(p, WithLenientTail()); !errors.Is(err, ErrTruncated) {
		t.Fatalf("BuildSegmentIndex on a torn file: %v", err)
	}
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatal("BuildSegmentIndex wrote a sidecar")
	}
	// After the repair the sidecar is written and hit.
	if err := os.Truncate(p, int64(len(synced))); err != nil {
		t.Fatal(err)
	}
	if err := BuildSegmentIndex(p); err != nil {
		t.Fatal(err)
	}
	g, err := Open(p, WithSegmentIndex(side))
	if err != nil || !g.SegmentIndexUsed() {
		t.Fatalf("repaired file: %v used=%v", err, g != nil && g.SegmentIndexUsed())
	}
	g.Close()
}

// Follow on a file whose last segment is still being written: a partial
// lead-in means "wait", the whole segments before it are delivered, and the
// rest arrives when the segment is whole. Zeros after the last whole
// segment stop Follow with the strict error, as before.
func TestFollowTornTail(t *testing.T) {
	synced, _, patched, want := syncedFile(t, false)
	dir := t.TempDir()
	p := filepath.Join(dir, "live.tdms")
	if err := os.WriteFile(p, patched[:len(synced)+10], 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tl, err := Follow(ctx, p, "g", "c", WithTailInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer tl.Close()
	var got []float64
	for len(got) < len(want) && tl.Next() {
		got = append(got, tl.Values().([]float64)...)
	}
	if !equalF(got, want) {
		t.Fatalf("before the segment finished: %v (err %v)", got, tl.Err())
	}
	if err := os.WriteFile(p, patched, 0o644); err != nil {
		t.Fatal(err)
	}
	for len(got) < len(want)+3 && tl.Next() {
		got = append(got, tl.Values().([]float64)...)
	}
	if len(got) != len(want)+3 {
		t.Fatalf("after the segment finished: %v (err %v)", got, tl.Err())
	}

	z := filepath.Join(dir, "zeros.tdms")
	if err := os.WriteFile(z, append(append([]byte(nil), synced...), make([]byte, 64)...), 0o644); err != nil {
		t.Fatal(err)
	}
	tz, err := Follow(ctx, z, "g", "c", WithTailInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer tz.Close()
	for tz.Next() {
	}
	if !errors.Is(tz.Err(), ErrBadTag) {
		t.Fatalf("Follow over zeros: %v", tz.Err())
	}
}

// A segment whose finite next-segment offset is fully inside the file was
// published after its bytes were written, so a failure inside it is not a
// torn write. A lenient open returns the strict error instead of dropping
// it and every later byte, and so does a well-formed segment that uses a
// data type this package does not read (ErrUnsupported), wherever it is.
func TestLenientTailKeepsDeclaredSegmentErrors(t *testing.T) {
	seg := func(name string, dt DataType, raw []byte) []byte {
		var meta le
		meta.u32(1)
		meta.str(name)
		meta.full(dt, 1)
		meta.u32(0)
		return finishSegment(t, TocMetaData|TocNewObjList|TocRawData, meta.b, raw)
	}
	good := seg("/'g'/'c'", TypeInt32, []byte{1, 0, 0, 0})
	bad := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(bad[leadInSize:], 0xFFFFFF) // object count past the metadata
	fixed := seg("/'g'/'x'", TypeFixedPoint, []byte{1, 0, 0, 0})
	sentinelFixed := append([]byte(nil), fixed...)
	binary.LittleEndian.PutUint64(sentinelFixed[12:20], incompleteSegment)
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for _, tc := range []struct {
		name string
		file []byte
		want error
	}{
		{"corrupt metadata mid-file", cat(good, bad, good), ErrCorrupt},
		{"corrupt metadata in the last declared segment", cat(good, good, bad), ErrCorrupt},
		{"unsupported type mid-file", cat(good, fixed, good), ErrUnsupported},
		{"unsupported type last", cat(good, fixed), ErrUnsupported},
		{"unsupported type in a sentinel segment", cat(good, sentinelFixed), ErrUnsupported},
	} {
		_, strict := NewReader(bytes.NewReader(tc.file))
		if !errors.Is(strict, tc.want) {
			t.Fatalf("%s: strict %v, want %v", tc.name, strict, tc.want)
		}
		_, lerr := NewReader(bytes.NewReader(tc.file), WithLenientTail())
		if lerr == nil || lerr.Error() != strict.Error() {
			t.Fatalf("%s: lenient %v, want the strict error %v", tc.name, lerr, strict)
		}
	}
	// The same corrupt metadata behind a sentinel lead-in is what a power
	// loss before the first fsync leaves: dropped.
	torn := append([]byte(nil), bad...)
	binary.LittleEndian.PutUint64(torn[12:20], incompleteSegment)
	f, err := NewReader(bytes.NewReader(cat(good, torn)), WithLenientTail())
	if err != nil {
		t.Fatal(err)
	}
	if st := f.Status(); st.DroppedTail != int64(len(torn)) || !errors.Is(st.TailError, ErrCorrupt) || f.SegmentCount() != 1 {
		t.Fatalf("sentinel corrupt tail: %#v segments %d", st, f.SegmentCount())
	}
}
