package tdms

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

func TestSpanIsSingleSegmentSize(t *testing.T) {
	if unsafe.Sizeof(span{}) != 112 {
		t.Fatalf("span is %d bytes; a no-run file allocates one span per segment", unsafe.Sizeof(span{}))
	}
}

func TestSegmentIndexRunsMatchParse(t *testing.T) {
	t.Run("uniform", func(t *testing.T) {
		path := writeSegments(t, 20, 0, false, false)
		assertIndexMatchesParse(t, path)
		f := openIndexed(t, path)
		defer f.Close()
		ch := chIn(t, f, "g", "a")
		if len(ch.spans) != 1 || ch.spans[0].repeats(ch.runs) != 20 {
			t.Fatalf("spans %d run %d", len(ch.spans), runCountOf(ch))
		}
		blob, err := os.ReadFile(SegmentIndexPath(path))
		if err != nil {
			t.Fatal(err)
		}
		if got := binary.LittleEndian.Uint32(blob[4:8]); got != segmentIndexVersion {
			t.Fatalf("version %d", got)
		}
		// One run per channel, not one span per segment.
		if int64(len(blob)) >= int64(20*spanRecord) {
			t.Fatalf("sidecar %d bytes still looks like one span per segment", len(blob))
		}
		plain := mustOpen(t, path)
		defer plain.Close()
		assertEqual(t, cursorFloats(t, ch), cursorFloats(t, chIn(t, plain, "g", "a")))
	})
	t.Run("mixed", func(t *testing.T) {
		path := writeSegments(t, 25, 5, false, false)
		assertIndexMatchesParse(t, path)
		f := openIndexed(t, path)
		defer f.Close()
		ch := chIn(t, f, "g", "a")
		if len(ch.spans) < 2 || len(ch.spans) >= 25 {
			t.Fatalf("mixed spans %d", len(ch.spans))
		}
		for _, sp := range ch.spans {
			if sp.repeats(ch.runs) > 1 && (sp.str || sp.live || sp.partialN != 0) {
				t.Fatalf("irregular run %+v", sp)
			}
		}
	})
	t.Run("alternating", func(t *testing.T) {
		path := writeSegments(t, 8, 2, false, false)
		assertIndexMatchesParse(t, path)
		f := openIndexed(t, path)
		defer f.Close()
		ch := chIn(t, f, "g", "a")
		if len(ch.spans) != 8 {
			t.Fatalf("alternating collapsed to %d spans", len(ch.spans))
		}
		for _, sp := range ch.spans {
			if sp.runIdx != 0 {
				t.Fatalf("single segment stored a run index %d", sp.runIdx)
			}
		}
		blob, err := os.ReadFile(SegmentIndexPath(path))
		if err != nil {
			t.Fatal(err)
		}
		// Two channels, eight single segments, plus a few hundred bytes of
		// names and header. Run records would add 256 bytes.
		if len(blob) > 8*2*spanRecordV1+400 {
			t.Fatalf("sidecar %d bytes still looks like a run record per span", len(blob))
		}
	})
	t.Run("strings", func(t *testing.T) {
		path := writeSegments(t, 8, 0, true, false)
		assertIndexMatchesParse(t, path)
		f := openIndexed(t, path)
		defer f.Close()
		ch := chIn(t, f, "g", "s")
		if len(ch.spans) != 8 {
			t.Fatalf("string spans %d", len(ch.spans))
		}
		for _, sp := range ch.spans {
			if sp.repeats(ch.runs) > 1 {
				t.Fatal("string span was merged")
			}
		}
	})
	t.Run("interleaved", func(t *testing.T) {
		path := writeSegments(t, 12, 0, false, true)
		assertIndexMatchesParse(t, path)
		f := openIndexed(t, path)
		defer f.Close()
		chs := f.Groups()[0].Channels()
		got, err := ReadValues(chs)
		if err != nil {
			t.Fatal(err)
		}
		plain := mustOpen(t, path)
		defer plain.Close()
		want, err := ReadValues(plain.Groups()[0].Channels())
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, got, want)
		if len(chIn(t, f, "g", "a").spans) != 1 {
			t.Fatalf("interleaved spans %d", len(chIn(t, f, "g", "a").spans))
		}
	})
	t.Run("v1", func(t *testing.T) {
		path := writeSegments(t, 10, 0, false, false)
		if err := BuildSegmentIndex(path); err != nil {
			t.Fatal(err)
		}
		rf, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer rf.Close()
		fi, err := rf.Stat()
		if err != nil {
			t.Fatal(err)
		}
		dev, ino, err := fileID(rf)
		if err != nil {
			t.Fatal(err)
		}
		fp, err := segmentFingerprint(rf, fi.Size(), false)
		if err != nil {
			t.Fatal(err)
		}
		parsed := mustOpen(t, path)
		defer parsed.Close()
		blob, err := encodeSegmentIndexV1(parsed, fi.Size(), fi.ModTime().UnixNano(), dev, ino, fp)
		if err != nil {
			t.Fatal(err)
		}
		if binary.LittleEndian.Uint32(blob[4:8]) != 1 {
			t.Fatal("v1 encode did not write version 1")
		}
		// Expanding the run puts one 96-byte span per segment back on disk.
		if err := os.WriteFile(SegmentIndexPath(path), blob, 0o644); err != nil {
			t.Fatal(err)
		}
		hit := openIndexed(t, path)
		defer hit.Close()
		if !hit.SegmentIndexUsed() {
			t.Fatal("version 1 sidecar was a miss")
		}
		assertEqual(t, mustValues(t, chIn(t, hit, "g", "a")), mustValues(t, chIn(t, parsed, "g", "a")))
		hitCh := chIn(t, hit, "g", "a")
		if hitCh.spans[0].repeats(hitCh.runs) != 10 {
			t.Fatalf("v1 was not coalesced in memory: %+v", hitCh.spans)
		}
		st, err := os.Stat(SegmentIndexPath(path))
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() != int64(len(blob)) {
			t.Fatal("a v1 hit rewrote the sidecar")
		}
	})
}

