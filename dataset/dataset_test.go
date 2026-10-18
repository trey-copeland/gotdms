package dataset

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trey-copeland/gotdms"
)

var epoch = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

func TestMergeThreeFiles(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "c.tdms"), "acq", "ai0", []float64{4, 5}, []float64{4, 5})
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1}, []float64{0, 1})
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{2, 3}, []float64{2, 3})

	ds := openAt(t, dir)
	if got := len(ds.Files()); got != 3 {
		t.Fatalf("files: %d", got)
	}
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{0, 1, 2, 3, 4, 5}) {
		t.Fatalf("Y = %v", s.Y)
	}
	if len(s.Gaps) != 0 || s.Source != TimeTimestamp || !s.Timed {
		t.Fatalf("source=%s gaps=%v timed=%v", s.Source, s.Gaps, s.Timed)
	}
	if s.Dt != 1 {
		t.Fatalf("dt = %v", s.Dt)
	}
	for i, ts := range s.Times {
		want := epoch.Add(time.Duration(i) * time.Second)
		if !ts.Equal(want) {
			t.Fatalf("time[%d] = %s, want %s", i, ts, want)
		}
	}
	if len(s.Files) != 3 || filepath.Base(s.Files[0]) != "a.tdms" || filepath.Base(s.Files[2]) != "c.tdms" {
		t.Fatalf("files = %v", s.Files)
	}
	if s.IsTimestamp {
		t.Fatal("data channel flagged as timestamp")
	}
}

func TestAbuttingFractionalDt(t *testing.T) {
	dir := t.TempDir()
	dt := 1.0 / 3
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, dt, 2 * dt}, []float64{0, 1, 2})
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{3 * dt, 4 * dt}, []float64{3, 4})
	ds := openAt(t, dir)
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{0, 1, 2, 3, 4}) || len(s.Gaps) != 0 {
		t.Fatalf("Y=%v gaps=%+v", s.Y, s.Gaps)
	}
}

