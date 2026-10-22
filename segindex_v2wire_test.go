package tdms

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"testing"
)

// v2WireFixture returns a valid version 2 sidecar for a one-segment file.
// The last span record sits immediately before the final channel total.
func v2WireFixture(t *testing.T, segs int) (blob []byte, size int64) {
	t.Helper()
	path := writeSegments(t, segs, 0, false, false)
	if err := BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(SegmentIndexPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(b[4:8]) != segmentIndexVersion {
		t.Fatalf("fixture is version %d", binary.LittleEndian.Uint32(b[4:8]))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeV2Checked(b, fi.Size()); err != nil {
		t.Fatalf("fixture does not decode: %v", err)
	}
	return b, fi.Size()
}

func decodeV2Checked(b []byte, size int64) (*indexImage, error) {
	img, err := decodeSegmentIndex(b)
	if err != nil {
		return nil, err
	}
	img.size = size
	if err := spansInFile(img); err != nil {
		return nil, err
	}
	return img, nil
}

func reseal(b []byte) []byte {
	binary.LittleEndian.PutUint32(b[44:48], crc32.ChecksumIEEE(b[segmentIndexHeader:]))
	return b
}

// withRunWords sets the run flag on the last span and inserts step and count
// before the trailing channel total.
func withRunWords(b []byte, flag bool, words ...int64) []byte {
	out := append([]byte(nil), b...)
	spanAt := len(out) - 8 - spanRecordV1
	if flag {
		fl := binary.LittleEndian.Uint32(out[spanAt+52:]) | spanFlagRun
		binary.LittleEndian.PutUint32(out[spanAt+52:], fl)
	}
	tail := append([]byte(nil), out[len(out)-8:]...)
	out = out[:len(out)-8]
	for _, w := range words {
		out = binary.LittleEndian.AppendUint64(out, uint64(w))
	}
	return reseal(append(out, tail...))
}

func TestSegmentIndexV2WireRejects(t *testing.T) {
	blob, size := v2WireFixture(t, 1)
	cases := map[string][]byte{
		// Flag set but the 16 bytes are missing: the reader runs into the
		// channel total and then off the end of the payload.
		"flag without words": withRunWords(blob, true),
		"count one":          withRunWords(blob, true, 4, 1),
		"count zero":         withRunWords(blob, true, 4, 0),
		"negative count":     withRunWords(blob, true, 4, -2),
		"zero step":          withRunWords(blob, true, 0, 2),
		"negative step":      withRunWords(blob, true, -8, 2),
		"huge count":         withRunWords(blob, true, 1, 1<<62),
		"step overflows":     withRunWords(blob, true, 1<<62, 3),
		// bec9c06/2e33c73 wrote every v2 span as 112 bytes with no flag.
		// The same version number must not misread those trailing words.
		"pre-flag 112-byte layout": withRunWords(blob, false, 0, 1),
		// Words without the flag are the same desync seen from the other side.
		"words without flag": withRunWords(blob, false, 4, 2),
	}
	v1 := withRunWords(blob, true, 4, 2)
	binary.LittleEndian.PutUint32(v1[4:8], segmentIndexV1)
	cases["run flag in version 1"] = reseal(v1)
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if img, err := decodeV2Checked(b, size); err == nil {
				t.Fatalf("decoded %+v", img)
			}
		})
	}
}

func TestSegmentIndexV2WireTruncated(t *testing.T) {
	blob, size := v2WireFixture(t, 20)
	for n := segmentIndexHeader; n < len(blob); n++ {
		b := reseal(append([]byte(nil), blob[:n]...))
		if _, err := decodeV2Checked(b, size); err == nil {
			t.Fatalf("truncated to %d of %d decoded", n, len(blob))
		}
	}
}

func TestSegmentIndexV2WireRunOutsideFile(t *testing.T) {
	blob, size := v2WireFixture(t, 20)
	img, err := decodeV2Checked(blob, size)
	if err != nil {
		t.Fatal(err)
	}
	ch := img.groups[0].channels[0]
	if len(ch.spans) != 1 || ch.spans[0].repeats(ch.runs) != 20 {
		t.Fatalf("fixture spans %d run %d", len(ch.spans), ch.spans[0].repeats(ch.runs))
	}
	// One more repeat than the file holds must fail the extent check even
	// though the decode itself succeeds.
	if _, err := decodeV2Checked(blob, size-1); err == nil {
		t.Fatal("run past EOF was accepted")
	}
}

func TestCoalesceSpansKeepsExistingRuns(t *testing.T) {
	base := span{dataPos: 100, chunkSize: 8, nChunks: 1, nPerChunk: 1, stride: 8, typeSize: 8}
	var runs []spanRun
	run := base
	runs = run.setRun(runs, 3, 10) // 100, 110, 120
	a := base
	a.dataPos = 130
	b := base
	b.dataPos = 140
	odd := base
	odd.dataPos = 500
	odd.nPerChunk = 2
	in := []span{run, a, b, odd}
	var want int64
	for i := range in {
		n, err := spanSamples(&in[i], runs)
		if err != nil {
			t.Fatal(err)
		}
		want += n
	}
	out, outRuns := coalesceSpans(in, runs)
	var got int64
	for i := range out {
		n, err := spanSamples(&out[i], outRuns)
		if err != nil {
			t.Fatal(err)
		}
		got += n
	}
	if got != want {
		t.Fatalf("coalesce changed the sample count from %d to %d: %+v %+v", want, got, out, outRuns)
	}
	if len(out) != 2 || out[0].repeats(outRuns) != 5 || out[0].repeatStep(outRuns) != 10 {
		t.Fatalf("want one run of 5 then the odd span, got %+v runs %+v", out, outRuns)
	}
}
