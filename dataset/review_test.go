package dataset

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/trey-copeland/gotdms"
)

func TestSharedClockDecodedOnce(t *testing.T) {
	const n = 4096
	const nch = 8
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	w, err := tdms.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	sec := make([]float64, n)
	for i := range sec {
		sec[i] = float64(i) * 0.5
	}
	ch, err := w.Group("acquisition").Channel("Time", tdms.TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Write(sec); err != nil {
		t.Fatal(err)
	}
	for c := 0; c < nch; c++ {
		y := make([]float64, n)
		for i := range y {
			y[i] = float64(c)
		}
		ch, err := w.Group("acquisition").Channel(fmt.Sprintf("ai%d", c), tdms.TypeFloat64)
		if err != nil {
			t.Fatal(err)
		}
		if err := ch.Write(y); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	before := spanDeclaredCalls.Load()
	runtime.GC()
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)
	ds, err := OpenFiles([]string{path}, WithTimestampChannelName("Time"), WithNumericEpoch(epoch), WithWorkers(1))
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&m2)
	calls := spanDeclaredCalls.Load() - before
	if calls != 1 {
		t.Fatalf("spanDeclared calls = %d, want 1 for %d channels", calls, nch)
	}
	clock := ds.Find("Time")
	if len(clock) != 1 || !clock[0].IsTimestamp {
		t.Fatalf("clock %+v", clock)
	}
	for c := 0; c < nch; c++ {
		o := ds.Find(fmt.Sprintf("ai%d", c))
		if len(o) != 1 || !o[0].Start.Equal(clock[0].Start) || !o[0].Last.Equal(clock[0].Last) || o[0].Dt != clock[0].Dt {
			t.Fatalf("ai%d span start=%s last=%s dt=%v clock start=%s last=%s dt=%v",
				c, o[0].Start, o[0].Last, o[0].Dt, clock[0].Start, clock[0].Last, clock[0].Dt)
		}
	}
	// One full decode of the clock is two n*8 buffers. Eight of those is the
	// old per-channel walk. Catalog has to stay under a single decode. A
	// two-sample Cursor also fails this: it allocates the 256 KiB sequential
	// read buffer. The head read has to stay a short Slice.
	delta := m2.TotalAlloc - m1.TotalAlloc
	limit := uint64(n) * 8 * 2
	if delta >= limit {
		t.Fatalf("catalog allocated %d bytes, limit %d (shared clock decoded more than once?)", delta, limit)
	}
}

func TestUniformSpanMatchesScan(t *testing.T) {
	for _, dt := range []float64{1, 0.1, 1.0 / 3} {
		dt := dt
		t.Run(fmt.Sprint(dt), func(t *testing.T) {
			const n = 1000
			times := make([]float64, n)
			y := make([]float64, n)
			var acc timeAcc
			for i := 0; i < n; i++ {
				times[i] = float64(i) * dt
				y[i] = float64(i)
				tm, err := secondsToTime(epoch, times[i])
				if err != nil {
					t.Fatal(err)
				}
				acc.add(tm)
			}
			dir := t.TempDir()
			writePair(t, filepath.Join(dir, "run.tdms"), "acq", "ai0", times, y)
			ds := openAt(t, dir)
			occ := ds.Find("ai0")[0]
			if !occ.Start.Equal(acc.start) || !occ.Last.Equal(acc.last) || occ.Dt != acc.dt || !acc.hasDt {
				t.Fatalf("catalog start=%s last=%s dt=%v oracle start=%s last=%s dt=%v has=%v",
					occ.Start, occ.Last, occ.Dt, acc.start, acc.last, acc.dt, acc.hasDt)
			}
			s, err := ds.Read(Selector{Name: "ai0"})
			if err != nil {
				t.Fatal(err)
			}
			if s.Dt != occ.Dt || !s.Times[0].Equal(occ.Start) || !s.Times[len(s.Times)-1].Equal(occ.Last) {
				t.Fatalf("series dt=%v start=%s last=%s", s.Dt, s.Times[0], s.Times[len(s.Times)-1])
			}
		})
	}
}