func TestGlobalNameAndAmbiguity(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "slow", name: "Time", y: []float64{0, 1}},
		{group: "slow", name: "ai0", y: []float64{1, 2}},
		{group: "fast", name: "Time", y: []float64{0}},
		{group: "fast", name: "ai0", y: []float64{3}},
	})
	ds := openAt(t, dir)
	slow := ds.Find("ai0")
	if len(slow) != 2 {
		t.Fatalf("find = %+v", slow)
	}
	for _, h := range slow {
		if h.Source != TimeTimestamp || h.Timestamp != "Time" || h.IsTimestamp {
			t.Fatalf("hit = %+v", h)
		}
	}
	times := ds.Find("Time")
	if len(times) != 2 {
		t.Fatalf("time hits = %+v", times)
	}
	for _, h := range times {
		if !h.IsTimestamp || h.Timestamp != "" || h.Source != TimeTimestamp {
			t.Fatalf("timestamp hit = %+v", h)
		}
	}
	_, err := ds.Read(Selector{Name: "ai0"})
	var amb *AmbiguousError
	if !errors.As(err, &amb) || !errors.Is(err, ErrAmbiguous) || len(amb.Candidates) != 2 {
		t.Fatalf("err = %v", err)
	}
	s, err := ds.Read(Selector{Name: "ai0", Group: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{3}) || s.Group != "fast" {
		t.Fatalf("series = %+v", s)
	}
	clock, err := ds.Read(Selector{Name: "Time", Group: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	if !clock.IsTimestamp || !sameFloats(clock.Y, []float64{0, 1}) {
		t.Fatalf("clock Y=%v flag=%v", clock.Y, clock.IsTimestamp)
	}
	if !clock.Times[0].Equal(epoch) || !clock.Times[1].Equal(epoch.Add(time.Second)) {
		t.Fatalf("clock times = %v", clock.Times)
	}
}

func TestPerGroupOverride(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "slow", name: "Time", y: []float64{0, 10}},
		{group: "slow", name: "ai0", y: []float64{1, 2}},
		{group: "fast", name: "Time", y: []float64{100, 200}},
		{group: "fast", name: "tick", y: []float64{0, 1}},
		{group: "fast", name: "ai0", y: []float64{7, 8}},
	})
	ds, err := OpenGlob(dir, "*.tdms",
		WithTimestampChannelName("Time"),
		WithTimestampChannel("fast", "tick"),
		WithNumericEpoch(epoch),
	)
	if err != nil {
		t.Fatal(err)
	}
	fastTime := one(t, ds.Find("Time"), "fast")
	if fastTime.IsTimestamp {
		t.Fatal("overridden group still treats Time as the clock")
	}
	tick := one(t, ds.Find("tick"), "fast")
	if !tick.IsTimestamp || tick.Source != TimeTimestamp {
		t.Fatalf("tick = %+v", tick)
	}
	s, err := ds.Read(Selector{Name: "ai0", Group: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Times[0].Equal(epoch) || !s.Times[1].Equal(epoch.Add(time.Second)) {
		t.Fatalf("fast times = %v", s.Times)
	}
	slow, err := ds.Read(Selector{Name: "ai0", Group: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	if !slow.Times[1].Equal(epoch.Add(10 * time.Second)) {
		t.Fatalf("slow times = %v", slow.Times)
	}
}

func TestOptOutKeepsWaveform(t *testing.T) {
	dir := t.TempDir()
	t0 := epoch.Add(3 * time.Hour)
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "acq", name: "Time", y: []float64{0, 1}},
		{group: "acq", name: "ai0", y: []float64{4, 5}},
		{group: "special", name: "Time", y: []float64{9, 9}},
		{group: "special", name: "ai0", y: []float64{1, 2}, wave: true, start: t0, inc: 1},
	})
	ds, err := OpenGlob(dir, "*.tdms",
		WithTimestampChannelName("Time"),
		WithTimestampChannel("special", ""),
		WithNumericEpoch(epoch),
	)
	if err != nil {
		t.Fatal(err)
	}
	sp := one(t, ds.Find("Time"), "special")
	if sp.IsTimestamp {
		t.Fatal("opt-out channel is still a timestamp")
	}
	s, err := ds.Read(Selector{Name: "ai0", Group: "special"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != TimeWaveform || !s.Times[0].Equal(t0) {
		t.Fatalf("special source=%s time=%v", s.Source, s.Times)
	}
	acq, err := ds.Read(Selector{Name: "ai0", Group: "acq"})
	if err != nil {
		t.Fatal(err)
	}
	if acq.Source != TimeTimestamp || !acq.Times[0].Equal(epoch) {
		t.Fatalf("acq = %+v", acq.Times)
	}
}

func TestDeclaredBeatsWaveform(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "acq", name: "Time", y: []float64{0, 1, 2}},
		{group: "acq", name: "ai0", y: []float64{10, 20, 30}, wave: true, start: epoch.Add(-24 * time.Hour), inc: 1},
	})
	ds := openAt(t, dir)
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != TimeTimestamp || !s.Times[0].Equal(epoch) || !sameFloats(s.Y, []float64{10, 20, 30}) {
		t.Fatalf("source=%s Y=%v t0=%s", s.Source, s.Y, s.Times[0])
	}
}

