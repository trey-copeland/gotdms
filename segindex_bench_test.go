package tdms

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Fragmented files are generated once per process. Each segment after the
// first is a raw-only lead-in plus four float64 samples, which is the shape
// that makes a lead-in walk expensive: many small segments, little metadata.
// every > 0 writes two samples on each segment whose index is a multiple of
// every, so the run splitter cannot collapse the whole file. every == 2
// alternates one and two samples, so no adjacent spans can merge.
var fragmentedFiles struct {
	sync.Mutex
	paths map[fragKey]string
}

type fragKey struct {
	segs  int
	every int
}

func TestMain(m *testing.M) {
	code := m.Run()
	fragmentedFiles.Lock()
	for _, p := range fragmentedFiles.paths {
		os.RemoveAll(filepath.Dir(p))
	}
	fragmentedFiles.Unlock()
	os.Exit(code)
}

func fragmentedTDMS(b *testing.B, segs, every int) string {
	b.Helper()
	fragmentedFiles.Lock()
	defer fragmentedFiles.Unlock()
	if fragmentedFiles.paths == nil {
		fragmentedFiles.paths = map[fragKey]string{}
	}
	key := fragKey{segs, every}
	if p, ok := fragmentedFiles.paths[key]; ok {
		return p
	}
	dir, err := os.MkdirTemp("", "gtdx-frag-")
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "frag.tdms")
	w, err := Create(path)
	if err != nil {
		b.Fatal(err)
	}
	g := w.Group("acq")
	names := []string{"a", "b", "c", "d"}
	chs := make([]*ChannelWriter, len(names))
	for i, name := range names {
		ch, err := g.Channel(name, TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		chs[i] = ch
	}
	one := []float64{1}
	two := []float64{1, 2}
	for i := 0; i < segs; i++ {
		buf := one
		if every > 0 && i%every == 0 {
			buf = two
		}
		for _, ch := range chs {
			if err := ch.Write(buf); err != nil {
				b.Fatal(err)
			}
		}
		if err := w.Flush(); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	if err := BuildSegmentIndex(path); err != nil {
		b.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	ss, err := os.Stat(SegmentIndexPath(path))
	if err != nil {
		b.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "frag segs=%d every=%d tdms=%d sidecar=%d\n", segs, every, st.Size(), ss.Size())
	fragmentedFiles.paths[key] = path
	return path
}

func BenchmarkSegmentIndexOpen(b *testing.B) {
	cases := []struct {
		segs  int
		every int
		name  string
	}{
		{10_000, 0, "uniform"},
		{10_000, 1000, "mixed"},
		{10_000, 2, "alternating"},
		{100_000, 0, "uniform"},
		{100_000, 1000, "mixed"},
		{100_000, 2, "alternating"},
	}
	for _, c := range cases {
		path := fragmentedTDMS(b, c.segs, c.every)
		side := SegmentIndexPath(path)
		segs := c.segs
		b.Run(fmt.Sprintf("%d/%s/none", segs, c.name), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				f, err := Open(path)
				if err != nil {
					b.Fatal(err)
				}
				if f.SegmentCount() != segs || f.SegmentIndexUsed() {
					b.Fatalf("segs %d used %v", f.SegmentCount(), f.SegmentIndexUsed())
				}
				f.Close()
			}
		})
		b.Run(fmt.Sprintf("%d/%s/sidecar", segs, c.name), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				f, err := Open(path, WithSegmentIndex(side))
				if err != nil {
					b.Fatal(err)
				}
				if !f.SegmentIndexUsed() || f.SegmentCount() != segs {
					b.Fatalf("used %v segs %d", f.SegmentIndexUsed(), f.SegmentCount())
				}
				f.Close()
			}
		})
		b.Run(fmt.Sprintf("%d/%s/trust", segs, c.name), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				f, err := Open(path, WithSegmentIndex(side), WithSegmentIndexTrustIdentity())
				if err != nil {
					b.Fatal(err)
				}
				if !f.SegmentIndexUsed() || f.SegmentCount() != segs {
					b.Fatalf("used %v segs %d", f.SegmentIndexUsed(), f.SegmentCount())
				}
				f.Close()
			}
		})
	}
}