func TestProvisionalIrregularSpan(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "run.tdms"), "acq", "ai0", []float64{0, 1, 100}, []float64{1, 2, 3})
	ds := openAt(t, dir)
	occ := ds.Find("ai0")[0]
	if !occ.Start.Equal(epoch) || !occ.Last.Equal(epoch.Add(2*time.Second)) || occ.Dt != 1 {
		t.Fatalf("provisional span start=%s last=%s dt=%v", occ.Start, occ.Last, occ.Dt)
	}
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 2, 3}) || s.Dt != 0 {
		t.Fatalf("read Y=%v dt=%v", s.Y, s.Dt)
	}
	// The file is opened because the window meets the provisional span.
	// Samples outside the window are dropped from the real axis, not from the guess.
	mid, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch, epoch.Add(1500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(mid.Y, []float64{1, 2}) {
		t.Fatalf("mid Y=%v", mid.Y)
	}
	// A window past the extrapolated end does not open the file.
	miss, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch.Add(50*time.Second), epoch.Add(150*time.Second))
	if err != nil || len(miss.Y) != 0 {
		t.Fatalf("miss Y=%v err=%v", miss.Y, err)
	}
}

func TestNonPositiveFirstStepScans(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "flat.tdms"), "acq", "flat", []float64{0, 0, 5}, []float64{1, 2, 3})
	writePair(t, filepath.Join(dir, "back.tdms"), "acq", "back", []float64{5, 0, 1}, []float64{1, 2, 3})
	ds := openAt(t, dir)
	flat := ds.Find("flat")[0]
	if !flat.Start.Equal(epoch) || !flat.Last.Equal(epoch.Add(5*time.Second)) || flat.Dt != 0 {
		t.Fatalf("flat span start=%s last=%s dt=%v", flat.Start, flat.Last, flat.Dt)
	}
	s, err := ds.Read(Selector{Name: "flat"})
	if err != nil || !sameFloats(s.Y, []float64{1, 2, 3}) || s.Dt != 0 {
		t.Fatalf("flat read Y=%v dt=%v err=%v", s.Y, s.Dt, err)
	}
	back := ds.Find("back")[0]
	if !back.Start.Equal(epoch) || !back.Last.Equal(epoch.Add(5*time.Second)) || back.Dt != 0 {
		t.Fatalf("back span start=%s last=%s dt=%v", back.Start, back.Last, back.Dt)
	}
	if _, err := ds.Read(Selector{Name: "back"}); !errors.Is(err, ErrMonotonic) {
		t.Fatalf("back err = %v", err)
	}
}

func TestReadWorkersSeveralFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 6; i++ {
		base := float64(i * 10)
		writeSpecs(t, filepath.Join(dir, fmt.Sprintf("f%d.tdms", i)), []spec{
			{group: "acq", name: "Time", y: []float64{base, base + 1}},
			{group: "acq", name: "ai0", y: []float64{base, base + 1}},
			{group: "acq", name: "ai1", y: []float64{base + 100, base + 101}},
		})
	}
	ds := openAt(t, dir, WithReadWorkers(2))
	s, err := ds.Read(Selector{Name: "ai0", Group: "acq"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Y) != 12 || s.Y[0] != 0 || s.Y[11] != 51 {
		t.Fatalf("merged Y=%v", s.Y)
	}
	out, err := ds.ReadMany([]Selector{{Name: "ai0", Group: "acq"}, {Name: "ai1", Group: "acq"}}, epoch, epoch.Add(100*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || len(out[0].Y) != 12 || len(out[1].Y) != 12 {
		t.Fatalf("lens %d %d", len(out[0].Y), len(out[1].Y))
	}
	out[0].Times[0] = out[0].Times[0].Add(time.Hour)
	out[0].Y[0] = -1
	if out[1].Y[0] == -1 || out[1].Times[0].Equal(out[0].Times[0]) {
		t.Fatal("ReadMany series alias across channels")
	}
}

func TestReadManyClosesOnOpenError(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 4)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("f%d.tdms", i))
		base := float64(i * 10)
		writeSpecs(t, paths[i], []spec{
			{group: "acq", name: "Time", y: []float64{base, base + 1}},
			{group: "acq", name: "ai0", y: []float64{base, base + 1}},
			{group: "acq", name: "ai1", y: []float64{base, base + 1}},
		})
	}
	ds := openAt(t, dir, WithReadWorkers(2))
	if err := os.WriteFile(paths[2], []byte("not a tdms file"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := countFDs(t)
	_, err := ds.ReadMany([]Selector{{Name: "ai0", Group: "acq"}, {Name: "ai1", Group: "acq"}}, epoch, epoch.Add(100*time.Second))
	if err == nil {
		t.Fatal("expected open error")
	}
	if after := countFDs(t); after > before {
		t.Fatalf("fds before %d after %d", before, after)
	}
	before = countFDs(t)
	if _, err := ds.Read(Selector{Name: "ai0", Group: "acq"}); err == nil {
		t.Fatal("expected read error")
	}
	if after := countFDs(t); after > before {
		t.Fatalf("read fds before %d after %d", before, after)
	}
}

func TestSeriesOwnsBuffers(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "run.tdms"), "acq", "ai0", []float64{0, 1, 2, 3}, []float64{1, 2, 3, 4})
	ds := openAt(t, dir)
	s1, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	y := append([]float64(nil), s1.Y...)
	ts := append([]time.Time(nil), s1.Times...)
	s2, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s1.Y, y) || !sameTimes(s1.Times, ts) {
		t.Fatal("second read mutated the first series")
	}
	s2.Y[0] = -123
	s2.Times[0] = s2.Times[0].Add(time.Hour)
	if !sameFloats(s1.Y, y) || !sameTimes(s1.Times, ts) {
		t.Fatal("timestamp series alias")
	}

	wave := filepath.Join(dir, "wave.tdms")
	writeWave(t, wave, "acq", "w", epoch, 1, []float64{10, 11, 12, 13, 14})
	wds := openWave(t, dir)
	// Only the waveform file is timed under openWave; the timestamp file has no wf_*.
	// Read the waveform channel by name. It is unambiguous.
	ws1, err := wds.ReadWindow(Selector{Name: "w"}, epoch.Add(3*time.Second), epoch.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(ws1.Y, []float64{13, 14}) {
		t.Fatalf("wave Y=%v", ws1.Y)
	}
	wy := append([]float64(nil), ws1.Y...)
	wt := append([]time.Time(nil), ws1.Times...)
	ws2, err := wds.ReadWindow(Selector{Name: "w"}, epoch.Add(3*time.Second), epoch.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(ws1.Y, wy) || !sameTimes(ws1.Times, wt) {
		t.Fatal("second waveform read mutated the first series")
	}
	ws2.Y[0] = -1
	ws2.Times[0] = ws2.Times[0].Add(time.Hour)
	if !sameFloats(ws1.Y, wy) || !sameTimes(ws1.Times, wt) {
		t.Fatal("waveform series aliases a cursor batch")
	}
}

func TestZeroLengthAndEmptyWindows(t *testing.T) {
	// The writer omits a raw index when a channel has no samples, so a
	// declared numeric clock cannot be typed and empty. A waveform channel
	// still carries wf_start_time with Len 0, which is the empty window path.
	dir := t.TempDir()
	writeWave(t, filepath.Join(dir, "empty-wave.tdms"), "acq", "flat", epoch, 1, nil)
	if hitsWindow(Occurrence{Timed: true, Samples: 0, Start: epoch, Last: epoch.Add(time.Second)}, epoch, epoch.Add(time.Hour)) {
		t.Fatal("zero-length occurrence must not be opened for a window")
	}
	wds := openWave(t, dir)
	occ := wds.Find("flat")
	if len(occ) != 1 || occ[0].Samples != 0 || !occ[0].Timed || occ[0].Source != TimeWaveform {
		t.Fatalf("empty wave occ %+v", occ)
	}
	w, err := wds.ReadWindow(Selector{Name: "flat"}, epoch.Add(-time.Hour), epoch.Add(-time.Minute))
	if err != nil || len(w.Y) != 0 || !w.Timed {
		t.Fatalf("wave before Y=%v timed=%v err=%v", w.Y, w.Timed, err)
	}
	w, err = wds.ReadWindow(Selector{Name: "flat"}, epoch, epoch.Add(time.Second))
	if err != nil || len(w.Y) != 0 || !w.Timed {
		t.Fatalf("wave on span Y=%v timed=%v err=%v", w.Y, w.Timed, err)
	}
	w, err = wds.ReadWindow(Selector{Name: "flat"}, epoch.Add(time.Hour), epoch.Add(2*time.Hour))
	if err != nil || len(w.Y) != 0 {
		t.Fatalf("wave after Y=%v err=%v", w.Y, err)
	}
	w, err = wds.Read(Selector{Name: "flat"})
	if err != nil || len(w.Y) != 0 || !w.Timed {
		t.Fatalf("wave empty read Y=%v err=%v", w.Y, err)
	}
}

func TestWaveformIndexWide(t *testing.T) {
	for _, n := range []int64{int64(1)<<32 + 10, int64(1)<<33 + 7} {
		inc := 1.0
		if n > 1<<32 {
			inc = 1e-3
		}
		target, err := waveformAt(epoch, inc, n-5)
		if err != nil {
			t.Fatal(err)
		}
		if got := firstAtOrAfter(epoch, inc, n, target); got != n-5 {
			t.Fatalf("n=%d i0=%d", n, got)
		}
		past, err := waveformAt(epoch, inc, n-1)
		if err != nil {
			t.Fatal(err)
		}
		if got := firstAtOrAfter(epoch, inc, n, past.Add(time.Second)); got != n {
			t.Fatalf("n=%d after=%d", n, got)
		}
		if got := firstAtOrAfter(epoch, inc, n, epoch.Add(-time.Second)); got != 0 {
			t.Fatalf("n=%d before=%d", n, got)
		}
		i0, i1, err := waveformBounds(epoch, inc, n, epoch.Add(-2*time.Second), epoch.Add(-time.Second))
		if err != nil || i0 != 0 || i1 != 0 {
			t.Fatalf("n=%d bounds %d %d err=%v", n, i0, i1, err)
		}
		// The index at the end of a long channel must not wrap through int32.
		i0, i1, err = waveformBounds(epoch, inc, n, target, past.Add(time.Nanosecond))
		if err != nil || i0 != n-5 || i1 != n {
			t.Fatalf("n=%d tail bounds %d %d err=%v", n, i0, i1, err)
		}
	}
	if _, err := indexToInt(int64(math.MaxInt32) + 1); err != nil && int64(int(int64(math.MaxInt32)+1)) == int64(math.MaxInt32)+1 {
		t.Fatal(err)
	}
}

func TestResampleStepAndAllocs(t *testing.T) {
	s := Series{
		Timed: true,
		Files: []string{"a.tdms"},
		Basis: epoch,
		Times: []time.Time{epoch, epoch.Add(time.Second)},
		Y:     []float64{0, 1000},
	}
	tiny := epoch.Add(time.Nanosecond)
	if _, err := s.Resample(epoch, tiny, 1e-10, InterpLinear); !errors.Is(err, ErrArgument) {
		t.Fatalf("tiny window err = %v", err)
	}
	if _, err := s.Resample(epoch, epoch.Add(time.Hour), 1e-10, InterpLinear); !errors.Is(err, ErrArgument) {
		t.Fatalf("large window err = %v", err)
	}
	if _, err := s.Resample(epoch, tiny, 0.4e-9, InterpLinear); !errors.Is(err, ErrArgument) {
		t.Fatalf("0.4ns err = %v", err)
	}
	if _, err := s.Resample(epoch, epoch.Add(time.Second), -1e-9, InterpLinear); !errors.Is(err, ErrArgument) {
		t.Fatalf("negative err = %v", err)
	}

	one, err := s.Resample(epoch, epoch.Add(5*time.Nanosecond), 1e-9, InterpLinear)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Y) != 5 || !one.Times[0].Equal(epoch) || !one.Times[4].Equal(epoch.Add(4*time.Nanosecond)) {
		t.Fatalf("1ns times=%v Y=%v", one.Times, one.Y)
	}
	above, err := s.Resample(epoch, epoch.Add(5*time.Nanosecond), 1.1e-9, InterpLinear)
	if err != nil {
		t.Fatal(err)
	}
	if len(above.Y) != 5 || !above.Times[1].Equal(epoch.Add(time.Nanosecond)) {
		t.Fatalf("1.1ns times=%v", above.Times)
	}
	// 0.5ns rounds away from zero to 1ns and must advance.
	half, err := s.Resample(epoch, epoch.Add(3*time.Nanosecond), 0.5e-9, InterpPrevious)
	if err != nil || len(half.Y) != 3 {
		t.Fatalf("0.5ns len=%d err=%v", len(half.Y), err)
	}

	allocs := testing.AllocsPerRun(200, func() {
		out, err := s.Resample(epoch, epoch.Add(3*time.Second), 1, InterpLinear)
		if err != nil || len(out.Y) != 2 || len(out.Times) != 2 {
			panic(err)
		}
	})
	// Files copy, Y, and Times. Nothing else.
	if allocs != 3 {
		t.Fatalf("allocs = %v, want 3", allocs)
	}
}

func countFDs(t *testing.T) int {
	t.Helper()
	f, err := os.Open("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

func sameTimes(got, want []time.Time) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !got[i].Equal(want[i]) {
			return false
		}
	}
	return true
}