func TestWaveformFallback(t *testing.T) {
	dir := t.TempDir()
	writeWave(t, filepath.Join(dir, "c.tdms"), "acq", "ai0", epoch.Add(4*time.Second), 1, []float64{4, 5})
	writeWave(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", epoch, 1, []float64{0, 1})
	writeWave(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", epoch.Add(2*time.Second), 1, []float64{2, 3})
	ds := openWave(t, dir)
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != TimeWaveform || !sameFloats(s.Y, []float64{0, 1, 2, 3, 4, 5}) || len(s.Gaps) != 0 {
		t.Fatalf("source=%s Y=%v gaps=%v", s.Source, s.Y, s.Gaps)
	}
	win, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch.Add(time.Second), epoch.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(win.Y, []float64{1, 2, 3}) {
		t.Fatalf("window Y = %v", win.Y)
	}
}

func TestMissingTimestampIsUntimed(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1}, []float64{1, 2})
	writeSpecs(t, filepath.Join(dir, "b.tdms"), []spec{
		{group: "other", name: "ai0", y: []float64{8, 9}, wave: true, start: epoch, inc: 1},
	})
	ds := openAt(t, dir)
	other := one(t, ds.Find("ai0"), "other")
	if other.Timed || other.Source != TimeNone {
		t.Fatalf("other = %+v", other)
	}
	_, err := ds.ReadWindow(Selector{Name: "ai0", Group: "other"}, epoch, epoch.Add(time.Hour))
	if !errors.Is(err, ErrUntimed) {
		t.Fatalf("err = %v", err)
	}
	s, err := ds.Read(Selector{Name: "ai0", Group: "acq"})
	if err != nil || !s.Timed {
		t.Fatalf("acq err=%v timed=%v", err, s.Timed)
	}
}

func TestLengthMismatch(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "acq", name: "Time", y: []float64{0, 1}},
		{group: "acq", name: "ai0", y: []float64{1, 2, 3}},
	})
	ds := openAt(t, dir)
	hit := ds.Find("ai0")
	if len(hit) != 1 || hit[0].Timed {
		t.Fatalf("hit = %+v", hit)
	}
	clock := ds.Find("Time")
	if len(clock) != 1 || !clock[0].IsTimestamp || !clock[0].Timed {
		t.Fatalf("clock = %+v", clock)
	}
	_, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch, epoch.Add(time.Hour))
	if !errors.Is(err, ErrUntimed) {
		t.Fatalf("err = %v", err)
	}
}

func TestNumericEpochRequired(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "run.tdms"), "acq", "ai0", []float64{0, 1}, []float64{1, 2})
	_, err := OpenGlob(dir, "*.tdms", WithTimestampChannelName("Time"))
	if !errors.Is(err, ErrArgument) {
		t.Fatalf("err = %v", err)
	}
	if _, err := OpenGlob(dir, "*.tdms", WithTimestampChannelName("")); !errors.Is(err, ErrArgument) {
		t.Fatalf("empty name = %v", err)
	}
	if _, err := OpenGlob(dir, "*.tdms", WithTimestampChannel("", "Time"), WithNumericEpoch(epoch)); !errors.Is(err, ErrArgument) {
		t.Fatalf("empty group = %v", err)
	}
}

func TestTimestampTypedChannel(t *testing.T) {
	dir := t.TempDir()
	t0 := epoch
	times := []time.Time{t0, t0.Add(2 * time.Second), t0.Add(4 * time.Second)}
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "acq", name: "Time", dt: tdms.TypeTimestamp, y: times},
		{group: "acq", name: "ai0", y: []float64{10, 20, 30}},
	})
	ds, err := OpenGlob(dir, "*.tdms", WithTimestampChannelName("Time"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ds.ReadWindow(Selector{Name: "ai0"}, t0.Add(2*time.Second), t0.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{20}) || len(s.Times) != 1 || !s.Times[0].Equal(t0.Add(2*time.Second)) {
		t.Fatalf("Y=%v times=%v", s.Y, s.Times)
	}
	clock, err := ds.Read(Selector{Name: "Time"})
	if err != nil {
		t.Fatal(err)
	}
	if !clock.IsTimestamp || math.Abs(clock.Y[0]-float64(t0.UnixNano())/1e9) > 1e-6 {
		t.Fatalf("clock Y=%v", clock.Y)
	}
}

func TestIntTimestampChannel(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "acq", name: "Time", dt: tdms.TypeInt32, y: []int32{0, 5, 10}},
		{group: "acq", name: "ai0", dt: tdms.TypeInt16, y: []int16{4, 5, 6}},
	})
	ds := openAt(t, dir)
	s, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch.Add(5*time.Second), epoch.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{5, 6}) {
		t.Fatalf("Y = %v", s.Y)
	}
	if !s.Times[0].Equal(epoch.Add(5*time.Second)) || !s.Times[1].Equal(epoch.Add(10*time.Second)) {
		t.Fatalf("times = %v", s.Times)
	}
}

