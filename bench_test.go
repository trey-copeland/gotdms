package tdms

import (
	"bytes"
	"os"
	"strconv"
	"testing"
)

func BenchmarkWriteFloat64(b *testing.B) {
	const n = 100000
	data := make([]float64, n)
	for i := range data {
		data[i] = float64(i)
	}
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := &bytes.Buffer{}
		w, err := NewWriter(buf)
		if err != nil {
			b.Fatal(err)
		}
		ch, err := w.Group("g").Channel("c", TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		if err := ch.Write(data); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadFloat64(b *testing.B) {
	const n = 100000
	data := make([]float64, n)
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		b.Fatal(err)
	}
	if err := ch.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		if _, err := c.Float64s(); err != nil {
			b.Fatal(err)
		}
		f.Close()
	}
}

func BenchmarkStreamFloat64(b *testing.B) {
	const n = 100000
	data := make([]float64, n)
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		b.Fatal(err)
	}
	if err := ch.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		cur := c.Cursor(4096)
		var count int
		for cur.Next() {
			count += cur.Len()
		}
		if err := cur.Err(); err != nil {
			b.Fatal(err)
		}
		if count != n {
			b.Fatalf("count %d", count)
		}
		f.Close()
	}
}

// Large benchmarks exercise the contiguous, interleaved, cursor, and write
// paths at 128 MiB. They are not part of go test ./... . Run:
//
//	go test -bench=BenchmarkLarge -benchmem -count=3
func benchFloats(n int) []float64 {
	data := make([]float64, n)
	for i := range data {
		data[i] = float64(i)
	}
	return data
}

func writeContigBench(b *testing.B, n int, toFile bool) ([]byte, string) {
	b.Helper()
	data := benchFloats(n)
	if toFile {
		f, err := os.CreateTemp("", "tdms-bench-*.tdms")
		if err != nil {
			b.Fatal(err)
		}
		name := f.Name()
		w, err := NewWriter(f)
		if err != nil {
			b.Fatal(err)
		}
		ch, err := w.Group("g").Channel("c", TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		if err := ch.Write(data); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
		return nil, name
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
	if err := ch.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	return append([]byte(nil), buf.Bytes()...), ""
}

func BenchmarkLargeReadFloat64(b *testing.B) {
	const n = 1 << 24 // 16,777,216 float64 values, 128 MiB
	raw, _ := writeContigBench(b, n, false)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		v, err := c.Float64s()
		if err != nil {
			b.Fatal(err)
		}
		if len(v) != n || v[n-1] != float64(n-1) {
			b.Fatalf("len %d", len(v))
		}
		f.Close()
	}
}

func BenchmarkLargeReadFloat64File(b *testing.B) {
	const n = 1 << 24
	_, name := writeContigBench(b, n, true)
	defer os.Remove(name)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := Open(name)
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		v, err := c.Float64s()
		if err != nil {
			b.Fatal(err)
		}
		if len(v) != n {
			b.Fatalf("len %d", len(v))
		}
		f.Close()
	}
}

func BenchmarkLargeCursorFile(b *testing.B) {
	const n = 1 << 24
	_, name := writeContigBench(b, n, true)
	defer os.Remove(name)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := Open(name)
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		cur := c.Cursor(4096)
		var count int
		var sink float64
		for cur.Next() {
			batch := cur.Values().([]float64)
			count += len(batch)
			sink += batch[0]
		}
		if err := cur.Err(); err != nil {
			b.Fatal(err)
		}
		if count != n {
			b.Fatalf("count %d", count)
		}
		f.Close()
		if sink == -1 {
			b.Fatal("sink")
		}
	}
}

func BenchmarkLargeInterleavedOnce(b *testing.B) {
	benchInterleaved(b, 1<<22, 4, true)
}

