package dataset

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	tdms "github.com/trey-copeland/gotdms"
)

// sameValues checks that a WithoutTimes series matches the default one in
// everything but the time axis, and that it carries no time axis.
func sameValues(t *testing.T, what string, plain, vals Series) {
	t.Helper()
	if vals.Times != nil || vals.Clock != nil {
		t.Fatalf("%s: WithoutTimes kept a time axis: %d times, clock %v", what, len(vals.Times), vals.Clock)
	}
	if len(vals.Y) != len(plain.Y) {
		t.Fatalf("%s: len %d vs %d", what, len(vals.Y), len(plain.Y))
	}
	for i := range vals.Y {
		if math.Float64bits(vals.Y[i]) != math.Float64bits(plain.Y[i]) {
			t.Fatalf("%s: Y[%d] %v vs %v", what, i, vals.Y[i], plain.Y[i])
		}
	}
	if vals.Dt != plain.Dt || !reflect.DeepEqual(vals.Gaps, plain.Gaps) || !reflect.DeepEqual(vals.Files, plain.Files) ||
		vals.Timed != plain.Timed || vals.Source != plain.Source || vals.IsTimestamp != plain.IsTimestamp ||
		!vals.Basis.Equal(plain.Basis) || vals.Name != plain.Name || vals.Group != plain.Group {
		t.Fatalf("%s: values-only %+v\nplain %+v", what, vals, plain)
	}
	if got := vals.AbsoluteTimes(); len(got) != 0 {
		t.Fatalf("%s: AbsoluteTimes returned %d times", what, len(got))
	}
}

// readValues reads sels with the default options and with WithoutTimes
// (alone and with WithSharedClock, which it overrides) and compares them.
func readValues(t *testing.T, ds *Dataset, sels []Selector, start, end time.Time) []Series {
	t.Helper()
	plain, err := ds.ReadMany(sels, start, end)
	if err != nil {
		t.Fatal(err)
	}
	var vals []Series
	for _, opts := range [][]ReadOption{{WithoutTimes()}, {WithSharedClock(), WithoutTimes()}, {WithoutTimes(), WithSharedClock()}} {
		vals, err = ds.ReadMany(sels, start, end, opts...)
		if err != nil {
			t.Fatal(err)
		}
		for i := range plain {
			sameValues(t, sels[i].Name, plain[i], vals[i])
		}
	}
	for i, sel := range sels {
		// Read and ReadWindow take the option too.
		one, err := ds.ReadWindow(sel, start, end)
		if err != nil {
			t.Fatal(err)
		}
		v, err := ds.ReadWindow(sel, start, end, WithoutTimes())
		if err != nil {
			t.Fatal(err)
		}
		sameValues(t, "ReadWindow "+sel.Name, one, v)
		sameValues(t, "ReadWindow vs ReadMany "+sel.Name, one, vals[i])
		all, err := ds.Read(sel, WithReadBasis(epoch.Add(3*time.Second)))
		if err != nil {
			t.Fatal(err)
		}
		v, err = ds.Read(sel, WithoutTimes(), WithReadBasis(epoch.Add(3*time.Second)))
		if err != nil {
			t.Fatal(err)
		}
		sameValues(t, "Read "+sel.Name, all, v)
	}
	return vals
}

var wide = [2]time.Time{epoch.Add(-time.Hour), epoch.Add(time.Hour)}

