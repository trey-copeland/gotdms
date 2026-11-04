package dataset

import (
	"path/filepath"
	"testing"
	"time"
)

// secs returns times as whole seconds after epoch.
func secs(ts []time.Time) []float64 {
	out := make([]float64, len(ts))
	for i, x := range ts {
		out[i] = x.Sub(epoch).Seconds()
	}
	return out
}

// A descending waveform split across overlapping files follows each overlap
// policy the way its ascending mirror does. OverlapKeepLast used to compare
// times as if ascending: it dropped nothing, so Times came out 10 9 8 7 8 7.
func TestDescendingWaveformOverlapPolicies(t *testing.T) {
	cases := []struct {
		policy OverlapPolicy
		y, ts  []float64
	}{
		{OverlapKeepFirst, []float64{10, 9, 8, 7, 6, 5}, []float64{10, 9, 8, 7, 6, 5}},
		{OverlapKeepLast, []float64{10, 9, 80, 70, 6, 5}, []float64{10, 9, 8, 7, 6, 5}},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeWave(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", epoch.Add(10*time.Second), -1, []float64{10, 9, 8, 7})
		writeWave(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", epoch.Add(8*time.Second), -1, []float64{80, 70, 6, 5})
		ds := openWave(t, dir, WithOverlap(c.policy))
		s, err := ds.Read(Selector{Name: "ai0"})
		if err != nil {
			t.Fatalf("policy %d: %v", c.policy, err)
		}
		if !sameFloats(s.Y, c.y) || !sameFloats(secs(s.Times), c.ts) || s.Dt != -1 || len(s.Gaps) != 0 {
			t.Fatalf("policy %d: Y=%v Times=%v Dt=%v gaps=%v", c.policy, s.Y, secs(s.Times), s.Dt, s.Gaps)
		}
		bare, err := ds.ReadMany([]Selector{{Name: "ai0"}}, epoch, epoch.Add(time.Minute), WithoutTimes())
		if err != nil || !sameFloats(bare[0].Y, c.y) {
			t.Fatalf("policy %d without times: %+v err=%v", c.policy, bare, err)
		}
	}
}

// OverlapKeepLast keeps a gap recorded before the overlap on a descending
// axis. trimGaps used to drop every gap whose To was after the last kept
// sample, which on a descending axis is every gap.
func TestDescendingKeepLastKeepsEarlierGap(t *testing.T) {
	dir := t.TempDir()
	writeWave(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", epoch.Add(20*time.Second), -1, []float64{20, 19})
	writeWave(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", epoch.Add(15*time.Second), -1, []float64{15, 14, 13})
	writeWave(t, filepath.Join(dir, "c.tdms"), "acq", "ai0", epoch.Add(14*time.Second), -1, []float64{140, 130, 12})
	s, err := openWave(t, dir, WithOverlap(OverlapKeepLast)).Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{20, 19, 15, 140, 130, 12}) || !sameFloats(secs(s.Times), []float64{20, 19, 15, 14, 13, 12}) {
		t.Fatalf("Y=%v Times=%v", s.Y, secs(s.Times))
	}
	if len(s.Gaps) != 1 || !s.Gaps[0].From.Equal(epoch.Add(18*time.Second)) || !s.Gaps[0].To.Equal(epoch.Add(15*time.Second)) {
		t.Fatalf("gaps=%+v", s.Gaps)
	}
}

// One descending file reports its increment as Dt, as a merge of several
// descending files does. It used to report 0.
func TestSingleDescendingFileDt(t *testing.T) {
	dir := t.TempDir()
	writeWave(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", epoch.Add(10*time.Second), -1, []float64{10, 9, 8, 7})
	ds := openWave(t, dir)
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil || s.Dt != -1 {
		t.Fatalf("Read Dt=%v err=%v", s.Dt, err)
	}
	for _, opts := range [][]ReadOption{nil, {WithSharedClock()}, {WithoutTimes()}} {
		m, err := ds.ReadMany([]Selector{{Name: "ai0"}}, epoch, epoch.Add(time.Minute), opts...)
		if err != nil || m[0].Dt != -1 {
			t.Fatalf("ReadMany %d opts: Dt=%v err=%v", len(opts), m[0].Dt, err)
		}
	}
}