func TestUnixEpoch(t *testing.T) {
	dir := t.TempDir()
	sec := float64(time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).Unix())
	writePair(t, filepath.Join(dir, "run.tdms"), "acq", "ai0", []float64{sec, sec + 1}, []float64{1, 2})
	ds, err := OpenGlob(dir, "*.tdms",
		WithTimestampChannelName("Time"),
		WithNumericEpoch(time.Unix(0, 0).UTC()),
	)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Times[0].Equal(epoch) || !s.Times[1].Equal(epoch.Add(time.Second)) {
		t.Fatalf("times = %v", s.Times)
	}
}

func TestNonMonotonic(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "run.tdms"), "acq", "ai0", []float64{0, 2, 1}, []float64{1, 2, 3})
	ds := openAt(t, dir)
	if len(ds.Find("ai0")) != 1 || !ds.Find("ai0")[0].Timed {
		t.Fatal("catalog should keep the channel")
	}
	_, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch, epoch.Add(500*time.Millisecond))
	if !errors.Is(err, ErrMonotonic) {
		t.Fatalf("window err = %v", err)
	}
	_, err = ds.Read(Selector{Name: "ai0"})
	if !errors.Is(err, ErrMonotonic) {
		t.Fatalf("read err = %v", err)
	}
}

func TestDuplicateTimestamps(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1, 1, 2}, []float64{1, 2, 3, 4})
	ds := openAt(t, dir)
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 2, 3, 4}) {
		t.Fatalf("Y = %v", s.Y)
	}
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{1, 2}, []float64{20, 30})
	ds = openAt(t, dir)
	s, err = ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	// File a ends at t=2 with dt 0 because of the repeated stamp, so file b's
	// sample at t=1 overlaps and is dropped; t=2 is equal to the last kept time.
	if !sameFloats(s.Y, []float64{1, 2, 3, 4}) {
		t.Fatalf("across files Y = %v times=%v", s.Y, s.Times)
	}
}

func TestDifferentRatesSameWindow(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "slow", name: "Time", y: []float64{0, 1, 2, 3}},
		{group: "slow", name: "ai0", y: []float64{0, 1, 2, 3}},
		{group: "fast", name: "Time", y: []float64{0, 0.5, 1, 1.5, 2, 2.5}},
		{group: "fast", name: "ai0", y: []float64{0, 0.5, 1, 1.5, 2, 2.5}},
	})
	ds := openAt(t, dir)
	start, end := epoch.Add(time.Second), epoch.Add(2500*time.Millisecond)
	got, err := ds.ReadMany([]Selector{{Name: "ai0", Group: "slow"}, {Name: "ai0", Group: "fast"}}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(got[0].Y, []float64{1, 2}) || !sameFloats(got[1].Y, []float64{1, 1.5, 2}) {
		t.Fatalf("Y = %v %v", got[0].Y, got[1].Y)
	}
	if !got[0].Origin.Equal(ds.Origin()) || !got[1].Origin.Equal(got[0].Origin) {
		t.Fatal("origins differ")
	}
	rel0 := got[0].RelativeTimes()
	if len(rel0) != 2 || math.Abs(rel0[0]-1) > 1e-9 || math.Abs(rel0[1]-2) > 1e-9 {
		t.Fatalf("slow rel = %v", rel0)
	}
	rel1 := got[1].RelativeTimes()
	for i, want := range []float64{1, 1.5, 2} {
		if math.Abs(rel1[i]-want) > 1e-9 {
			t.Fatalf("fast rel = %v", rel1)
		}
	}
}

