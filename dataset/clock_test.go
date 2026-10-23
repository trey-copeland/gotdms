package dataset

import (
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	tdms "github.com/trey-copeland/gotdms"
)

// sameSeries checks that a WithSharedClock series matches the default one:
// same values, same instants, bitwise-equal relative times.
func sameSeries(t *testing.T, what string, plain, shared Series) {
	t.Helper()
	if !sameFloats(shared.Y, plain.Y) || shared.Dt != plain.Dt || !reflect.DeepEqual(shared.Gaps, plain.Gaps) ||
		!reflect.DeepEqual(shared.Files, plain.Files) || shared.Timed != plain.Timed || shared.Source != plain.Source ||
		shared.IsTimestamp != plain.IsTimestamp || !shared.Basis.Equal(plain.Basis) || shared.Name != plain.Name || shared.Group != plain.Group {
		t.Fatalf("%s: shared %+v\nplain %+v", what, shared, plain)
	}
	if shared.Clock != nil {
		if shared.Times != nil {
			t.Fatalf("%s: Clock and Times both set", what)
		}
		if shared.Clock.Len() != len(plain.Times) {
			t.Fatalf("%s: clock len %d, times %d", what, shared.Clock.Len(), len(plain.Times))
		}
		for i, ts := range plain.Times {
			if shared.Clock.Ns[i] != ts.UnixNano() || !shared.Clock.Time(i).Equal(ts) {
				t.Fatalf("%s: sample %d: %d vs %s", what, i, shared.Clock.Ns[i], ts)
			}
		}
	} else if len(shared.Times) != len(plain.Times) {
		t.Fatalf("%s: times len %d vs %d", what, len(shared.Times), len(plain.Times))
	}
	abs := shared.AbsoluteTimes()
	if len(abs) != len(plain.Times) {
		t.Fatalf("%s: AbsoluteTimes len %d", what, len(abs))
	}
	for i := range abs {
		if !abs[i].Equal(plain.Times[i]) {
			t.Fatalf("%s: AbsoluteTimes[%d] = %s, want %s", what, i, abs[i], plain.Times[i])
		}
	}
	for _, rel := range [][2][]float64{
		{shared.RelativeTimes(), plain.RelativeTimes()},
		{shared.RelativeTimesFrom(epoch.Add(-1234567891 * time.Nanosecond)), plain.RelativeTimesFrom(epoch.Add(-1234567891 * time.Nanosecond))},
	} {
		if len(rel[0]) != len(rel[1]) {
			t.Fatalf("%s: relative len", what)
		}
		for i := range rel[0] {
			if math.Float64bits(rel[0][i]) != math.Float64bits(rel[1][i]) {
				t.Fatalf("%s: relative[%d] %v vs %v", what, i, rel[0][i], rel[1][i])
			}
		}
	}
}

func readBoth(t *testing.T, ds *Dataset, sels []Selector, start, end time.Time) (plain, shared []Series) {
	t.Helper()
	plain, err := ds.ReadMany(sels, start, end)
	if err != nil {
		t.Fatal(err)
	}
	shared, err = ds.ReadMany(sels, start, end, WithSharedClock())
	if err != nil {
		t.Fatal(err)
	}
	for i := range plain {
		sameSeries(t, sels[i].Name, plain[i], shared[i])
	}
	return plain, shared
}

func TestSharedClockTimestampChannel(t *testing.T) {
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
	for _, w := range [][2]time.Time{
		{epoch.Add(-time.Hour), epoch.Add(time.Hour)},
		{epoch.Add(time.Second), epoch.Add(2500 * time.Millisecond)},
	} {
		_, shared := readBoth(t, ds, sels, w[0], w[1])
		if shared[0].Clock == nil || shared[0].Clock != shared[1].Clock || shared[0].Clock != shared[2].Clock {
			t.Fatalf("clock not shared: %p %p %p", shared[0].Clock, shared[1].Clock, shared[2].Clock)
		}
	}
	// Read and ReadWindow take the option too.
	plain, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch.Add(time.Second), epoch.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	one, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch.Add(time.Second), epoch.Add(2*time.Second), WithSharedClock())
	if err != nil || one.Clock == nil {
		t.Fatalf("ReadWindow clock=%v err=%v", one.Clock, err)
	}
	sameSeries(t, "ReadWindow", plain, one)
	full, _ := ds.Read(Selector{Name: "ai1"})
	fullC, err := ds.Read(Selector{Name: "ai1"}, WithSharedClock(), WithReadBasis(epoch.Add(3*time.Second)))
	if err != nil || fullC.Clock == nil {
		t.Fatalf("Read clock=%v err=%v", fullC.Clock, err)
	}
	full.Basis = fullC.Basis
	sameSeries(t, "Read with basis", full, fullC)
}

