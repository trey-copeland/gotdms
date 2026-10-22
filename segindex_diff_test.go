package tdms

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Differential test for run-merged spans. Every file is read four ways:
// fresh parse, version 2 sidecar hit, trust-identity hit, and version 1
// sidecar hit. Values, ReadValues, random Slices, Cursor and CursorAt walks
// must agree with each other and with an oracle that never builds a run.

type diffOracle map[string]any // channel path -> expected Values

type diffCase struct {
	name    string
	file    []byte
	lenient bool
	want    diffOracle // nil: only cross-mode agreement plus prefix check
	prefix  diffOracle // when set, every channel must be a prefix of this
}

func diffSnapshot(t *testing.T, f *File, seed int64) map[string]any {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	out := map[string]any{}
	for _, g := range f.Groups() {
		vals, err := ReadValues(g.Channels())
		out[g.Path()+"#readvalues"] = fmt.Sprint(err)
		if err == nil {
			for i, c := range g.Channels() {
				out[c.Path()+"#rv"] = vals[i]
			}
		}
		for _, c := range g.Channels() {
			p := c.Path()
			v, err := c.Values()
			out[p] = v
			out[p+"#err"] = fmt.Sprint(err)
			n := c.Len()
			for k := 0; k < 12 && n > 0; k++ {
				i := rng.Intn(n)
				m := rng.Intn(n-i) + 1
				s, err := c.Slice(i, m)
				out[fmt.Sprintf("%s#slice%d:%d", p, i, m)] = []any{s, fmt.Sprint(err)}
			}
			for _, batch := range []int{1, 3, 7, 64, 5000} {
				cur := c.Cursor(batch)
				var parts []any
				for cur.Next() {
					parts = append(parts, copyAny(cur.Values()))
				}
				out[fmt.Sprintf("%s#cursor%d", p, batch)] = []any{parts, fmt.Sprint(cur.Err())}
			}
			if n > 0 {
				i := rng.Intn(n)
				m := rng.Intn(n - i + 1)
				cur := c.CursorAt(i, m, 1+rng.Intn(9))
				var parts []any
				for cur.Next() {
					parts = append(parts, copyAny(cur.Values()))
				}
				out[fmt.Sprintf("%s#cursorat%d:%d", p, i, m)] = []any{parts, fmt.Sprint(cur.Err())}
			}
			if dx, ok := c.DAQmx(); ok {
				for _, sc := range dx.Scalers {
					s, err := c.DAQmxSlice(sc.ScaleID, 0, n)
					out[fmt.Sprintf("%s#daqmx%d", p, sc.ScaleID)] = []any{s, fmt.Sprint(err)}
				}
			}
		}
	}
	out["#status"] = f.Status()
	return out
}

func copyAny(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return v
	}
	cp := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
	reflect.Copy(cp, rv)
	return cp.Interface()
}