func TestUntimed(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "b.tdms"), []spec{{group: "acq", name: "raw", y: []float64{3, 4}}})
	writeSpecs(t, filepath.Join(dir, "a.tdms"), []spec{{group: "acq", name: "raw", y: []float64{1, 2}}})
	ds := openWave(t, dir)
	hits := ds.Find("raw")
	if len(hits) != 2 || hits[0].Timed || hits[0].Source != TimeNone {
		t.Fatalf("hits = %+v", hits)
	}
	s, err := ds.Read(Selector{Name: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Timed || s.Times != nil || !sameFloats(s.Y, []float64{1, 2, 3, 4}) {
		t.Fatalf("series timed=%v Y=%v", s.Timed, s.Y)
	}
	if !sameFloats(s.RelativeTimes(), []float64{0, 1, 2, 3}) {
		t.Fatalf("rel = %v", s.RelativeTimes())
	}
	_, err = ds.ReadWindow(Selector{Name: "raw"}, epoch, epoch.Add(time.Hour))
	if !errors.Is(err, ErrUntimed) {
		t.Fatalf("window err = %v", err)
	}
}

func TestRelativeOrigin(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "early.tdms"), "acq", "early", []float64{0}, []float64{1})
	writePair(t, filepath.Join(dir, "late.tdms"), "acq", "late", []float64{10, 11}, []float64{2, 3})
	ds := openAt(t, dir)
	if !ds.Origin().Equal(epoch) {
		t.Fatalf("origin = %s", ds.Origin())
	}
	s, err := ds.Read(Selector{Name: "late"})
	if err != nil {
		t.Fatal(err)
	}
	rel := s.RelativeTimes()
	if len(rel) != 2 || math.Abs(rel[0]-10) > 1e-9 || math.Abs(rel[1]-11) > 1e-9 {
		t.Fatalf("rel = %v", rel)
	}
	origin := epoch.Add(30 * time.Second)
	ds2, err := OpenGlob(dir, "*.tdms", WithTimestampChannelName("Time"), WithNumericEpoch(epoch), WithOrigin(origin))
	if err != nil {
		t.Fatal(err)
	}
	s, err = ds2.Read(Selector{Name: "early"})
	if err != nil {
		t.Fatal(err)
	}
	rel = s.RelativeTimes()
	if len(rel) != 1 || math.Abs(rel[0]-(-30)) > 1e-9 {
		t.Fatalf("explicit rel = %v", rel)
	}
}

func TestWindowCropAndPrune(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "run.tdms"), "acq", "ai0", []float64{0, 1, 2, 3, 4}, []float64{0, 1, 2, 3, 4})
	ds := openAt(t, dir)
	s, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch.Add(time.Second), epoch.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 2, 3}) {
		t.Fatalf("Y = %v", s.Y)
	}
	if !s.Times[0].Equal(epoch.Add(time.Second)) || !s.Times[2].Equal(epoch.Add(3*time.Second)) {
		t.Fatalf("times = %v", s.Times)
	}
	empty, err := ds.ReadWindow(Selector{Name: "ai0"}, epoch.Add(10*time.Second), epoch.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Y) != 0 || !empty.Timed {
		t.Fatalf("empty timed=%v Y=%v", empty.Timed, empty.Y)
	}
	_, err = ds.ReadWindow(Selector{Name: "ai0"}, epoch, epoch)
	if !errors.Is(err, ErrArgument) {
		t.Fatalf("bad window = %v", err)
	}

	early := filepath.Join(dir, "early.tdms")
	mid := filepath.Join(dir, "mid.tdms")
	late := filepath.Join(dir, "late.tdms")
	writePair(t, early, "acq", "span", []float64{0, 1}, []float64{0, 1})
	writePair(t, mid, "acq", "span", []float64{2, 3}, []float64{2, 3})
	writePair(t, late, "acq", "span", []float64{4, 5}, []float64{4, 5})
	ds = openAt(t, dir)
	if err := os.Remove(early); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(late); err != nil {
		t.Fatal(err)
	}
	s, err = ds.ReadWindow(Selector{Name: "span"}, epoch.Add(2*time.Second), epoch.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{2, 3}) {
		t.Fatalf("pruned Y = %v", s.Y)
	}
	if _, err := ds.Read(Selector{Name: "span"}); err == nil {
		t.Fatal("full read should open the removed files")
	}
}