func BenchmarkLargeInterleaved(b *testing.B) {
	const n = 1 << 22 // 4 channels * 4,194,304 * 8 bytes = 128 MiB
	const chans = 4
	cols := make([][]float64, chans)
	for c := 0; c < chans; c++ {
		cols[c] = make([]float64, n)
		for i := range cols[c] {
			cols[c][i] = float64(c*n + i)
		}
	}
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	w.SetInterleaved(true)
	for c := 0; c < chans; c++ {
		ch, err := w.Group("g").Channel(string(rune('a'+c)), TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		if err := ch.Write(cols[c]); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	names := make([]string, chans)
	for c := range names {
		names[c] = string(rune('a' + c))
	}
	b.SetBytes(int64(n) * 8 * chans)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		for c := 0; c < chans; c++ {
			ch, _ := g.Channel(names[c])
			v, err := ch.Float64s()
			if err != nil {
				b.Fatal(err)
			}
			if len(v) != n || v[n-1] != cols[c][n-1] {
				b.Fatalf("chan %d len %d", c, len(v))
			}
		}
		f.Close()
	}
}

func benchInterleaved(b *testing.B, n, chans int, onePass bool) {
	b.Helper()
	cols := make([][]float64, chans)
	names := make([]string, chans)
	for c := 0; c < chans; c++ {
		names[c] = "c" + strconv.Itoa(c)
		cols[c] = make([]float64, n)
		for i := range cols[c] {
			cols[c][i] = float64(c*n + i)
		}
	}
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	w.SetInterleaved(true)
	for c := 0; c < chans; c++ {
		ch, err := w.Group("g").Channel(names[c], TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		if err := ch.Write(cols[c]); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	b.SetBytes(int64(n) * 8 * int64(chans))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		if onePass {
			chs := make([]*Channel, chans)
			for c := 0; c < chans; c++ {
				chs[c], _ = g.Channel(names[c])
			}
			got, err := ReadValues(chs)
			if err != nil {
				b.Fatal(err)
			}
			last := got[chans-1].([]float64)
			if len(last) != n || last[n-1] != cols[chans-1][n-1] {
				b.Fatalf("len %d", len(last))
			}
		} else {
			for c := 0; c < chans; c++ {
				ch, _ := g.Channel(names[c])
				v, err := ch.Float64s()
				if err != nil {
					b.Fatal(err)
				}
				if len(v) != n || v[n-1] != cols[c][n-1] {
					b.Fatalf("chan %d len %d", c, len(v))
				}
			}
		}
		f.Close()
	}
}

// BenchmarkLargeInterleavedMany is 32 float64 channels, 1<<16 samples each
// (16 MiB total). It is the wide-multiplex case: stride grows with channel
// count, and each channel rereads the shared span. On demand, like the other
// BenchmarkLarge cases.
func BenchmarkLargeInterleavedManyOnce(b *testing.B) {
	benchInterleaved(b, 1<<16, 32, true)
}

func BenchmarkLargeInterleavedManySeparate(b *testing.B) {
	benchInterleaved(b, 1<<16, 32, false)
}

func BenchmarkLargeInterleavedMany(b *testing.B) {
	const n = 1 << 16
	const chans = 32
	cols := make([][]float64, chans)
	for c := 0; c < chans; c++ {
		cols[c] = make([]float64, n)
		for i := range cols[c] {
			cols[c][i] = float64(c*n + i)
		}
	}
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	w.SetInterleaved(true)
	for c := 0; c < chans; c++ {
		ch, err := w.Group("g").Channel("c"+strconv.Itoa(c), TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		if err := ch.Write(cols[c]); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	names := make([]string, chans)
	for c := range names {
		names[c] = "c" + strconv.Itoa(c)
	}
	b.SetBytes(int64(n) * 8 * chans)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		for c := 0; c < chans; c++ {
			ch, _ := g.Channel(names[c])
			v, err := ch.Float64s()
			if err != nil {
				b.Fatal(err)
			}
			if len(v) != n || v[n-1] != cols[c][n-1] {
				b.Fatalf("chan %d len %d", c, len(v))
			}
		}
		f.Close()
	}
}

func BenchmarkLargeWriteFile(b *testing.B) {
	const n = 1 << 24
	data := benchFloats(n)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := os.CreateTemp("", "tdms-w-*.tdms")
		if err != nil {
			b.Fatal(err)
		}
		name := f.Name()
		w, err := NewWriter(f)
		if err != nil {
			b.Fatal(err)
		}
		ch, err := w.Group("g").Channel("c", TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		if err := ch.Write(data); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
		os.Remove(name)
	}
}

func BenchmarkLargeWriteMem(b *testing.B) {
	const n = 1 << 24
	data := benchFloats(n)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := &bytes.Buffer{}
		w, err := NewWriter(buf)
		if err != nil {
			b.Fatal(err)
		}
		ch, err := w.Group("g").Channel("c", TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		if err := ch.Write(data); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
		if buf.Len() < n*8 {
			b.Fatalf("short %d", buf.Len())
		}
	}
}

func BenchmarkCursorDirectSmall(b *testing.B) { benchCursorDirect(b, 4096, 2) }
func BenchmarkCursorBufferedSmall(b *testing.B) {
	benchCursorDirect(b, 4096, 0)
}
func BenchmarkCursorDirectTiny(b *testing.B) { benchCursorDirect(b, 1024, 2) }
func BenchmarkCursorBufferedTiny(b *testing.B) {
	benchCursorDirect(b, 1024, 0)
}
func BenchmarkCursorDirectBoundary(b *testing.B) {
	benchCursorDirect(b, 32768, 2) // 256 KiB, the sequential buffer size
}
func BenchmarkCursorBufferedBoundary(b *testing.B) {
	benchCursorDirect(b, 32768, 0)
}
func BenchmarkCursorDirectLarge(b *testing.B) { benchCursorDirect(b, 1<<16, 2) }
func BenchmarkCursorBufferedLarge(b *testing.B) {
	benchCursorDirect(b, 1<<16, 0)
}

func benchCursorDirect(b *testing.B, batch, mode int) {
	const n = 1 << 22 // 32 MiB; enough batches at every size below
	_, name := writeContigBench(b, n, true)
	defer os.Remove(name)
	prev := contigDirect
	contigDirect = mode
	defer func() { contigDirect = prev }()
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := Open(name)
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		cur := c.Cursor(batch)
		var count int
		var sink float64
		for cur.Next() {
			batchv := cur.Values().([]float64)
			count += len(batchv)
			sink += batchv[0]
		}
		if err := cur.Err(); err != nil {
			b.Fatal(err)
		}
		if count != n {
			b.Fatalf("count %d", count)
		}
		f.Close()
		if sink == -1 {
			b.Fatal("sink")
		}
	}
}

func BenchmarkTailFull(b *testing.B)  { benchTail(b, false) }
func BenchmarkTailSlice(b *testing.B) { benchTail(b, true) }

func benchTail(b *testing.B, slice bool) {
	const n = 1 << 20
	data := benchFloats(n)
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		b.Fatal(err)
	}
	if err := ch.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	b.SetBytes(64 * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		var last float64
		if slice {
			v, err := c.Slice(n-64, 64)
			if err != nil {
				b.Fatal(err)
			}
			last = v.([]float64)[63]
		} else {
			v, err := c.Float64s()
			if err != nil {
				b.Fatal(err)
			}
			last = v[n-1]
		}
		if last != float64(n-1) {
			b.Fatalf("last %v", last)
		}
		f.Close()
	}
}

func BenchmarkCursorFloat64sCopy(b *testing.B) {
	const n = 1 << 16
	data := benchFloats(n)
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		b.Fatal(err)
	}
	if err := ch.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		cur := c.Cursor(4096)
		var sink float64
		for cur.Next() {
			v, err := cur.Float64sCopy()
			if err != nil {
				b.Fatal(err)
			}
			sink += v[0]
		}
		if err := cur.Err(); err != nil {
			b.Fatal(err)
		}
		f.Close()
		if sink == -1 {
			b.Fatal("sink")
		}
	}
}

