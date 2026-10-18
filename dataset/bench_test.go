package dataset

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/trey-copeland/gotdms"
)

// Heavy benchmarks below are not part of go test ./.... Run them with
// go test ./dataset -bench 'BenchmarkCatalogShared|BenchmarkReadManyShared|BenchmarkReadWindowTail' -benchmem -benchtime=1x -count=3

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

// sharedClockFile is one group, a numeric Time channel, and nch data channels,
// each with n samples. Time[i] = i seconds. Data values are the channel index.
func sharedClockFile(b *testing.B, n, nch int) string {
	b.Helper()
	dir := b.TempDir()
	path := filepath.Join(dir, "run.tdms")
	w, err := tdms.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	sec := make([]float64, n)
	for i := range sec {
		sec[i] = float64(i)
	}
	ch, err := w.Group("acquisition").Channel("Time", tdms.TypeFloat64)
	if err != nil {
		b.Fatal(err)
	}
	if err := ch.Write(sec); err != nil {
		b.Fatal(err)
	}
	for c := 0; c < nch; c++ {
		y := make([]float64, n)
		for i := range y {
			y[i] = float64(c)
		}
		ch, err := w.Group("acquisition").Channel(fmt.Sprintf("ai%d", c), tdms.TypeFloat64)
		if err != nil {
			b.Fatal(err)
		}
		if err := ch.Write(y); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	return path
}

func catalogOpts() []Option {
	return []Option{
		WithTimestampChannelName("Time"),
		WithNumericEpoch(epoch),
		WithWorkers(4),
		WithReadWorkers(4),
	}
}

// samplePeak runs fn once and returns the highest HeapAlloc observed from a
// side goroutine. Indicative only: ReadMemStats stops the world and can miss
// a short spike.
func samplePeak(fn func()) uint64 {
	stop := make(chan struct{})
	var mu sync.Mutex
	var peak uint64
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.ReadMemStats(&m)
			mu.Lock()
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
			mu.Unlock()
		}
	}()
	fn()
	close(stop)
	return peak
}

// BenchmarkCatalogSharedClock opens one file whose timestamp channel is long
// and shared by several data channels. Catalog used to walk that axis once
// per channel.
func BenchmarkCatalogSharedClock(b *testing.B) {
	const n = 1 << 20
	const nch = 8
	path := sharedClockFile(b, n, nch)
	opts := catalogOpts()
	peak := samplePeak(func() {
		ds, err := OpenFiles([]string{path}, opts...)
		if err != nil {
			b.Fatal(err)
		}
		if len(ds.Find("ai0")) != 1 {
			b.Fatal("missing ai0")
		}
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ds, err := OpenFiles([]string{path}, opts...)
		if err != nil {
			b.Fatal(err)
		}
		if len(ds.Occurrences()) != nch+1 {
			b.Fatalf("occurrences = %d", len(ds.Occurrences()))
		}
	}
	// ResetTimer clears metrics recorded before the loop.
	b.ReportMetric(float64(peak)/(1<<20), "peak-MiB")
}

// BenchmarkReadManySharedClock reads every data channel over the full span.
// The channels share one clock in one file.
func BenchmarkReadManySharedClock(b *testing.B) {
	const n = 1 << 20
	const nch = 8
	path := sharedClockFile(b, n, nch)
	opts := catalogOpts()
	ds, err := OpenFiles([]string{path}, opts...)
	if err != nil {
		b.Fatal(err)
	}
	sels := make([]Selector, nch)
	for i := range sels {
		sels[i] = Selector{Name: fmt.Sprintf("ai%d", i), Group: "acquisition"}
	}
	start := epoch
	end := epoch.Add(time.Duration(n) * time.Second)
	b.SetBytes(int64(n) * 8 * int64(nch))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := ds.ReadMany(sels, start, end)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != nch || len(out[0].Y) != n || len(out[nch-1].Y) != n {
			b.Fatalf("series %d len %d", len(out), len(out[0].Y))
		}
	}
}

// BenchmarkReadWindowTail reads 64 samples at the end of a 1<<20 timestamp
// axis. main has no range read, so this decodes the prefix and then clips.
// The result is that cost. It is not a claim that the tail is cheap.
func BenchmarkReadWindowTail(b *testing.B) {
	const n = 1 << 20
	path := sharedClockFile(b, n, 1)
	ds, err := OpenFiles([]string{path}, catalogOpts()...)
	if err != nil {
		b.Fatal(err)
	}
	start := epoch.Add(time.Duration(n-64) * time.Second)
	end := epoch.Add(time.Duration(n) * time.Second)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := ds.ReadWindow(Selector{Name: "ai0", Group: "acquisition"}, start, end)
		if err != nil {
			b.Fatal(err)
		}
		if len(s.Y) != 64 {
			b.Fatalf("len %d", len(s.Y))
		}
	}
}

// BenchmarkReadWindowTailWaveform is the same tail window on a wf_* axis.
// The index of the window is known from t0 and dt, but main exports no
// per-segment sample counts and no range read, so the cursor still starts
// at sample 0. The benchmark measures that prefix decode.
func BenchmarkReadWindowTailWaveform(b *testing.B) {
	const n = 1 << 20
	dir := b.TempDir()
	path := filepath.Join(dir, "wave.tdms")
	w, err := tdms.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	y := make([]float64, n)
	for i := range y {
		y[i] = float64(i)
	}
	ch, err := w.Group("acquisition").Channel("ai0", tdms.TypeFloat64)
	if err != nil {
		b.Fatal(err)
	}
	if err := ch.SetProperty(tdms.PropStartTime, epoch); err != nil {
		b.Fatal(err)
	}
	if err := ch.SetProperty(tdms.PropIncrement, 1.0); err != nil {
		b.Fatal(err)
	}
	if err := ch.Write(y); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	ds, err := OpenFiles([]string{path}, WithWorkers(1), WithReadWorkers(1))
	if err != nil {
		b.Fatal(err)
	}
	start := epoch.Add(time.Duration(n-64) * time.Second)
	end := epoch.Add(time.Duration(n) * time.Second)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := ds.ReadWindow(Selector{Name: "ai0", Group: "acquisition"}, start, end)
		if err != nil {
			b.Fatal(err)
		}
		if len(s.Y) != 64 || s.Y[63] != float64(n-1) {
			b.Fatalf("len %d tail %v", len(s.Y), s.Y)
		}
	}
}