func TestGap(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1}, []float64{1, 2})
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{5}, []float64{9})
	ds := openAt(t, dir)
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 2, 9}) || len(s.Gaps) != 1 {
		t.Fatalf("Y=%v gaps=%+v", s.Y, s.Gaps)
	}
	for _, v := range s.Y {
		if math.IsNaN(v) {
			t.Fatal("NaN filler")
		}
	}
	if !s.Gaps[0].From.Equal(epoch.Add(2*time.Second)) || !s.Gaps[0].To.Equal(epoch.Add(5*time.Second)) {
		t.Fatalf("gap = %+v", s.Gaps[0])
	}
}

func TestOverlapPolicies(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1, 2}, []float64{1, 2, 3})
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{1, 2, 3}, []float64{20, 30, 40})

	first := openAt(t, dir, WithOverlap(OverlapKeepFirst))
	s, err := first.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 2, 3, 40}) || len(s.Files) != 2 {
		t.Fatalf("keep first Y=%v files=%v", s.Y, s.Files)
	}

	last := openAt(t, dir, WithOverlap(OverlapKeepLast))
	s, err = last.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 20, 30, 40}) {
		t.Fatalf("keep last Y = %v", s.Y)
	}

	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1}, []float64{1, 2})
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{0, 1, 2}, []float64{7, 8, 9})
	replaced := openAt(t, dir, WithOverlap(OverlapKeepLast))
	s, err = replaced.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{7, 8, 9}) || len(s.Files) != 1 || filepath.Base(s.Files[0]) != "b.tdms" {
		t.Fatalf("replaced Y=%v files=%v", s.Y, s.Files)
	}

	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1, 2}, []float64{1, 2, 3})
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{1, 2, 3}, []float64{20, 30, 40})
	bad := openAt(t, dir, WithOverlap(OverlapError))
	_, err = bad.Read(Selector{Name: "ai0"})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("overlap err = %v", err)
	}
}