func BenchmarkCursorFloat64sAlias(b *testing.B) {
	const n = 1 << 16
	data := benchFloats(n)
	buf := &bytes.Buffer{}
	w, err := NewWriter(buf)
	if err != nil {
		b.Fatal(err)
	}
	ch, err := w.Group("g").Channel("c", TypeFloat64)
	if err != nil {
		b.Fatal(err)
	}
	if err := ch.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	b.SetBytes(int64(n) * 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := NewReader(bytes.NewReader(raw))
		if err != nil {
			b.Fatal(err)
		}
		g, _ := f.Group("g")
		c, _ := g.Channel("c")
		cur := c.Cursor(4096)
		var sink float64
		for cur.Next() {
			v, err := cur.Float64s()
			if err != nil {
				b.Fatal(err)
			}
			sink += v[0]
		}
		if err := cur.Err(); err != nil {
			b.Fatal(err)
		}
		f.Close()
		if sink == -1 {
			b.Fatal("sink")
		}
	}
}

func BenchmarkRepeatFlush(b *testing.B) { benchRepeatFlush(b, true) }

func BenchmarkRepeatFlushRebuild(b *testing.B) { benchRepeatFlush(b, false) }

func benchRepeatFlush(b *testing.B, reuse bool) {
	prev := metaReuse
	metaReuse = reuse
	defer func() { metaReuse = prev }()
	data := []float64{1, 2, 3, 4}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := &bytes.Buffer{}
		w, err := NewWriter(buf)
		if err != nil {
			b.Fatal(err)
		}
		ch, err := w.Group("g").Channel("c", TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		for k := 0; k < 32; k++ {
			if err := ch.Write(data); err != nil {
				b.Fatal(err)
			}
			if err := w.Flush(); err != nil {
				b.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