func runDiffCase(t *testing.T, dc diffCase) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "d.tdms")
	if err := os.WriteFile(path, dc.file, 0o644); err != nil {
		t.Fatal(err)
	}
	var base []ReaderOption
	if dc.lenient {
		base = append(base, WithLenientTail())
	}
	open := func(extra ...ReaderOption) *File {
		f, err := Open(path, append(append([]ReaderOption{}, base...), extra...)...)
		if err != nil {
			t.Fatalf("%s: open: %v", dc.name, err)
		}
		return f
	}
	const seed = 42
	fresh := open()
	want := diffSnapshot(t, fresh, seed)
	for _, g := range fresh.Groups() {
		for _, c := range g.Channels() {
			if rv, ok := want[c.Path()+"#rv"]; ok && !sameAny(rv, want[c.Path()]) {
				t.Errorf("%s: fresh ReadValues %s = %v, Values %v (spans %+v)", dc.name, c.Path(), rv, want[c.Path()], c.spans)
				return
			}
		}
	}
	// Oracle: values that never went through a run.
	if dc.want != nil {
		for p, v := range dc.want {
			if !sameAny(want[p], v) {
				t.Fatalf("%s: fresh parse %s = %v, oracle %v", dc.name, p, want[p], v)
			}
		}
	}
	if dc.prefix != nil {
		for p, full := range dc.prefix {
			got := reflect.ValueOf(want[p])
			fv := reflect.ValueOf(full)
			if !got.IsValid() || got.Len() > fv.Len() || !sameAny(got.Interface(), fv.Slice(0, got.Len()).Interface()) {
				t.Fatalf("%s: %s = %v is not a prefix of %v", dc.name, p, want[p], full)
			}
		}
	}
	// Per-segment oracle: expand runs and read each segment span alone.
	for _, g := range fresh.Groups() {
		for _, c := range g.Channels() {
			if c.dt == TypeString || !c.hasDT || c.total == 0 {
				continue
			}
			sz, ok := c.dt.Size()
			if !ok {
				continue
			}
			exp, err := expandRuns(c.spans, c.runs)
			if err != nil {
				t.Fatal(err)
			}
			var buf []byte
			for i := range exp {
				sp := &exp[i]
				n, err := spanSamples(sp, nil)
				if err != nil {
					t.Fatal(err)
				}
				part := make([]byte, int(n)*sz)
				if err := sp.copyValues(nil, fresh.r, 0, n, part); err != nil {
					t.Fatalf("%s: per-segment read %s: %v", dc.name, c.path, err)
				}
				normalize(c.dt, part, sp.big)
				buf = append(buf, part...)
			}
			v, err := decodePacked(c.dt, buf)
			if err != nil {
				t.Fatal(err)
			}
			if !sameAny(v, want[c.path]) {
				t.Fatalf("%s: %s per-segment %v, run read %v", dc.name, c.path, v, want[c.path])
			}
		}
	}
	fresh.Close()

	side := SegmentIndexPath(path)
	if err := BuildSegmentIndex(path, base...); err != nil {
		t.Fatalf("%s: build: %v", dc.name, err)
	}
	check := func(label string, extra ...ReaderOption) {
		before := segmentParses.Load()
		f := open(extra...)
		defer f.Close()
		if !f.SegmentIndexUsed() || segmentParses.Load() != before {
			t.Fatalf("%s: %s did not use the sidecar", dc.name, label)
		}
		got := diffSnapshot(t, f, seed)
		for k, v := range want {
			if !sameAny(got[k], v) {
				t.Fatalf("%s: %s %s = %v, fresh %v", dc.name, label, k, got[k], v)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %s key count %d vs %d", dc.name, label, len(got), len(want))
		}
	}
	check("v2", WithSegmentIndex(side))
	check("trust", WithSegmentIndex(side), WithSegmentIndexTrustIdentity())

	// Version 1 sidecar from the expanding encoder.
	f := open()
	st, err := f.statData()
	if err != nil {
		t.Fatal(err)
	}
	fp, err := segmentFingerprint(f.r, f.size, f.lenient)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := encodeSegmentIndexV1(f, st.fi.Size(), st.fi.ModTime().UnixNano(), st.dev, st.ino, fp)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if binary.LittleEndian.Uint32(v1[4:8]) != 1 {
		t.Fatal("not v1")
	}
	if err := os.WriteFile(side, v1, 0o644); err != nil {
		t.Fatal(err)
	}
	// Opening with the default check rewrites nothing on a hit.
	check("v1", WithSegmentIndex(side))
}

// --- writer-generated files -------------------------------------------------

type genCh struct {
	name string
	dt   DataType
	w    *ChannelWriter
	vals any
	ctr  int
}

func genValues(dt DataType, start, n, salt int) any {
	switch dt {
	case TypeFloat64:
		v := make([]float64, n)
		for i := range v {
			v[i] = float64(salt*1000000 + start + i)
		}
		return v
	case TypeFloat32:
		v := make([]float32, n)
		for i := range v {
			v[i] = float32(salt*10000 + start + i)
		}
		return v
	case TypeInt16:
		v := make([]int16, n)
		for i := range v {
			v[i] = int16(salt*100 + start + i)
		}
		return v
	case TypeInt32:
		v := make([]int32, n)
		for i := range v {
			v[i] = int32(salt*1000000 + start + i)
		}
		return v
	case TypeUint8:
		v := make([]uint8, n)
		for i := range v {
			v[i] = uint8(salt*7 + start + i)
		}
		return v
	case TypeString:
		v := make([]string, n)
		for i := range v {
			v[i] = fmt.Sprintf("s%d-%d", salt, start+i)
		}
		return v
	}
	panic(dt)
}

func appendAny(dst, src any) any {
	if dst == nil {
		return src
	}
	return reflect.AppendSlice(reflect.ValueOf(dst), reflect.ValueOf(src)).Interface()
}

