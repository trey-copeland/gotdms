package tdms

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestChunkStatsContiguousAndInterleaved(t *testing.T) {
	samples := make([]float64, 1000)
	for i := range samples {
		samples[i] = math.Sin(float64(i)*0.17) * 12.5
	}
	other := make([]float64, len(samples))
	for i := range other {
		other[i] = float64(i)
	}

	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	// Two segments, so a chunk can cross a segment boundary.
	if err := ch.Write(samples[:600]); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write(samples[600:]); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	c, _ := f.Group("g")
	chR, _ := c.Channel("c")
	for _, chunk := range []int{1, 3, 64, 4096, len(samples) + 10} {
		got := readStats(t, chR, chunk)
		want := bruteStats(samples, chunk)
		if len(got) != len(want) {
			t.Fatalf("chunk %d: %d stats, want %d", chunk, len(got), len(want))
		}
		for i := range want {
			if !sameStat(got[i], want[i]) {
				t.Fatalf("chunk %d stat %d: got %+v want %+v", chunk, i, got[i], want[i])
			}
		}
	}

	ibuf := &bytes.Buffer{}
	iw, err := NewWriter(ibuf)
	if err != nil {
		t.Fatal(err)
	}
	iw.SetInterleaved(true)
	a, err := iw.Group("g").Channel("a", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := iw.Group("g").Channel("b", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Write(samples); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(other); err != nil {
		t.Fatal(err)
	}
	if err := iw.Close(); err != nil {
		t.Fatal(err)
	}
	inf := openBytes(t, ibuf.Bytes())
	ig, _ := inf.Group("g")
	ia, _ := ig.Channel("a")
	got := readStats(t, ia, 128)
	want := bruteStats(samples, 128)
	if len(got) != len(want) {
		t.Fatalf("interleaved: %d stats, want %d", len(got), len(want))
	}
	for i := range want {
		if !sameStat(got[i], want[i]) {
			t.Fatalf("interleaved stat %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestChunkStatsIntegersAndExtended(t *testing.T) {
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("n", TypeInt32)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := w.Group("g").Channel("e", TypeExtendedFloat)
	if err != nil {
		t.Fatal(err)
	}
	ints := []int32{-2, -1, 0, 5, 8}
	exts := []Extended{ExtendedFromFloat64(1.5), ExtendedFromFloat64(-2.5), ExtendedFromFloat64(4)}
	if err := ch.Write(ints); err != nil {
		t.Fatal(err)
	}
	if err := ex.Write(exts); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	ni, _ := g.Channel("n")
	got := readStats(t, ni, 3)
	asFloat := []float64{-2, -1, 0, 5, 8}
	want := bruteStats(asFloat, 3)
	for i := range want {
		if !sameStat(got[i], want[i]) {
			t.Fatalf("int stat %d: %+v want %+v", i, got[i], want[i])
		}
	}
	ei, _ := g.Channel("e")
	egot := readStats(t, ei, 2)
	ef := []float64{1.5, -2.5, 4}
	ewant := bruteStats(ef, 2)
	for i := range ewant {
		if !sameStat(egot[i], ewant[i]) {
			t.Fatalf("extended stat %d: %+v want %+v", i, egot[i], ewant[i])
		}
	}
}

func TestChunkStatsFloat32AndInt16(t *testing.T) {
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	f32, err := w.Group("g").Channel("f", TypeFloat32)
	if err != nil {
		t.Fatal(err)
	}
	i16, err := w.Group("g").Channel("n", TypeInt16)
	if err != nil {
		t.Fatal(err)
	}
	fv := []float32{-1.5, 0, 2.5, 4, -8}
	iv := []int16{-4, -1, 0, 3, 9, 2}
	if err := f32.Write(fv); err != nil {
		t.Fatal(err)
	}
	if err := i16.Write(iv); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	fc, _ := g.Channel("f")
	got := readStats(t, fc, 2)
	asF := []float64{-1.5, 0, 2.5, 4, -8}
	want := bruteStats(asF, 2)
	for i := range want {
		if !sameStat(got[i], want[i]) {
			t.Fatalf("float32 stat %d: %+v want %+v", i, got[i], want[i])
		}
	}
	ic, _ := g.Channel("n")
	igot := readStats(t, ic, 4)
	asI := []float64{-4, -1, 0, 3, 9, 2}
	iwant := bruteStats(asI, 4)
	for i := range iwant {
		if !sameStat(igot[i], iwant[i]) {
			t.Fatalf("int16 stat %d: %+v want %+v", i, igot[i], iwant[i])
		}
	}
}

func TestNumericConversions(t *testing.T) {
	type row struct {
		dt    DataType
		write any
		i64   []int64
		f64   []float64
		badI  bool
	}
	rows := []row{
		{TypeInt8, []int8{-1, 2}, []int64{-1, 2}, []float64{-1, 2}, false},
		{TypeInt16, []int16{-300, 4}, []int64{-300, 4}, []float64{-300, 4}, false},
		{TypeInt32, []int32{1 << 20, -8}, []int64{1 << 20, -8}, []float64{1 << 20, -8}, false},
		{TypeInt64, []int64{-9, 12}, []int64{-9, 12}, []float64{-9, 12}, false},
		{TypeUint8, []uint8{0, 255}, []int64{0, 255}, []float64{0, 255}, false},
		{TypeUint16, []uint16{1, 40000}, []int64{1, 40000}, []float64{1, 40000}, false},
		{TypeUint32, []uint32{2, 3}, []int64{2, 3}, []float64{2, 3}, false},
		{TypeUint64, []uint64{4, 5}, []int64{4, 5}, []float64{4, 5}, false},
		{TypeUint64, []uint64{math.MaxUint64}, nil, []float64{float64(math.MaxUint64)}, true},
		{TypeFloat32, []float32{-1.5, 2}, nil, []float64{-1.5, 2}, true},
		{TypeFloat64, []float64{0.25, -4}, nil, []float64{0.25, -4}, true},
		{TypeBool, []bool{true, false}, []int64{1, 0}, []float64{1, 0}, false},
	}
	for _, r := range rows {
		buf := &bytes.Buffer{}
		w, err := NewWriter(buf)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := w.Group("g").Channel("c", r.dt)
		if err != nil {
			t.Fatal(err)
		}
		if err := ch.Write(r.write); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		f := openBytes(t, buf.Bytes())
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		gotI, err := c.Int64s()
		if r.badI {
			if !errors.Is(err, ErrType) {
				t.Fatalf("%s Int64s err = %v", r.dt, err)
			}
		} else if err != nil {
			t.Fatalf("%s Int64s: %v", r.dt, err)
		} else {
			assertEqual(t, gotI, r.i64)
		}
		gotF, err := c.Float64s()
		if err != nil {
			t.Fatalf("%s Float64s: %v", r.dt, err)
		}
		if len(gotF) != len(r.f64) {
			t.Fatalf("%s float len %d", r.dt, len(gotF))
		}
		for i := range r.f64 {
			if math.Float64bits(gotF[i]) != math.Float64bits(r.f64[i]) {
				t.Fatalf("%s float[%d] = %v want %v", r.dt, i, gotF[i], r.f64[i])
			}
		}
		cur := c.Cursor(8)
		if !cur.Next() {
			t.Fatalf("%s cursor: %v", r.dt, cur.Err())
		}
		copied, err := cur.Float64sCopy()
		cur.Close()
		if err != nil {
			t.Fatalf("%s Float64sCopy: %v", r.dt, err)
		}
		if len(copied) != len(r.f64) {
			t.Fatalf("%s copy len %d", r.dt, len(copied))
		}
		for i := range r.f64 {
			if math.Float64bits(copied[i]) != math.Float64bits(r.f64[i]) {
				t.Fatalf("%s copy[%d] = %v", r.dt, i, copied[i])
			}
		}
	}
}

func TestChunkStatsRejectsNonNumeric(t *testing.T) {
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	s, err := w.Group("g").Channel("s", TypeString)
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.Group("g").Channel("b", TypeBool)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := w.Group("g").Channel("t", TypeTimestamp)
	if err != nil {
		t.Fatal(err)
	}
	cx, err := w.Group("g").Channel("z", TypeComplex128)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]bool{true}); err != nil {
		t.Fatal(err)
	}
	if err := ts.Write([]Timestamp{{Seconds: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := cx.Write([]complex128{1 + 2i}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	for _, name := range []string{"s", "b", "t", "z"} {
		ch, _ := g.Channel(name)
		_, err := ch.ChunkStats(4)
		if !errors.Is(err, ErrType) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A numeric channel rejects a non-positive chunk size even when the type matches.
	nf := openBytes(t, fixedIndexFile(t, TypeFloat64, 0, false, nil))
	ng, _ := nf.Group("g")
	nc, _ := ng.Channel("c")
	if _, err := nc.ChunkStats(0); !errors.Is(err, ErrArgument) {
		t.Fatalf("zero chunk: %v", err)
	}
}

func TestChunkStatsEmptyAndNaN(t *testing.T) {
	// A raw-data index with a float64 type and a count of zero. The writer
	// does not emit a type for a channel that was never written.
	emptyFile := fixedIndexFile(t, TypeFloat64, 0, false, nil)
	ef := openBytes(t, emptyFile)
	eg, _ := ef.Group("g")
	e, _ := eg.Channel("c")
	if e.DataType() != TypeFloat64 || e.Len() != 0 {
		t.Fatalf("type %s len %d", e.DataType(), e.Len())
	}
	cur, err := e.ChunkStats(4)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Next() {
		t.Fatal("empty channel yielded a chunk")
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}

	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	nanCh, err := w.Group("g").Channel("nan", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := nanCh.Write([]float64{1, math.NaN(), 3}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	n, _ := g.Channel("nan")
	got := readStats(t, n, 8)
	if len(got) != 1 || got[0].Count != 3 || !math.IsNaN(got[0].Min) || !math.IsNaN(got[0].Max) || !math.IsNaN(got[0].Mean) || !math.IsNaN(got[0].RMS) {
		t.Fatalf("%+v", got)
	}
}

func TestChunkStatsMemoryBounded(t *testing.T) {
	const n = 1 << 20 // 8 MiB of float64
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = float64(i%1000) * 0.5
	}
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write(samples); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cur, err := c.ChunkStats(4096)
			if err != nil {
				b.Fatal(err)
			}
			var last ChunkStat
			for cur.Next() {
				last = cur.Stat()
			}
			cur.Close()
			if err := cur.Err(); err != nil {
				b.Fatal(err)
			}
			if last.Count == 0 {
				b.Fatal("no stats")
			}
		}
	})
	// The channel is 8 MiB. A bounded pass should stay near the 32 KiB chunk
	// (4096 float64) plus a small constant, far under the channel size.
	if res.AllocedBytesPerOp() > 1<<20 {
		t.Fatalf("allocs %d bytes/op, want a bounded pass under 1 MiB (channel is 8 MiB)", res.AllocedBytesPerOp())
	}
	if res.N < 1 {
		t.Fatal("benchmark did not run")
	}
}

func BenchmarkChunkStatsFloat64(b *testing.B) {
	const n = 1 << 20
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = float64(i%1000) * 0.5
	}
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		b.Fatal(err)
	}
	if err := ch.Write(samples); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	f, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		b.Fatal(err)
	}
	g, _ := f.Group("g")
	c, _ := g.Channel("c")
	b.ReportAllocs()
	b.SetBytes(int64(n) * 8)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cur, err := c.ChunkStats(4096)
		if err != nil {
			b.Fatal(err)
		}
		var last ChunkStat
		for cur.Next() {
			last = cur.Stat()
		}
		cur.Close()
		if err := cur.Err(); err != nil {
			b.Fatal(err)
		}
		if last.Count == 0 {
			b.Fatal("no stats")
		}
	}
}

func readStats(t *testing.T, ch *Channel, chunk int) []ChunkStat {
	t.Helper()
	cur, err := ch.ChunkStats(chunk)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()
	var out []ChunkStat
	for cur.Next() {
		out = append(out, cur.Stat())
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func bruteStats(samples []float64, chunk int) []ChunkStat {
	var out []ChunkStat
	for i := 0; i < len(samples); i += chunk {
		j := i + chunk
		if j > len(samples) {
			j = len(samples)
		}
		part := samples[i:j]
		st := ChunkStat{Index: len(out), Start: i, Count: len(part), Min: math.Inf(1), Max: math.Inf(-1)}
		var sum, sumsq float64
		for _, x := range part {
			sum += x
			sumsq += x * x
			if x < st.Min {
				st.Min = x
			}
			if x > st.Max {
				st.Max = x
			}
		}
		st.Mean = sum / float64(len(part))
		st.RMS = math.Sqrt(sumsq / float64(len(part)))
		out = append(out, st)
	}
	return out
}

func sameStat(a, b ChunkStat) bool {
	if a.Index != b.Index || a.Start != b.Start || a.Count != b.Count {
		return false
	}
	return a.Min == b.Min && a.Max == b.Max && a.Mean == b.Mean && a.RMS == b.RMS
}
