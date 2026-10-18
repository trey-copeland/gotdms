package dataset

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/trey-copeland/gotdms"
)

func BenchmarkCatalog(b *testing.B) {
	dir := b.TempDir()
	epoch := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	paths := make([]string, 100)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("f%03d.tdms", i))
		w, err := tdms.Create(paths[i])
		if err != nil {
			b.Fatal(err)
		}
		sec := []float64{float64(i)}
		for _, name := range []string{"Time", "ai0"} {
			ch, err := w.Group("acquisition").Channel(name, tdms.TypeFloat64)
			if err != nil {
				b.Fatal(err)
			}
			if err := ch.Write(sec); err != nil {
				b.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ds, err := OpenFiles(paths, WithTimestampChannelName("Time"), WithNumericEpoch(epoch))
		if err != nil {
			b.Fatal(err)
		}
		if len(ds.Occurrences()) != 200 {
			b.Fatalf("occurrences = %d", len(ds.Occurrences()))
		}
	}
}
