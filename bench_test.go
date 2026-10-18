package tdms

import (
	"bytes"
	"os"
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
			ch, _ := g.Channel(string(rune('a' + c)))
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