func genFile(t *testing.T, rng *rand.Rand, dir string, idx int) (string, diffOracle) {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("g%d.tdms", idx))
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	inter := rng.Intn(3) == 0
	w.SetInterleaved(inter)
	types := []DataType{TypeFloat64, TypeFloat32, TypeInt16, TypeInt32, TypeUint8}
	if !inter && rng.Intn(3) == 0 {
		types = append(types, TypeString)
	}
	nch := 1 + rng.Intn(4)
	var chs []*genCh
	for i := 0; i < nch; i++ {
		dt := types[rng.Intn(len(types))]
		grp := "g"
		if rng.Intn(4) == 0 {
			grp = "h"
		}
		cw, err := w.Group(grp).Channel(fmt.Sprintf("c%d", i), dt)
		if err != nil {
			t.Fatal(err)
		}
		chs = append(chs, &genCh{name: "/'" + grp + "'/'" + fmt.Sprintf("c%d", i) + "'", dt: dt, w: cw})
	}
	segs := 1 + rng.Intn(80)
	base := 1 + rng.Intn(5)
	for s := 0; s < segs; s++ {
		cnt := base
		switch rng.Intn(10) {
		case 0:
			cnt = 1 + rng.Intn(9)
		case 1:
			base = 1 + rng.Intn(5)
			cnt = base
		}
		if rng.Intn(15) == 0 {
			inter = !inter
			hasStr := false
			for _, c := range chs {
				hasStr = hasStr || c.dt == TypeString
			}
			if !hasStr {
				w.SetInterleaved(inter)
			}
		}
		wrote := false
		for ci, c := range chs {
			if rng.Intn(8) == 0 && !(ci == len(chs)-1 && !wrote) {
				continue // channel absent from this segment
			}
			n := cnt
			if !inter && rng.Intn(10) == 0 {
				n = rng.Intn(4) // 0 is a zero-sample write
			}
			v := genValues(c.dt, c.ctr, n, ci+1)
			if err := c.w.Write(v); err != nil {
				t.Fatalf("write: %v", err)
			}
			c.ctr += n
			c.vals = appendAny(c.vals, v)
			wrote = true
		}
		if rng.Intn(20) == 0 {
			if err := w.SetProperty(fmt.Sprintf("p%d", s), int32(s)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := diffOracle{}
	for _, c := range chs {
		if c.vals != nil && reflect.ValueOf(c.vals).Len() > 0 {
			want[c.name] = c.vals
		}
	}
	return path, want
}

func TestSegmentIndexRunDifferentialWriter(t *testing.T) {
	iters := 40
	if os.Getenv("DIFF_ITERS") != "" {
		fmt.Sscan(os.Getenv("DIFF_ITERS"), &iters)
	}
	if testing.Short() {
		iters = 30
	}
	dir := t.TempDir()
	for i := 0; i < iters; i++ {
		rng := rand.New(rand.NewSource(int64(1000 + i)))
		path, want := genFile(t, rng, dir, i)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		runDiffCase(t, diffCase{name: fmt.Sprintf("writer%d", i), file: raw, want: want})

		// Lenient tail: cut inside the last segment.
		offs := leadInOffsets(t, raw)
		last := offs[len(offs)-1]
		if len(raw)-last > leadInSize+1 {
			cut := last + leadInSize + rng.Intn(len(raw)-last-leadInSize)
			// Keep metadata intact: cut only inside raw data.
			metaLen := int(binary.LittleEndian.Uint64(raw[last+20 : last+28]))
			if cut > last+leadInSize+metaLen {
				runDiffCase(t, diffCase{name: fmt.Sprintf("writer%d-cut", i), file: append([]byte(nil), raw[:cut]...), lenient: true, prefix: want})
			}
		}
		// Incomplete sentinel on the last segment.
		inc := append([]byte(nil), raw...)
		for k := last + 12; k < last+20; k++ {
			inc[k] = 0xFF
		}
		runDiffCase(t, diffCase{name: fmt.Sprintf("writer%d-incomplete", i), file: inc, prefix: want})
	}
}

// --- hand-built big-endian and DAQmx files ---------------------------------

type handSeg struct {
	objs []dmObj // nil: raw-only segment reusing the previous metadata
	raw  []byte
}

func buildHand(toc uint32, segs []handSeg) []byte {
	var out []byte
	for _, s := range segs {
		if s.objs != nil {
			out = append(out, buildDAQmx(toc, s.objs, s.raw)...)
			continue
		}
		rawToc := toc &^ (TocMetaData | TocNewObjList)
		if toc&TocBigEndian != 0 {
			out = append(out, beLead(rawToc, nil, s.raw)...)
			continue
		}
		out = append(out, encodeLeadIn(rawToc, uint64(len(s.raw)), 0)...)
		out = append(out, s.raw...)
	}
	return out
}

// handOracle reads every segment as its own one-segment file.
func handOracle(t *testing.T, toc uint32, segs []handSeg) diffOracle {
	t.Helper()
	want := diffOracle{}
	var objs []dmObj
	for _, s := range segs {
		if s.objs != nil {
			objs = s.objs
		}
		f := openBytes(t, buildDAQmx(toc, objs, s.raw))
		for _, g := range f.Groups() {
			for _, c := range g.Channels() {
				v, err := c.Values()
				if err != nil {
					t.Fatal(err)
				}
				want[c.Path()] = appendAny(want[c.Path()], v)
			}
		}
	}
	return want
}

func randBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	rng.Read(b)
	return b
}

func TestSegmentIndexRunDifferentialHand(t *testing.T) {
	handIters := 40
	if os.Getenv("DIFF_ITERS") != "" {
		fmt.Sscan(os.Getenv("DIFF_ITERS"), &handIters)
	}
	for i := 0; i < handIters; i++ {
		rng := rand.New(rand.NewSource(int64(7000 + i)))
		type layout struct {
			toc  uint32
			objs func(n uint64) []dmObj
			raw  func(n int) int // raw bytes for n samples-per-channel unit
		}
		widths := []uint32{4, 2}
		layouts := []layout{
			{TocMetaData | TocNewObjList | TocRawData | TocBigEndian, func(n uint64) []dmObj {
				return []dmObj{objStd("/'g'/'a'", TypeInt32, n), objStd("/'g'/'b'", TypeInt16, n)}
			}, func(n int) int { return n * 6 }},
			{TocMetaData | TocNewObjList | TocRawData | TocBigEndian | TocInterleavedData, func(n uint64) []dmObj {
				return []dmObj{objStd("/'g'/'a'", TypeInt32, n), objStd("/'g'/'b'", TypeFloat64, n)}
			}, func(n int) int { return n * 12 }},
			{daqTOC, func(n uint64) []dmObj {
				return []dmObj{
					chDAQmx("/'g'/'a'", n, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 0}}),
					chDAQmx("/'g'/'b'", n, 0, widths, []dmScaler{{typ: 3, off: 2, buf: 0}}),
					chDAQmx("/'g'/'c'", n, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 1}}),
				}
			}, func(n int) int { return n * 6 }},
			{daqTOC, func(n uint64) []dmObj {
				// Two raw buffers whose channels hold different counts.
				return []dmObj{
					chDAQmx("/'g'/'a'", n, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 0}}),
					chDAQmx("/'g'/'b'", n, 0, widths, []dmScaler{{typ: 3, off: 2, buf: 0}}),
					chDAQmx("/'g'/'c'", n+1, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 1}}),
				}
			}, func(n int) int { return n*4 + (n+1)*2 }},
			{daqTOC, func(n uint64) []dmObj {
				return []dmObj{
					chDAQmx("/'g'/'d0'", n, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 0, off: 0}}),
					chDAQmx("/'g'/'d5'", n, RawIndexDAQmxDigitalObserved, []uint32{4}, []dmScaler{{typ: 0, off: 5}}),
				}
			}, func(n int) int { return n * 4 }},
		}
		lay := layouts[i%len(layouts)]
		var segs []handSeg
		n := 1 + rng.Intn(4)
		segs = append(segs, handSeg{objs: lay.objs(uint64(n)), raw: randBytes(rng, lay.raw(n)*(1+rng.Intn(2)))})
		nseg := 1 + rng.Intn(40)
		for s := 0; s < nseg; s++ {
			if rng.Intn(10) == 0 {
				n = 1 + rng.Intn(4)
				segs = append(segs, handSeg{objs: lay.objs(uint64(n)), raw: randBytes(rng, lay.raw(n))})
				continue
			}
			segs = append(segs, handSeg{raw: randBytes(rng, lay.raw(n)*(1+rng.Intn(2)*rng.Intn(2)))})
		}
		// Clear NaN payloads: DeepEqual on float NaN fails.
		file := buildHand(lay.toc, segs)
		want := handOracle(t, lay.toc, segs)
		for p, v := range want {
			if fs, ok := v.([]float64); ok {
				for _, x := range fs {
					if x != x {
						delete(want, p)
						break
					}
				}
			}
		}
		runDiffCase(t, diffCase{name: fmt.Sprintf("hand%d", i), file: file, want: want})
	}
}

