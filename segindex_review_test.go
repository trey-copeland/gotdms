package tdms

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func writePropFile(t *testing.T, path, unit string) {
	t.Helper()
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(PropUnitString, unit); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func unitOf(t *testing.T, f *File) string {
	t.Helper()
	g, ok := f.Group("g")
	if !ok {
		t.Fatal("group")
	}
	c, ok := g.Channel("c")
	if !ok {
		t.Fatal("channel")
	}
	for _, p := range c.Properties() {
		if p.Name == PropUnitString {
			return p.Value.(string)
		}
	}
	return ""
}

// A file replaced at its path while an open handle is being parsed must not
// produce a sidecar keyed to the new file's identity but describing the old
// bytes. Trust-identity would then serve the old object tree.
func TestSegmentIndexReplacedDuringParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writePropFile(t, path, "AAAA")
	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	// Replace path with same-size, different metadata (atomic save pattern).
	other := filepath.Join(dir, "new.tdms")
	writePropFile(t, other, "BBBB")
	if runtime.GOOS == "windows" {
		t.Skip("rename over an open file is refused on Windows")
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	side := SegmentIndexPath(path)
	f, err := NewReader(rf, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	if got := unitOf(t, f); got != "AAAA" {
		t.Fatalf("handle unit %q", got)
	}
	f.Close()
	g, err := Open(path, WithSegmentIndex(side), WithSegmentIndexTrustIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if got := unitOf(t, g); got != "BBBB" {
		t.Fatalf("trust-identity served the replaced file's metadata: unit %q (sidecar used %v)", got, g.SegmentIndexUsed())
	}
}

func TestSegmentIndexSidecarMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writePropFile(t, path, "AAAA")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(SegmentIndexPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0644 {
		t.Fatalf("sidecar mode %v, data file 0644", st.Mode().Perm())
	}
}

func rawSidecar(flags uint32, payload []byte) []byte {
	b := make([]byte, segmentIndexHeader+len(payload))
	copy(b, segmentIndexMagic)
	binary.LittleEndian.PutUint32(b[4:8], segmentIndexVersion)
	binary.LittleEndian.PutUint64(b[8:16], 1000)
	binary.LittleEndian.PutUint32(b[48:52], Version20)
	binary.LittleEndian.PutUint32(b[52:56], 1)
	binary.LittleEndian.PutUint32(b[56:60], flags)
	copy(b[segmentIndexHeader:], payload)
	binary.LittleEndian.PutUint32(b[44:48], crc32.ChecksumIEEE(payload))
	return b
}

// A header flag bit this version does not define is a decode error, so a
// later writer's flag cannot be silently ignored.
func TestSegmentIndexUnknownHeaderFlag(t *testing.T) {
	payload := make([]byte, 8) // zero file properties, zero groups
	if _, err := decodeSegmentIndex(rawSidecar(0, payload)); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if _, err := decodeSegmentIndex(rawSidecar(1<<3, payload)); err == nil {
		t.Fatal("unknown header flag bit 3 accepted")
	}
}

// A property count is bounded by the bytes left, but each Property is 40
// bytes in memory. Decoding must not allocate ~10x the sidecar up front.
func TestSegmentIndexPropCountAllocation(t *testing.T) {
	const n = 1 << 20
	payload := make([]byte, 4+4*n)
	binary.LittleEndian.PutUint32(payload, n)
	blob := rawSidecar(0, payload)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := decodeSegmentIndex(blob); err == nil {
		t.Fatal("decoded garbage properties")
	}
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > uint64(2*len(blob)) {
		t.Fatalf("decode allocated %d bytes for a %d-byte sidecar", got, len(blob))
	}
}

// writeSegmentIndex must not key a sidecar to a stat taken after the parse
// when the file changed in between (an in-place edit during the parse).
func TestSegmentIndexWriteRejectsChangeDuringParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writePropFile(t, path, "AAAA")
	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	preFI, err := rf.Stat()
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, err := fileID(rf)
	if err != nil {
		t.Fatal(err)
	}
	pre := dataStat{fi: preFI, dev: dev, ino: ino}
	f, err := NewReader(rf)
	if err != nil {
		t.Fatal(err)
	}
	later := pre.fi.ModTime().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	side := SegmentIndexPath(path)
	if err := f.writeSegmentIndex(side, pre); err == nil {
		t.Fatal("sidecar written although the file changed during the parse")
	}
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists: %v", err)
	}
}

// The remove-and-retry fallback must not delete a directory that sits at
// the sidecar path.
func TestSegmentIndexReplaceKeepsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	writePropFile(t, path, "AAAA")
	side := SegmentIndexPath(path)
	if err := os.Mkdir(side, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := BuildSegmentIndex(path); err == nil {
		t.Fatal("expected a write error")
	}
	st, err := os.Stat(side)
	if err != nil || !st.IsDir() {
		t.Fatalf("empty directory at sidecar path was removed: %v", err)
	}
}

// FuzzSegmentIndexTrusted feeds crafted payloads behind a header whose size,
// modification time, and identity match the data file, then opens with
// WithSegmentIndexTrustIdentity and reads every channel. Decode bounds
// checks are the only guard on that path; nothing may panic.
func FuzzSegmentIndexTrusted(f *testing.F) {
	dir := f.TempDir()
	path := filepath.Join(dir, "f.tdms")
	w, err := Create(path)
	if err != nil {
		f.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeInt32)
	if err != nil {
		f.Fatal(err)
	}
	if err := ch.Write([]int32{7, 8, 9}); err != nil {
		f.Fatal(err)
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}
	side := SegmentIndexPath(path)
	if err := BuildSegmentIndex(path); err != nil {
		f.Fatal(err)
	}
	blob, err := os.ReadFile(side)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(blob[segmentIndexHeader:], uint32(0))
	f.Add([]byte{}, uint32(indexFlagTruncated|indexFlagPartial))
	fi, err := os.Stat(path)
	if err != nil {
		f.Fatal(err)
	}
	rf, err := os.Open(path)
	if err != nil {
		f.Fatal(err)
	}
	dev, ino, err := fileID(rf)
	rf.Close()
	if err != nil {
		f.Fatal(err)
	}
	if len(blob) >= segmentIndexHeader+spanRecordV1+8 {
		huge := append([]byte(nil), blob[segmentIndexHeader:]...)
		// A single segment omits the run words. Set the run flag and insert
		// step and a huge count just before the channel total. A huge count
		// must not become a trust hit.
		spanAt := len(huge) - 8 - spanRecordV1
		flags := binary.LittleEndian.Uint32(huge[spanAt+52:]) | spanFlagRun
		binary.LittleEndian.PutUint32(huge[spanAt+52:], flags)
		tail := append([]byte(nil), huge[len(huge)-8:]...)
		run := make([]byte, 16)
		binary.LittleEndian.PutUint64(run[0:8], 1)
		binary.LittleEndian.PutUint64(run[8:16], uint64(1)<<62)
		huge = append(huge[:len(huge)-8], run...)
		huge = append(huge, tail...)
		f.Add(huge, uint32(0))
	}
	f.Fuzz(func(t *testing.T, payload []byte, flags uint32) {
		if len(payload) > 1<<16 {
			payload = payload[:1<<16]
		}
		b := make([]byte, segmentIndexHeader+len(payload))
		copy(b, blob[:segmentIndexHeader])
		binary.LittleEndian.PutUint64(b[8:16], uint64(fi.Size()))
		binary.LittleEndian.PutUint64(b[16:24], uint64(fi.ModTime().UnixNano()))
		binary.LittleEndian.PutUint64(b[24:32], dev)
		binary.LittleEndian.PutUint64(b[32:40], ino)
		binary.LittleEndian.PutUint32(b[56:60], flags&7)
		copy(b[segmentIndexHeader:], payload)
		binary.LittleEndian.PutUint32(b[44:48], crc32.ChecksumIEEE(payload))
		crafted := filepath.Join(t.TempDir(), "crafted.gtdx")
		if err := os.WriteFile(crafted, b, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Open(path, WithSegmentIndex(crafted), WithSegmentIndexTrustIdentity(), WithLenientTail())
		if err != nil {
			t.Fatalf("sidecar must not fail the open: %v", err)
		}
		defer got.Close()
		if got.SegmentIndexUsed() {
			for _, g := range got.Groups() {
				for _, c := range g.Channels() {
					limit := fi.Size()
					if c.DataType() == TypeString {
						limit = fi.Size() / 4
					}
					if int64(c.Len()) > limit {
						t.Fatalf("trust hit %s len %d exceeds file bound %d", c.Path(), c.Len(), limit)
					}
				}
			}
		}
		for _, g := range got.Groups() {
			for _, c := range g.Channels() {
				_, _ = c.Values()
				_, _ = c.Float64s()
				if c.Len() > 0 && c.Len() < 1<<20 {
					_, _ = c.Slice(c.Len()-1, c.Len())
				}
			}
		}
	})
}