func TestSearchFindMatch(t *testing.T) {
	dir := t.TempDir()
	writeSpecs(t, filepath.Join(dir, "run.tdms"), []spec{
		{group: "acq", name: "Time", y: []float64{0}},
		{group: "acq", name: "ai0", y: []float64{1}},
		{group: "acq", name: "ai1", y: []float64{2}},
		{group: "acq", name: "temp", y: []float64{3}},
	})
	ds := openAt(t, dir)
	if len(ds.Find("ai0")) != 1 || len(ds.Find("missing")) != 0 {
		t.Fatal(ds.Find("ai0"))
	}
	if len(ds.Search("ai")) != 2 || len(ds.Search("AI")) != 0 || len(ds.Search("")) != len(ds.Occurrences()) {
		t.Fatal("search")
	}
	matched, err := ds.Match("ai?")
	if err != nil || len(matched) != 2 {
		t.Fatalf("match = %v %v", matched, err)
	}
	if _, err := ds.Match("["); !errors.Is(err, ErrArgument) {
		t.Fatalf("bad pattern = %v", err)
	}
	if _, err := ds.Read(Selector{}); !errors.Is(err, ErrArgument) {
		t.Fatalf("empty name = %v", err)
	}
	if _, err := ds.Read(Selector{Name: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
}

func TestOpenFilesAndGlob(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.tdms")
	b := filepath.Join(dir, "b.tdms")
	writePair(t, a, "acq", "ai0", []float64{0}, []float64{1})
	writePair(t, b, "acq", "ai0", []float64{1}, []float64{2})
	ds, err := OpenFiles([]string{b, a, b}, WithTimestampChannelName("Time"), WithNumericEpoch(epoch), WithWorkers(2), WithReadWorkers(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Files()) != 2 || !filepath.IsAbs(ds.Files()[0]) {
		t.Fatalf("files = %v", ds.Files())
	}
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 2}) {
		t.Fatalf("Y = %v", s.Y)
	}
	if _, err := OpenFiles(nil); !errors.Is(err, ErrNoFiles) {
		t.Fatalf("nil list = %v", err)
	}
	if _, err := OpenGlob(dir, "none-*.tdms"); !errors.Is(err, ErrNoFiles) {
		t.Fatalf("empty glob = %v", err)
	}
}

func TestZeroAndNegativeIncrement(t *testing.T) {
	dir := t.TempDir()
	writeWave(t, filepath.Join(dir, "flat.tdms"), "acq", "flat", epoch, 0, []float64{1, 2, 3})
	writeWave(t, filepath.Join(dir, "back.tdms"), "acq", "back", epoch, -1, []float64{0, -1, -2})
	ds := openWave(t, dir)
	flat, err := ds.ReadWindow(Selector{Name: "flat"}, epoch, epoch.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(flat.Y, []float64{1, 2, 3}) {
		t.Fatalf("flat = %v", flat.Y)
	}
	miss, err := ds.ReadWindow(Selector{Name: "flat"}, epoch.Add(time.Second), epoch.Add(2*time.Second))
	if err != nil || len(miss.Y) != 0 {
		t.Fatalf("flat miss = %v %v", miss.Y, err)
	}
	back, err := ds.ReadWindow(Selector{Name: "back"}, epoch.Add(-1500*time.Millisecond), epoch.Add(-500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(back.Y, []float64{-1}) {
		t.Fatalf("back = %v times=%v", back.Y, back.Times)
	}
	all, err := ds.Read(Selector{Name: "back"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(all.Y, []float64{0, -1, -2}) || len(all.Gaps) != 0 {
		t.Fatalf("back full Y=%v gaps=%v", all.Y, all.Gaps)
	}
	occ := ds.Find("back")
	if len(occ) != 1 || occ[0].Start.After(occ[0].Last) || occ[0].Source != TimeWaveform {
		t.Fatalf("span %+v", occ)
	}
}

func TestResample(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", []float64{0, 1}, []float64{0, 10})
	writePair(t, filepath.Join(dir, "b.tdms"), "acq", "ai0", []float64{4}, []float64{40})
	ds := openAt(t, dir)
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	lin, err := s.Resample(epoch.Add(-time.Second), epoch.Add(6*time.Second), 1, InterpLinear)
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(lin.Y, []float64{0, 10, 40}) {
		t.Fatalf("linear = %v gaps=%+v", lin.Y, s.Gaps)
	}
	prev, err := s.Resample(epoch, epoch.Add(2*time.Second), 0.5, InterpPrevious)
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(prev.Y, []float64{0, 0, 10, 10}) {
		t.Fatalf("previous = %v", prev.Y)
	}
	if _, err := s.Resample(epoch, epoch.Add(time.Second), 0, InterpLinear); !errors.Is(err, ErrArgument) {
		t.Fatalf("dt = %v", err)
	}
}

func TestAppendedSameTiming(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.tdms")
	w, err := tdms.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.Group("acq").Channel("ai0", tdms.TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(tdms.PropStartTime, epoch); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetProperty(tdms.PropIncrement, 1.0); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Write([]float64{4, 5}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ds := openWave(t, dir)
	s, err := ds.Read(Selector{Name: "ai0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloats(s.Y, []float64{1, 2, 3, 4, 5}) || len(s.Gaps) != 0 {
		t.Fatalf("Y=%v gaps=%v", s.Y, s.Gaps)
	}
	if !s.Times[4].Equal(epoch.Add(4 * time.Second)) {
		t.Fatalf("last = %s", s.Times[4])
	}
}

func TestMixedTimeAxes(t *testing.T) {
	dir := t.TempDir()
	writeWave(t, filepath.Join(dir, "a.tdms"), "acq", "ai0", epoch, 1, []float64{1})
	writeSpecs(t, filepath.Join(dir, "b.tdms"), []spec{{group: "acq", name: "ai0", y: []float64{2}}})
	ds := openWave(t, dir)
	_, err := ds.Read(Selector{Name: "ai0"})
	if !errors.Is(err, ErrArgument) {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeSlack(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	dt := 1.0 / 3
	end := addDt(t0, dt)
	if overlaps(end, t0, dt) || gapBetween(end, t0, dt) {
		t.Fatal("exact next tick")
	}
	if overlaps(end.Add(-time.Nanosecond), t0, dt) || gapBetween(end.Add(time.Nanosecond), t0, dt) {
		t.Fatal("1ns rounding")
	}
	if !overlaps(end.Add(-2*time.Nanosecond), t0, dt) || !gapBetween(end.Add(2*time.Nanosecond), t0, dt) {
		t.Fatal("2ns")
	}
	if !overlaps(t0, t0, 0) || gapBetween(t0.Add(time.Second), t0, 0) {
		t.Fatal("zero dt")
	}
	neg := addDt(t0, -1)
	if overlaps(neg, t0, -1) || gapBetween(neg, t0, -1) || !overlaps(neg.Add(2*time.Nanosecond), t0, -1) {
		t.Fatal("negative dt")
	}
}

type spec struct {
	group string
	name  string
	dt    tdms.DataType
	y     any
	start time.Time
	inc   float64
	wave  bool
}

func writeWave(t *testing.T, path, group, name string, start time.Time, inc float64, y []float64) {
	t.Helper()
	writeSpecs(t, path, []spec{{group: group, name: name, y: y, start: start, inc: inc, wave: true}})
}

func writePair(t *testing.T, path, group, data string, times, y []float64) {
	t.Helper()
	writeSpecs(t, path, []spec{
		{group: group, name: "Time", y: times},
		{group: group, name: data, y: y},
	})
}

func writeSpecs(t *testing.T, path string, specs []spec) {
	t.Helper()
	w, err := tdms.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range specs {
		dt := s.dt
		if dt == 0 {
			dt = tdms.TypeFloat64
		}
		ch, err := w.Group(s.group).Channel(s.name, dt)
		if err != nil {
			t.Fatal(err)
		}
		if s.wave {
			if err := ch.SetProperty(tdms.PropStartTime, s.start); err != nil {
				t.Fatal(err)
			}
			if err := ch.SetProperty(tdms.PropIncrement, s.inc); err != nil {
				t.Fatal(err)
			}
		}
		if err := ch.Write(s.y); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func openAt(t *testing.T, dir string, opts ...Option) *Dataset {
	t.Helper()
	all := []Option{WithTimestampChannelName("Time"), WithNumericEpoch(epoch), WithWorkers(2), WithReadWorkers(2)}
	all = append(all, opts...)
	ds, err := OpenGlob(dir, "*.tdms", all...)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func openWave(t *testing.T, dir string, opts ...Option) *Dataset {
	t.Helper()
	all := []Option{WithWorkers(2), WithReadWorkers(2)}
	all = append(all, opts...)
	ds, err := OpenGlob(dir, "*.tdms", all...)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func one(t *testing.T, hits []Occurrence, group string) Occurrence {
	t.Helper()
	for _, h := range hits {
		if h.Group == group {
			return h
		}
	}
	t.Fatalf("no occurrence in group %s: %+v", group, hits)
	return Occurrence{}
}

func sameFloats(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
