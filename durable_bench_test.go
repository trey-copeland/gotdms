package tdms

import (
	"fmt"
	"path/filepath"
	"testing"
)

// BenchmarkCreateWrite writes 64 MiB of float64 through Create in segments
// of the given size, with and without WithSync.
func BenchmarkCreateWrite(b *testing.B) {
	const total = 64 << 20
	for _, seg := range []int{256 << 10, 1 << 20, 16 << 20} {
		for _, durable := range []bool{false, true} {
			name := fmt.Sprintf("seg=%dKiB/sync=%v", seg>>10, durable)
			b.Run(name, func(b *testing.B) {
				vals := make([]float64, seg/8)
				for i := range vals {
					vals[i] = float64(i)
				}
				dir := b.TempDir()
				b.SetBytes(total)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var opts []WriterOption
					if durable {
						opts = append(opts, WithSync())
					}
					w, err := Create(filepath.Join(dir, "b.tdms"), opts...)
					if err != nil {
						b.Fatal(err)
					}
					ch, err := w.Group("g").Channel("c", TypeFloat64)
					if err != nil {
						b.Fatal(err)
					}
					for n := 0; n < total/seg; n++ {
						if err := ch.Write(vals); err != nil {
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
			})
		}
	}
}