func TestWithoutTimesTimestampChannel(t *testing.T) {
	dir := t.TempDir()
	n := 50
	sec, a, b := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := range sec {
		sec[i] = float64(i) * 0.1
		a[i], b[i] = float64(i), float64(-i)
	}
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "acq", name: "Time", y: sec},
		{group: "acq", name: "ai0", y: a},
		{group: "acq", name: "ai1", y: b},
	})
	ds := openAt(t, dir)
	sels := []Selector{{Name: "ai0"}, {Name: "ai1"}, {Name: "Time"}}
	for _, w := range [][2]time.Time{wide, {epoch.Add(time.Second), epoch.Add(2500 * time.Millisecond)}, {epoch.Add(time.Hour), epoch.Add(2 * time.Hour)}} {
		readValues(t, ds, sels, w[0], w[1])
	}
	// A timestamp channel of TDMS timestamps, read as its own series.
	times := make([]time.Time, n)
	for i := range times {
		times[i] = epoch.Add(time.Duration(i) * 7 * time.Millisecond)
	}
	dir2 := t.TempDir()
	writeSpecs(t, filepath.Join(dir2, "ts.tdms"), []spec{
		{group: "acq", name: "Time", dt: tdms.TypeTimestamp, y: times},
		{group: "acq", name: "ai0", y: a},
	})
	ds2, err := OpenGlob(dir2, "*.tdms", WithTimestampChannelName("Time"))
	if err != nil {
		t.Fatal(err)
	}
	readValues(t, ds2, []Selector{{Name: "ai0"}, {Name: "Time"}}, epoch.Add(20*time.Millisecond), epoch.Add(200*time.Millisecond))
}

func TestWithoutTimesWaveform(t *testing.T) {
	dir := t.TempDir()
	y := []float64{1, 2, 3, 4, 5, 6}
	writeSpecs(t, filepath.Join(dir, "a.tdms"), []spec{
		{group: "g", name: "c0", y: y, start: epoch, inc: 1e-4, wave: true},
		{group: "g", name: "slow", y: y, start: epoch, inc: 1e-3, wave: true},
		{group: "g", name: "back", y: y, start: epoch, inc: -0.25, wave: true},
		{group: "g", name: "flat", y: y, start: epoch, inc: 0, wave: true},
	})
	ds := openWave(t, dir)
	sels := []Selector{{Name: "c0"}, {Name: "slow"}, {Name: "back"}, {Name: "flat"}}
	for _, w := range [][2]time.Time{wide, {epoch.Add(150 * time.Microsecond), epoch.Add(450 * time.Microsecond)}, {epoch.Add(time.Hour), epoch.Add(2 * time.Hour)}} {
		readValues(t, ds, sels, w[0], w[1])
	}
}

func TestWithoutTimesUntimed(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "a.tdms"), []spec{{group: "g", name: "u", y: []float64{3, 1, 2}}})
	writeSpecs(t, filepath.Join(dir, "b.tdms"), []spec{{group: "g", name: "u", y: []float64{9}}})
	ds := openWave(t, dir)
	plain, err := ds.Read(Selector{Name: "u"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := ds.Read(Selector{Name: "u"}, WithoutTimes())
	if err != nil {
		t.Fatal(err)
	}
	sameValues(t, "untimed", plain, v)
}

// Merges across files: gaps, Dt, and every overlap policy match the default
// read, including the overlap error.
func TestWithoutTimesMergedFiles(t *testing.T) {
	for _, policy := range []OverlapPolicy{OverlapError, OverlapKeepFirst, OverlapKeepLast} {
		for _, overlap := range []bool{false, true} {
			t.Run(fmt.Sprintf("policy%d/overlap=%v", policy, overlap), func(t *testing.T) {
				dir := t.TempDir()
				shift := 10.0
				if overlap {
					shift = 1.0
				}
				writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 0.5, 1, 1.5}, []float64{0, 1, 2, 3})
				writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{shift, shift + 0.5, shift + 1}, []float64{5, 6, 7})
				writeWave(t, filepath.Join(dir, "w1.tdms"), "wave", "w", epoch, 0.5, []float64{1, 2, 3, 4})
				writeWave(t, filepath.Join(dir, "w2.tdms"), "wave", "w", epoch.Add(time.Duration(shift*float64(time.Second))), 0.5, []float64{5, 6, 7})
				writeWave(t, filepath.Join(dir, "w3.tdms"), "wave", "w", epoch.Add(20*time.Second), 0.25, []float64{8, 9})
				ds := openAt(t, dir, WithTimestampChannel("wave", ""), WithOverlap(policy))
				sels := []Selector{{Name: "ai0"}, {Name: "w"}}
				plain, perr := ds.ReadMany(sels, wide[0], wide[1])
				vals, verr := ds.ReadMany(sels, wide[0], wide[1], WithoutTimes())
				if (perr == nil) != (verr == nil) || (perr != nil && perr.Error() != verr.Error()) {
					t.Fatalf("errors differ: %v vs %v", perr, verr)
				}
				if perr != nil {
					if !errors.Is(verr, ErrOverlap) {
						t.Fatalf("want ErrOverlap, got %v", verr)
					}
					return
				}
				for i := range plain {
					sameValues(t, sels[i].Name, plain[i], vals[i])
					if len(plain[i].Files) < 2 {
						t.Fatalf("%s: not merged: %v", sels[i].Name, plain[i].Files)
					}
				}
				if !overlap && len(plain[1].Gaps) != 2 {
					t.Fatalf("gaps %v", plain[1].Gaps)
				}
				for _, sel := range sels {
					one, err := ds.Read(sel)
					if err != nil {
						t.Fatal(err)
					}
					v, err := ds.Read(sel, WithoutTimes())
					if err != nil {
						t.Fatal(err)
					}
					sameValues(t, "Read "+sel.Name, one, v)
				}
			})
		}
	}
}