func TestSegmentIndexV010MissesVersion2(t *testing.T) {
	out, err := exec.Command("git", "show", "v0.1.0:segindex.go").Output()
	if err != nil {
		t.Fatalf("v0.1.0 tag is not in this checkout: %v", err)
	}
	src := string(out)
	if !strings.Contains(src, "segmentIndexVersion = 1") {
		t.Fatal("v0.1.0 segment index version is not 1")
	}
	if !strings.Contains(src, "if binary.LittleEndian.Uint32(b[4:8]) != segmentIndexVersion {") {
		t.Fatal("v0.1.0 does not reject a sidecar version other than 1")
	}
	path := writeSegments(t, 3, 0, false, false)
	if err := BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(SegmentIndexPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(blob[4:8]) != 2 {
		t.Fatalf("wrote version %d", binary.LittleEndian.Uint32(blob[4:8]))
	}
}

func TestSegmentIndexSampleCapMiss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cap.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeInt32)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]int32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	dev, ino, err := fileID(rf)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := segmentFingerprint(rf, fi.Size(), false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.Close()
	c := chIn(t, parsed, "g", "c")

	t.Run("overlapping run", func(t *testing.T) {
		extent := int64(8)
		if fi.Size() <= extent {
			t.Fatalf("file %d", fi.Size())
		}
		// step 1, last end == file size, but each repeat holds 8 sample bytes.
		count := fi.Size() - extent + 1
		sp := span{
			dataPos: 0, chunkSize: extent, nChunks: 1, nPerChunk: 2,
			stride: 4, typeSize: 4,
		}
		c.runs = sp.setRun(nil, count, 1)
		samples, err := spanSamples(&sp, c.runs)
		if err != nil {
			t.Fatal(err)
		}
		bytes := samples * 4
		if bytes <= fi.Size() {
			t.Fatalf("crafted run does not exceed the file: %d bytes in %d", bytes, fi.Size())
		}
		c.spans = []span{sp}
		c.total = samples
		blob, err := encodeSegmentIndex(parsed, fi.Size(), fi.ModTime().UnixNano(), dev, ino, fp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeSegmentIndex(blob); err == nil {
			t.Fatal("overlapping run decoded")
		}
		assertCapMiss(t, path, blob)
	})
	t.Run("many spans", func(t *testing.T) {
		// The previous subtest replaced spans. Rebuild from a fresh open.
		fresh, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		fc := chIn(t, fresh, "g", "c")
		one := fc.spans[0]
		const n = 10000
		fc.spans = make([]span, n)
		var total int64
		for i := range fc.spans {
			fc.spans[i] = one
			fc.spans[i].runIdx = 0
			s, err := spanSamples(&fc.spans[i], nil)
			if err != nil {
				t.Fatal(err)
			}
			total += s
		}
		fc.runs = nil
		fc.total = total
		if total*int64(one.typeSize) <= fi.Size() {
			t.Fatalf("many spans do not exceed the file")
		}
		blob, err := encodeSegmentIndex(fresh, fi.Size(), fi.ModTime().UnixNano(), dev, ino, fp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeSegmentIndex(blob); err == nil {
			t.Fatal("overlapping spans decoded")
		}
		assertCapMiss(t, path, blob)
	})
	t.Run("overflow", func(t *testing.T) {
		fresh, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		fc := chIn(t, fresh, "g", "c")
		over := span{
			dataPos: 0, chunkSize: 4, nChunks: 1, nPerChunk: 1,
			stride: 4, typeSize: 4,
		}
		fc.runs = over.setRun(nil, 4, 1<<62)
		fc.spans = []span{over}
		fc.total = 4
		blob, err := encodeSegmentIndex(fresh, fi.Size(), fi.ModTime().UnixNano(), dev, ino, fp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeSegmentIndex(blob); err == nil {
			t.Fatal("overflowing run decoded")
		}
		assertCapMiss(t, path, blob)
	})
}

func TestSegmentIndexRunDAQmxAndLive(t *testing.T) {
	first := buildDAQmx(daqTOC, []dmObj{daqI16(0, 2)}, i16Raw(1, 2))
	rawOnly := buildDAQmx(TocRawData|TocDAQmxRawData, nil, i16Raw(3, 4))
	file := append([]byte(nil), first...)
	for i := 0; i < 4; i++ {
		file = append(file, rawOnly...)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "daq.tdms")
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}
	assertIndexMatchesParse(t, path)
	f := openIndexed(t, path)
	defer f.Close()
	c := chIn(t, f, "g", "c")
	if len(c.spans) != 1 || c.spans[0].repeats(c.runs) != 5 {
		t.Fatalf("daqmx spans %d run %d", len(c.spans), runCountOf(c))
	}
	sl, err := c.DAQmxSlice(c.daqmx.spec.scalers[c.daqmx.spec.primary].scaleID, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	plain := mustOpen(t, path)
	defer plain.Close()
	pc := chIn(t, plain, "g", "c")
	want, err := pc.DAQmxSlice(pc.daqmx.spec.scalers[pc.daqmx.spec.primary].scaleID, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, sl, want)

	live := writeSegments(t, 4, 0, false, false)
	st, err := os.Stat(live)
	if err != nil {
		t.Fatal(err)
	}
	// The fourth segment starts after three flushes. Patch its next-offset
	// to the incomplete sentinel so the tail span stays live and unmerged.
	wpath := live
	raw, err := os.ReadFile(wpath)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild with a recorded split so the sentinel lands on the last lead-in.
	_ = st
	split := segmentSplit(t, 3)
	if split <= 0 || int(split)+20 > len(raw) {
		// writeSegments does not record the split. Find it from a sibling file.
		split = liveSplit(t)
	}
	if int(split)+20 > len(raw) {
		t.Fatalf("split %d file %d", split, len(raw))
	}
	for i := 0; i < 8; i++ {
		raw[int(split)+12+i] = 0xFF
	}
	if err := os.WriteFile(wpath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	assertIndexMatchesParse(t, wpath)
	lf := openIndexed(t, wpath)
	defer lf.Close()
	lch := chIn(t, lf, "g", "a")
	if len(lch.spans) < 2 {
		t.Fatalf("live tail merged: %+v", lch.spans)
	}
	last := lch.spans[len(lch.spans)-1]
	if !last.live || last.repeats(lch.runs) > 1 {
		t.Fatalf("tail %+v", last)
	}
	if lch.committed() == lch.total {
		t.Fatal("live samples were committed")
	}
}

func TestCoalesceSkipsIrregular(t *testing.T) {
	base := span{
		dataPos: 100, chunkSize: 8, nChunks: 1, nPerChunk: 1,
		stride: 8, typeSize: 8,
	}
	next := base
	next.dataPos = 180
	str := base
	str.str = true
	str.dataPos = 260
	live := base
	live.live = true
	live.dataPos = 340
	partial := base
	partial.partialN = 1
	partial.dataPos = 420
	changed := base
	changed.nPerChunk = 2
	changed.dataPos = 500
	out, runs := coalesceSpans([]span{base, next, str, live, partial, changed}, nil)
	if len(out) != 5 || out[0].repeats(runs) != 2 || out[0].repeatStep(runs) != 80 {
		t.Fatalf("%+v", out)
	}
	for _, sp := range out[1:] {
		if sp.repeats(runs) > 1 {
			t.Fatalf("merged irregular %+v", sp)
		}
	}
}

func TestSegmentIndexTestdata(t *testing.T) {
	err := filepath.Walk("testdata", func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(strings.ToLower(path), ".tdms") {
			assertIndexMatchesParse(t, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func writeSegments(t *testing.T, n, every int, strings, interleaved bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if interleaved {
		w.SetInterleaved(true)
	}
	g := w.Group("g")
	if strings {
		ch, err := g.Channel("s", TypeString)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			if err := ch.Write([]string{"a", "bb"}); err != nil {
				t.Fatal(err)
			}
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		a, err := g.Channel("a", TypeFloat64)
		if err != nil {
			t.Fatal(err)
		}
		b, err := g.Channel("b", TypeInt16)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			af, bf := []float64{float64(i)}, []int16{int16(i)}
			if every > 0 && i%every == 0 {
				af = []float64{float64(i), float64(i) + 0.5}
				bf = []int16{int16(i), int16(i + 1)}
			}
			if err := a.Write(af); err != nil || b.Write(bf) != nil {
				t.Fatal(err)
			}
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertIndexMatchesParse(t *testing.T, path string) {
	t.Helper()
	plain := mustOpen(t, path)
	defer plain.Close()
	assertRunsMatchSingles(t, plain)
	if err := BuildSegmentIndex(path); err != nil {
		t.Fatal(err)
	}
	hit := openIndexed(t, path)
	defer hit.Close()
	if !hit.SegmentIndexUsed() {
		t.Fatal("sidecar miss")
	}
	trust, err := Open(path, WithSegmentIndex(SegmentIndexPath(path)), WithSegmentIndexTrustIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer trust.Close()
	if !trust.SegmentIndexUsed() {
		t.Fatal("trust miss")
	}
	compareFiles(t, plain, hit)
	compareFiles(t, plain, trust)
}

func assertRunsMatchSingles(t *testing.T, f *File) {
	t.Helper()
	type saved struct {
		ch *Channel
		v  any
		sl any
	}
	var want []saved
	for _, g := range f.Groups() {
		for _, c := range g.Channels() {
			v, err := c.Values()
			if err != nil {
				t.Fatal(err)
			}
			var sl any
			if c.Len() > 2 {
				sl, err = c.Slice(1, c.Len()-2)
				if err != nil {
					t.Fatal(err)
				}
			}
			want = append(want, saved{c, v, sl})
		}
	}
	restore, err := expandAllRuns(f)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	for _, s := range want {
		v, err := s.ch.Values()
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, v, s.v)
		if s.sl != nil {
			sl, err := s.ch.Slice(1, s.ch.Len()-2)
			if err != nil {
				t.Fatal(err)
			}
			assertEqual(t, sl, s.sl)
		}
	}
}

func compareFiles(t *testing.T, a, b *File) {
	t.Helper()
	if a.SegmentCount() != b.SegmentCount() || a.version != b.version {
		t.Fatalf("meta %d/%d version %d/%d", a.SegmentCount(), b.SegmentCount(), a.version, b.version)
	}
	ga, gb := a.Groups(), b.Groups()
	if len(ga) != len(gb) {
		t.Fatalf("groups %d %d", len(ga), len(gb))
	}
	for i := range ga {
		ca, cb := ga[i].Channels(), gb[i].Channels()
		if len(ca) != len(cb) {
			t.Fatalf("channels %d %d", len(ca), len(cb))
		}
		va, err := ReadValues(ca)
		if err != nil {
			t.Fatal(err)
		}
		vb, err := ReadValues(cb)
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, va, vb)
		for j := range ca {
			if ca[j].Len() != cb[j].Len() {
				t.Fatalf("len %d %d", ca[j].Len(), cb[j].Len())
			}
			if ca[j].Len() > 2 {
				sa, err := ca[j].Slice(1, ca[j].Len()-2)
				if err != nil {
					t.Fatal(err)
				}
				sb, err := cb[j].Slice(1, cb[j].Len()-2)
				if err != nil {
					t.Fatal(err)
				}
				assertEqual(t, sa, sb)
			}
		}
	}
}

func assertCapMiss(t *testing.T, path string, blob []byte) {
	t.Helper()
	side := filepath.Join(t.TempDir(), "cap.gtdx")
	if err := os.WriteFile(side, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, WithSegmentIndex(side), WithSegmentIndexTrustIdentity())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.SegmentIndexUsed() {
		t.Fatal("hostile sidecar was trusted")
	}
	c := chIn(t, f, "g", "c")
	assertEqual(t, mustValues(t, c), []int32{1, 2, 3})
	if int64(c.Len()) > 3 {
		t.Fatalf("len %d", c.Len())
	}
}

func openIndexed(t *testing.T, path string) *File {
	t.Helper()
	f, err := Open(path, WithSegmentIndex(SegmentIndexPath(path)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func mustOpen(t *testing.T, path string) *File {
	t.Helper()
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func chIn(t *testing.T, f *File, group, name string) *Channel {
	t.Helper()
	g, ok := f.Group(group)
	if !ok {
		t.Fatalf("no group %s", group)
	}
	c, ok := g.Channel(name)
	if !ok {
		t.Fatalf("no channel %s", name)
	}
	return c
}

func runCountOf(c *Channel) int64 {
	if len(c.spans) == 0 {
		return 0
	}
	return c.spans[0].repeats(c.runs)
}

func cursorFloats(t *testing.T, c *Channel) []float64 {
	t.Helper()
	cur := c.Cursor(3)
	defer cur.Close()
	var out []float64
	for cur.Next() {
		b, err := cur.Float64sCopy()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b...)
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func segmentSplit(t *testing.T, nFlush int) int64 { return 0 }

func liveSplit(t *testing.T) int64 {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "split.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("a", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.Group("g").Channel("b", TypeInt16)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := ch.Write([]float64{float64(i)}); err != nil || b.Write([]int16{int16(i)}) != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