// The differential corpus must actually contain runs, or it proves nothing.
func TestSegmentIndexRunDifferentialHasRuns(t *testing.T) {
	dir := t.TempDir()
	runs := 0
	for i := 0; i < 20; i++ {
		rng := rand.New(rand.NewSource(int64(1000 + i)))
		path, _ := genFile(t, rng, dir, i)
		f, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range f.Groups() {
			for _, c := range g.Channels() {
				for _, sp := range c.spans {
					if sp.repeats(c.runs) > 1 {
						runs++
					}
				}
			}
		}
		f.Close()
	}
	if runs < 20 {
		t.Fatalf("only %d runs in the writer corpus", runs)
	}
}

// sameAny is DeepEqual that treats NaN as equal to NaN (random float bytes).
func sameAny(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	return reflect.TypeOf(a) == reflect.TypeOf(b) && fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b)
}

// ReadValues and Group.Values must place each interleaved span at its own
// sample offset. v0.1.0 dropped the per-channel base, so every interleaved
// span after the first overwrote the start of the result.
func TestReadValuesInterleavedMultiSegment(t *testing.T) {
	toc := uint32(TocMetaData | TocNewObjList | TocRawData | TocInterleavedData)
	objs := []dmObj{objStd("/'g'/'a'", TypeInt16, 1), objStd("/'g'/'b'", TypeInt16, 1)}
	segs := []handSeg{
		{objs: objs, raw: i16Raw(1, 101)},
		{raw: i16Raw(2, 102)},
		{raw: i16Raw(3, 103, 4, 104)},
		{raw: i16Raw(5, 105)},
	}
	f := openBytes(t, buildHand(toc, segs))
	g, _ := f.Group("g")
	got, err := g.Values()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got[0], []int16{1, 2, 3, 4, 5})
	assertEqual(t, got[1], []int16{101, 102, 103, 104, 105})

	// DAQmx: two channels share one raw buffer, so each span is interleaved.
	widths := []uint32{4}
	dobjs := func(n uint64) []dmObj {
		return []dmObj{
			chDAQmx("/'g'/'a'", n, 0, widths, []dmScaler{{typ: 3, off: 0, buf: 0}}),
			chDAQmx("/'g'/'b'", n, 0, widths, []dmScaler{{typ: 3, off: 2, buf: 0}}),
		}
	}
	dsegs := []handSeg{
		{objs: dobjs(1), raw: i16Raw(1, 101)},
		{objs: dobjs(2), raw: i16Raw(2, 102, 3, 103)},
		{raw: i16Raw(4, 104, 5, 105)},
	}
	df := openBytes(t, buildHand(daqTOC, dsegs))
	dg, _ := df.Group("g")
	dgot, err := ReadValues(dg.Channels())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, dgot[0], []int16{1, 2, 3, 4, 5})
	assertEqual(t, dgot[1], []int16{101, 102, 103, 104, 105})
}

// A sidecar over the read limit is never written: Open would rewrite it on
// every miss and never hit it, and BuildSegmentIndex would report success.
func TestSegmentIndexOverLimitNotWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alt.tdms")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	for s := 0; s < 200; s++ {
		// Alternating counts: no two neighbours share a layout, so no run.
		if err := ch.Write(make([]float64, 1+s%2)); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	old := segmentIndexLimit
	segmentIndexLimit = 4096
	t.Cleanup(func() { segmentIndexLimit = old })
	side := SegmentIndexPath(path)
	if err := BuildSegmentIndex(path); err == nil {
		t.Fatal("BuildSegmentIndex reported success for a sidecar over the limit")
	}
	f, err := Open(path, WithSegmentIndex(side))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatalf("over-limit sidecar written: %v", err)
	}
}