func TestWithoutTimesResample(t *testing.T) {
	dir := t.TempDir()
	writeWave(t, filepath.Join(dir, "a.tdms"), "g", "w", epoch, 0.5, []float64{1, 2, 3})
	ds := openWave(t, dir)
	v, err := ds.Read(Selector{Name: "w"}, WithoutTimes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Resample(epoch, epoch.Add(time.Second), 0.25, InterpLinear); !errors.Is(err, ErrArgument) {
		t.Fatalf("Resample of a values-only series: %v", err)
	}
	if rel := v.RelativeTimes(); rel != nil {
		t.Fatalf("RelativeTimes returned %d values", len(rel))
	}
}

// waveFixture writes nch float64 waveform channels of n samples on one axis
// and the same values as untimed channels in a second file.
func waveFixture(tb testing.TB, n, nch int) (timed, untimed *Dataset, sels, usels []Selector) {
	tb.Helper()
	dir := tb.TempDir()
	for _, timed := range []bool{true, false} {
		name := "u.tdms"
		if timed {
			name = "w.tdms"
		}
		w, err := tdms.Create(filepath.Join(dir, name))
		if err != nil {
			tb.Fatal(err)
		}
		for c := 0; c < nch; c++ {
			y := make([]float64, n)
			for i := range y {
				y[i] = float64(c*n + i)
			}
			grp := "u"
			if timed {
				grp = "w"
			}
			ch, err := w.Group(grp).Channel(fmt.Sprintf("c%d", c), tdms.TypeFloat64)
			if err != nil {
				tb.Fatal(err)
			}
			if timed {
				if err := ch.SetProperty(tdms.PropStartTime, epoch); err != nil {
					tb.Fatal(err)
				}
				if err := ch.SetProperty(tdms.PropIncrement, 1e-4); err != nil {
					tb.Fatal(err)
				}
			}
			if err := ch.Write(y); err != nil {
				tb.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			tb.Fatal(err)
		}
	}
	ds, err := OpenGlob(dir, "*.tdms", WithReadWorkers(2))
	if err != nil {
		tb.Fatal(err)
	}
	for c := 0; c < nch; c++ {
		sels = append(sels, Selector{Name: fmt.Sprintf("c%d", c), Group: "w"})
		usels = append(usels, Selector{Name: fmt.Sprintf("c%d", c), Group: "u"})
	}
	return ds, ds, sels, usels
}

// allocBytes is the heap bytes f allocates, the minimum of three runs.
func allocBytes(f func()) uint64 {
	best := uint64(math.MaxUint64)
	for k := 0; k < 3; k++ {
		runtime.GC()
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		f()
		runtime.ReadMemStats(&b)
		if d := b.TotalAlloc - a.TotalAlloc; d < best {
			best = d
		}
	}
	return best
}

// A values-only read of timed waveforms allocates what reading the same
// samples from untimed channels allocates: no []time.Time, no Clock.
func TestWithoutTimesAllocatesNoTimeAxis(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation test")
	}
	const n, nch = 1 << 18, 4
	ds, _, sels, usels := waveFixture(t, n, nch)
	readMany := func(sels []Selector, opts ...ReadOption) func() {
		return func() {
			out, err := ds.ReadMany(sels, wide[0], wide[1].Add(1000*time.Hour), opts...)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != nch || len(out[0].Y) != n {
				t.Fatalf("read %d series", len(out))
			}
		}
	}
	readAll := func(sels []Selector, opts ...ReadOption) func() {
		return func() {
			for _, sel := range sels {
				s, err := ds.Read(sel, opts...)
				if err != nil || len(s.Y) != n {
					t.Fatalf("read %v: %d, %v", sel, len(s.Y), err)
				}
			}
		}
	}
	untimed := allocBytes(readAll(usels))
	for _, c := range []struct {
		what  string
		plain func()
		share func()
		vals  func()
	}{
		{"ReadMany", readMany(sels), readMany(sels, WithSharedClock()), readMany(sels, WithoutTimes())},
		{"Read", readAll(sels), readAll(sels, WithSharedClock()), readAll(sels, WithoutTimes())},
	} {
		plain, shared, vals := allocBytes(c.plain), allocBytes(c.share), allocBytes(c.vals)
		t.Logf("%s: default %d B, WithSharedClock %d B, WithoutTimes %d B, untimed Read %d B", c.what, plain, shared, vals, untimed)
		// The default holds 24 bytes per sample per channel of Times, the
		// shared clock 8 bytes per sample once.
		if plain-vals < 24*n*nch*95/100 || shared-vals < 8*n*95/100 {
			t.Fatalf("%s: WithoutTimes saves too little: default %d, shared %d, values-only %d", c.what, plain, shared, vals)
		}
		// Anything left over is far below one int64 per sample.
		if vals > untimed+n {
			t.Fatalf("%s: WithoutTimes allocates %d B, untimed read %d B", c.what, vals, untimed)
		}
	}
}

// randomPieces builds 2..5 timed pieces from rng: some lazy waveforms, some
// with Times, with overlaps, gaps, equal times, and differing dt.
func randomPieces(rng *rand.Rand) (lazy, eager []piece) {
	k := 2 + rng.Intn(4)
	untimed := rng.Intn(8) == 0
	for f := 0; f < k; f++ {
		n := rng.Intn(9) // an empty piece is skipped by the merge
		start := epoch.Add(time.Duration(rng.Intn(40)) * 250 * time.Millisecond)
		incs := []float64{0.25, 0.5, 0.25, 1, 0, -0.25}
		inc := incs[rng.Intn(len(incs))]
		y := make([]float64, n)
		for i := range y {
			y[i] = float64(f*100 + i)
		}
		file := fmt.Sprintf("f%d", f)
		if rng.Intn(3) == 0 {
			// Same file name as the previous piece: samples stay in order.
			file = fmt.Sprintf("f%d", max(f-1, 0))
		}
		var p piece
		if untimed {
			p = piece{file: file, y: y}
		} else if rng.Intn(2) == 0 {
			p = piece{file: file, dt: inc, timed: true, y: y, start: start, waveT0: start,
				wave: &waveKey{sec: start.Unix(), nsec: start.Nanosecond(), inc: math.Float64bits(inc), n: int64(n)}}
		} else {
			times := make([]time.Time, n)
			t := start
			for i := range times {
				times[i] = t
				t = t.Add(time.Duration(rng.Intn(3)) * 250 * time.Millisecond)
			}
			dt := 0.0
			if d, ok := uniformDt(times); ok {
				dt = d
			}
			p = piece{file: file, dt: dt, timed: true, y: y, times: times, start: start}
		}
		lazy = append(lazy, p)
		e := p
		if e.wave != nil {
			e.times = pieceTimes(&e)
			e.wave = nil
		}
		eager = append(eager, e)
	}
	order := func(ps []piece) {
		sort.SliceStable(ps, func(i, j int) bool {
			if !ps[i].start.Equal(ps[j].start) {
				return ps[i].start.Before(ps[j].start)
			}
			return ps[i].file < ps[j].file
		})
	}
	order(lazy)
	order(eager)
	return lazy, eager
}

func checkMerge(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	lazy, eager := randomPieces(rng)
	for _, policy := range []OverlapPolicy{OverlapError, OverlapKeepFirst, OverlapKeepLast} {
		ref, rerr := refMergePieces(clonePieces(eager), policy)
		got, gerr := mergePieces(clonePieces(eager), policy, true)
		lt, lerr := mergePieces(clonePieces(lazy), policy, true)
		vals, verr := mergePieces(clonePieces(lazy), policy, false)
		if fmt.Sprint(rerr) != fmt.Sprint(gerr) || fmt.Sprint(rerr) != fmt.Sprint(verr) || fmt.Sprint(rerr) != fmt.Sprint(lerr) {
			t.Fatalf("seed %d policy %d: errors %v / %v / %v / %v", seed, policy, rerr, gerr, lerr, verr)
		}
		if rerr != nil {
			continue
		}
		if !reflect.DeepEqual(ref, got) {
			t.Fatalf("seed %d policy %d: merge\n got %+v\nwant %+v", seed, policy, got, ref)
		}
		// Lazy waveform pieces with times wanted build the same axis.
		if !reflect.DeepEqual(ref, lt) {
			t.Fatalf("seed %d policy %d: lazy merge with times\n got %+v\nwant %+v", seed, policy, lt, ref)
		}
		ref.Times = nil
		if !reflect.DeepEqual(ref, vals) {
			t.Fatalf("seed %d policy %d: values-only merge\n got %+v\nwant %+v", seed, policy, vals, ref)
		}
	}
}

// Mixed timed and untimed pieces are an argument error, not a panic.
func TestMergeRejectsMixedTimedPieces(t *testing.T) {
	timed := piece{file: "a", timed: true, y: []float64{1, 2}, times: []time.Time{epoch, epoch.Add(time.Second)}, start: epoch}
	plain := piece{file: "b", y: []float64{3}}
	for _, ps := range [][]piece{{timed, plain}, {plain, timed}} {
		for _, policy := range []OverlapPolicy{OverlapError, OverlapKeepFirst, OverlapKeepLast} {
			for _, want := range []bool{true, false} {
				if _, err := mergePieces(clonePieces(ps), policy, want); !errors.Is(err, ErrArgument) {
					t.Fatalf("mixed pieces: %v", err)
				}
			}
		}
	}
}

func clonePieces(ps []piece) []piece {
	out := make([]piece, len(ps))
	for i, p := range ps {
		p.y = append([]float64(nil), p.y...)
		if p.times != nil {
			p.times = append([]time.Time(nil), p.times...)
		}
		out[i] = p
	}
	return out
}

// The run-based merge matches the per-sample merge it replaced, for eager
// pieces and, without times, for lazy waveform pieces.
func TestMergeRunsMatchesReference(t *testing.T) {
	for seed := int64(0); seed < 3000; seed++ {
		checkMerge(t, seed)
	}
}

func FuzzMergeRuns(f *testing.F) {
	for _, s := range []int64{0, 1, 42, 1 << 40} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed int64) { checkMerge(t, seed) })
}

// BenchmarkReadManyTimeModes reads 4 waveform channels of 1<<20 samples on
// one axis with the default Times, WithSharedClock, and WithoutTimes.
func BenchmarkReadManyTimeModes(b *testing.B) {
	const n, nch = 1 << 20, 4
	ds, _, sels, _ := waveFixture(b, n, nch)
	for _, m := range []struct {
		name string
		opts []ReadOption
	}{{"times", nil}, {"shared", []ReadOption{WithSharedClock()}}, {"none", []ReadOption{WithoutTimes()}}} {
		b.Run(m.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				out, err := ds.ReadMany(sels, wide[0], wide[1].Add(1000*time.Hour), m.opts...)
				if err != nil || len(out[0].Y) != n {
					b.Fatal(err)
				}
			}
		})
	}
}
