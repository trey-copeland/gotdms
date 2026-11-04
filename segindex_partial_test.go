package tdms

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// tailSpan is a float64 partial tail in the final, unfinished segment: no
// whole chunk of 4 samples (32 bytes), 3 samples at dataPos 100.
func tailSpan() span {
	return span{
		dataPos: 100, chunkSize: 32, nChunks: 0, nPerChunk: 4, col: 0, stride: 8, typeSize: 8,
		partialN: 3, partialPos: 100, partialCol: 0, partialStride: 8, live: true,
	}
}

func TestSpanInFilePartialTailBounds(t *testing.T) {
	ok := func(what string, sp span, size int64) {
		t.Helper()
		if _, err := spanInFile(sp, nil, size); err != nil {
			t.Fatalf("%s: rejected a valid span: %v", what, err)
		}
	}
	bad := func(what string, sp span, size int64) {
		t.Helper()
		if _, err := spanInFile(sp, nil, size); err == nil {
			t.Fatalf("%s: accepted a crafted span", what)
		}
	}
	sp := tailSpan()
	// 3 samples end at byte 124.
	ok("tail ends at EOF", sp, 124)
	bad("tail ends one byte past EOF", sp, 123)

	// The tail is shorter than one chunk and starts after the whole chunks.
	sp = tailSpan()
	sp.partialPos = 107 // ends at 131: 31 bytes past the tail start
	ok("tail ends one byte before a whole chunk", sp, 1<<20)
	sp.partialPos = 108 // ends at 132: a whole chunk
	bad("tail reaches a whole chunk", sp, 1<<20)
	sp = tailSpan()
	sp.partialPos = 99
	bad("tail starts before the segment's data", sp, 1<<20)
	sp = tailSpan()
	sp.nChunks, sp.partialPos = 2, 164 // two whole chunks, tail at 164
	ok("tail after two whole chunks", sp, 188)
	sp.partialPos = 163
	bad("tail overlaps the last whole chunk", sp, 1<<20)

	// A crafted stride stays inside the file but walks past the tail into
	// the next segment: 0 + 2*16 + 8 = 40 bytes. With the chunk stride
	// changed to match, only the tail bound catches it.
	sp = tailSpan()
	sp.partialStride = 16
	bad("partial stride past the tail", sp, 1<<20)
	sp.stride = 16
	bad("partial and chunk stride past the tail", sp, 1<<20)
	// A tail stride that differs from the chunk stride, even inside the
	// tail (0 + 2*9 + 8 = 26 bytes), is not a layout a parse produces.
	sp = tailSpan()
	sp.partialStride = 9
	bad("partial stride differs from the chunk stride", sp, 1<<20)
	sp = tailSpan()
	sp.partialCol = 9 // 9 + 2*8 + 8 = 33 bytes
	bad("partial column past the tail", sp, 1<<20)

	// A zero stride repeats one sample; reject it like the chunk stride,
	// even when the chunk stride is crafted to match.
	sp = tailSpan()
	sp.partialN, sp.partialStride = 2, 0
	bad("zero partial stride with two samples", sp, 1<<20)
	sp.stride, sp.nPerChunk, sp.nChunks = 0, 1, 0
	bad("zero partial and chunk stride with two samples", sp, 1<<20)

	// The tail holds at most one chunk's worth of samples.
	sp = tailSpan()
	sp.nPerChunk = 2
	bad("more tail samples than a chunk holds", sp, 1<<20)
	sp.nPerChunk = 3
	ok("tail of exactly one chunk's count", sp, 1<<20)

	// Only an unfinished final segment has a partial tail.
	sp = tailSpan()
	sp.live = false
	bad("partial tail in a finished segment", sp, 1<<20)
	sp = tailSpan()
	sp.chunkSize = 0
	bad("partial tail with no chunk", sp, 1<<20)

	// String tail: the whole block must fit after the whole chunks and
	// before EOF.
	str := span{dataPos: 100, chunkSize: 20, nPerChunk: 2, strSize: 20, str: true,
		partialN: 2, partialPos: 100, live: true}
	ok("string tail ends at EOF", span{dataPos: 100, chunkSize: 21, nPerChunk: 2, strSize: 20, str: true, partialN: 2, partialPos: 100, live: true}, 120)
	bad("string tail is a whole chunk", str, 1<<20)
	short := span{dataPos: 100, chunkSize: 21, nPerChunk: 2, strSize: 20, str: true, partialN: 1, partialPos: 100, live: true}
	bad("string tail with part of a block", short, 1<<20)
	str.chunkSize = 21
	bad("string tail one byte past EOF", str, 119)
	str.partialPos = 99
	bad("string tail before the data", str, 1<<20)
}