func TestSharedClockWaveform(t *testing.T) {
	dir := t.TempDir()
	y := []float64{1, 2, 3, 4, 5, 6}
	writeSpecs(t, filepath.Join(dir, "a.tdms"), []spec{
		{group: "g", name: "c0", y: y, start: epoch, inc: 1e-4, wave: true},
		{group: "g", name: "c1", y: y, start: epoch, inc: 1e-4, wave: true},
		{group: "g", name: "slow", y: y, start: epoch, inc: 1e-3, wave: true},
		{group: "g", name: "back", y: y, start: epoch, inc: -0.25, wave: true},
		{group: "g", name: "flat", y: y, start: epoch, inc: 0, wave: true},
	})
	writeSpecs(t, filepath.Join(dir, "b.tdms"), []spec{
		{group: "h", name: "d0", y: y, start: epoch, inc: 1e-4, wave: true},
	})
	ds := openWave(t, dir)
	sels := []Selector{{Name: "c0"}, {Name: "c1"}, {Name: "slow"}, {Name: "back"}, {Name: "flat"}, {Name: "d0"}}
	_, shared := readBoth(t, ds, sels, epoch.Add(-time.Hour), epoch.Add(time.Hour))
	c := shared[0].Clock
	if c == nil || shared[1].Clock != c || shared[5].Clock != c {
		t.Fatal("equal waveform axes do not share a Clock")
	}
	if shared[2].Clock == c || shared[3].Clock == c || shared[4].Clock == c {
		t.Fatal("different waveform axes share a Clock")
	}
	// A window that cuts c0 and c1 at the same samples still shares.
	_, shared = readBoth(t, ds, sels[:2], epoch.Add(150*time.Microsecond), epoch.Add(450*time.Microsecond))
	if shared[0].Clock == nil || shared[0].Clock != shared[1].Clock || shared[0].Clock.Len() != 3 {
		t.Fatalf("windowed clock %+v %+v", shared[0].Clock, shared[1].Clock)
	}
	// A window with no samples is empty either way.
	_, shared = readBoth(t, ds, sels[:1], epoch.Add(time.Hour), epoch.Add(2*time.Hour))
	if shared[0].Clock != nil || len(shared[0].Y) != 0 {
		t.Fatalf("empty window %+v", shared[0])
	}
}

func TestSharedClockMergedFiles(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1}, []float64{0, 1})
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{5, 6}, []float64{5, 6})
	writeWave(t, filepath.Join(dir, "w1.tdms"), "wave", "w", epoch, 0.5, []float64{1, 2, 3})
	writeWave(t, filepath.Join(dir, "w2.tdms"), "wave", "w", epoch.Add(10*time.Second), 0.5, []float64{4, 5})
	ds := openAt(t, dir, WithTimestampChannel("wave", ""))
	sels := []Selector{{Name: "ai0"}, {Name: "w"}}
	plain, shared := readBoth(t, ds, sels, epoch.Add(-time.Hour), epoch.Add(time.Hour))
	for i := range shared {
		if shared[i].Clock == nil || len(plain[i].Gaps) != 1 || len(plain[i].Files) != 2 {
			t.Fatalf("%s: clock=%v gaps=%v files=%v", sels[i].Name, shared[i].Clock, plain[i].Gaps, plain[i].Files)
		}
	}
	// Resample on a Clock series matches resampling Times.
	for _, m := range []Interp{InterpLinear, InterpPrevious} {
		want, err := plain[0].Resample(epoch, epoch.Add(7*time.Second), 0.5, m)
		if err != nil {
			t.Fatal(err)
		}
		got, err := shared[0].Resample(epoch, epoch.Add(7*time.Second), 0.5, m)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resample %v: %+v vs %+v", m, got, want)
		}
	}
}

func TestSharedClockOutOfRangeKeepsTimes(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)
	times := []time.Time{old, old.Add(time.Second), old.Add(2 * time.Second)}
	writeSpecs(t, filepath.Join(dir, "old.tdms"), []spec{
		{group: "acq", name: "Time", dt: tdms.TypeTimestamp, y: times},
		{group: "acq", name: "ai0", y: []float64{1, 2, 3}},
		{group: "acq", name: "ai1", y: []float64{4, 5, 6}},
		{group: "w", name: "wv", y: []float64{1, 2}, start: old, inc: 1, wave: true},
	})
	ds, err := OpenGlob(dir, "*.tdms", WithTimestampChannelName("Time"), WithTimestampChannel("w", ""))
	if err != nil {
		t.Fatal(err)
	}
	sels := []Selector{{Name: "ai0"}, {Name: "ai1"}, {Name: "wv"}}
	_, shared := readBoth(t, ds, sels, old.Add(-time.Hour), old.Add(time.Hour))
	for i, s := range shared {
		if s.Clock != nil || len(s.Times) == 0 {
			t.Fatalf("%s: clock=%v times=%d", sels[i].Name, s.Clock, len(s.Times))
		}
	}
	if &shared[0].Times[0] == &shared[1].Times[0] {
		t.Fatal("fallback Times alias another series")
	}
}

func TestSharedClockUntimed(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "u.tdms"), []spec{{group: "acq", name: "raw", y: []float64{1, 2}}})
	ds := openWave(t, dir)
	s, err := ds.Read(Selector{Name: "raw"}, WithSharedClock())
	if err != nil || s.Timed || s.Clock != nil || s.Times != nil || s.AbsoluteTimes() != nil {
		t.Fatalf("untimed %+v %v", s, err)
	}
	if got := s.RelativeTimes(); !sameFloats(got, []float64{0, 1}) {
		t.Fatalf("indexes %v", got)
	}
	var nilClock *Clock
	if nilClock.Len() != 0 {
		t.Fatal("nil Clock Len")
	}
}
