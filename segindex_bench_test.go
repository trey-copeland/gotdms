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
var fragmentedFiles struct {
	sync.Mutex
	paths map[int]string
}

func fragmentedTDMS(b *testing.B, segs int) string {
	b.Helper()
	fragmentedFiles.Lock()
	defer fragmentedFiles.Unlock()
	if fragmentedFiles.paths == nil {
		fragmentedFiles.paths = map[int]string{}
	}
	if p, ok := fragmentedFiles.paths[segs]; ok {
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
	for i := 0; i < segs; i++ {
		for _, ch := range chs {
			if err := ch.Write(one); err != nil {
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
	fragmentedFiles.paths[segs] = path
	return path
}

func BenchmarkSegmentIndexOpen(b *testing.B) {
	for _, segs := range []int{10_000, 100_000} {
		path := fragmentedTDMS(b, segs)
		side := SegmentIndexPath(path)
		b.Run(fmt.Sprintf("%d/none", segs), func(b *testing.B) {
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
		b.Run(fmt.Sprintf("%d/sidecar", segs), func(b *testing.B) {
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
		b.Run(fmt.Sprintf("%d/trust", segs), func(b *testing.B) {
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