// A sidecar whose partial tail is moved or re-strided is a cache miss, and
// the file still reads its real values.
func TestSegmentIndexCraftedPartialTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tail.tdms")
	writeFloatSegment(t, path, []float64{1, 2, 3, 4}, true)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint64(body[12:], incompleteSegment)
	body = body[:len(body)-3]
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	side := SegmentIndexPath(path)
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	blob, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	spanAt := len(blob) - 8 - spanRecordV1
	// Field offsets in a span record (see SegmentIndexPath).
	const offPartialN, offPartialPos, offPartialStride = 64, 72, 88
	if n := binary.LittleEndian.Uint64(blob[spanAt+offPartialN:]); n != 3 {
		t.Fatalf("partialN = %d; span is not where this test assumed", n)
	}
	if st := binary.LittleEndian.Uint64(blob[spanAt+offPartialStride:]); st != 8 {
		t.Fatalf("partialStride = %d", st)
	}
	f, err = Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	if !f.SegmentIndexUsed() {
		t.Fatal("untouched sidecar missed")
	}
	f.Close()
	for _, edit := range []struct {
		what string
		fn   func(b []byte)
	}{
		{"partial stride 9", func(b []byte) { binary.LittleEndian.PutUint64(b[spanAt+offPartialStride:], 9) }},
		{"partial stride 0", func(b []byte) { binary.LittleEndian.PutUint64(b[spanAt+offPartialStride:], 0) }},
		{"partial position back 1", func(b []byte) {
			pos := binary.LittleEndian.Uint64(b[spanAt+offPartialPos:])
			binary.LittleEndian.PutUint64(b[spanAt+offPartialPos:], pos-1)
		}},
	} {
		b := append([]byte(nil), blob...)
		edit.fn(b)
		binary.LittleEndian.PutUint32(b[44:48], crc32.ChecksumIEEE(b[segmentIndexHeader:]))
		if _, err := decodeSegmentIndex(b); err == nil {
			t.Fatalf("%s: crafted sidecar decoded", edit.what)
		}
		if err := os.WriteFile(side, b, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := Open(path, WithSegmentIndex(side))
		if err != nil {
			t.Fatalf("%s: open: %v", edit.what, err)
		}
		if f.SegmentIndexUsed() {
			t.Fatalf("%s: crafted sidecar was used", edit.what)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		assertEqual(t, mustValues(t, c), []float64{1, 2, 3})
		f.Close()
	}
}

// A lead-in whose metadata length runs past its own segment, while still
// inside the file, fails the fingerprint walk without a file-sized buffer.
func TestSegmentFingerprintMetadataPastSegment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "two.tdms")
	writeFloatSegment(t, path, []float64{1, 2}, true)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Second segment: a large raw-only tail so the file is big.
	big := make([]byte, 4<<20)
	file := append(append([]byte(nil), first...), big...)
	if _, err := segmentFingerprint(bytesReaderAt(first), int64(len(first)), false); err != nil {
		t.Fatalf("valid first segment: %v", err)
	}
	next := binary.LittleEndian.Uint64(first[12:])
	// Metadata exactly filling the segment is valid (no raw bytes) as far as
	// the bound goes; one byte more runs into the next segment.
	binary.LittleEndian.PutUint64(file[20:], next+1)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = segmentFingerprint(bytesReaderAt(file), int64(len(file)), true)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("metadata past the segment end was accepted")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("fingerprint allocated %d bytes for a %d-byte segment", grew, next)
	}
	// Metadata length near EOF: within the file, far past the segment.
	binary.LittleEndian.PutUint64(file[20:], uint64(len(file))-leadInSize-1)
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = segmentFingerprint(bytesReaderAt(file), int64(len(file)), true)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("metadata length near EOF was accepted")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("fingerprint allocated %d bytes for a metadata length near EOF", grew)
	}
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(b)) {
		return 0, os.ErrInvalid
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, os.ErrInvalid
	}
	return n, nil
}

func TestReadFileLimited(t *testing.T) {
	dir := t.TempDir()
	at := filepath.Join(dir, "at")
	over := filepath.Join(dir, "over")
	if err := os.WriteFile(at, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("0123456789A"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := readFileLimited(at, 10)
	if err != nil || string(b) != "0123456789" {
		t.Fatalf("file at the limit: %q %v", b, err)
	}
	b, err = readFileLimited(over, 10)
	if err != nil || b != nil {
		t.Fatalf("file one byte over the limit: %q %v", b, err)
	}
	if _, err := readFileLimited(filepath.Join(dir, "missing"), 10); err == nil {
		t.Fatal("missing file: want an error")
	}
}
