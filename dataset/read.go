package dataset

import (
	"math"
	"strings"
	"time"

	"github.com/trey-copeland/gotdms"
)

// timeSlack is the tolerance for last+dt versus the next sample.
// Rounding wf_increment or a numeric second to nanoseconds can disagree by 1ns.
const timeSlack = time.Nanosecond

type piece struct {
	file  string
	start time.Time
	dt    float64
	y     []float64
	times []time.Time
	timed bool
}

func readOccurrence(o Occurrence, opt options, start, end time.Time, window bool) (piece, error) {
	f, err := tdms.Open(o.File)
	if err != nil {
		return piece{}, err
	}
	defer f.Close()
	g, ok := f.Group(o.Group)
	if !ok {
		return piece{}, ErrNotFound
	}
	ch, ok := g.Channel(o.Name)
	if !ok {
		return piece{}, ErrNotFound
	}
	p := piece{file: o.File, dt: o.Dt, timed: o.Timed}
	if !o.Timed {
		y, err := ch.Float64s()
		if err != nil {
			return piece{}, err
		}
		p.y = y
		return p, nil
	}
	switch o.Source {
	case TimeWaveform:
		return readWaveform(ch, o, start, end, window)
	case TimeTimestamp:
		return readTimestampAxis(g, ch, o, opt, start, end, window)
	default:
		return piece{}, ErrUntimed
	}
}

func readWaveform(ch *tdms.Channel, o Occurrence, start, end time.Time, window bool) (piece, error) {
	t0, ok := ch.StartTime()
	if !ok {
		return piece{}, ErrArgument
	}
	inc := 0.0
	if v, has := ch.Increment(); has {
		inc = v
	}
	n := ch.Len()
	p := piece{file: o.File, dt: inc, timed: true}
	if n == 0 {
		return p, nil
	}
	i0, i1 := 0, n
	if window {
		var err error
		i0, i1, err = waveformBounds(t0, inc, n, start, end)
		if err != nil {
			return piece{}, err
		}
	}
	if i0 > i1 {
		i0, i1 = i1, i0
	}
	y, err := readFloatSpan(ch, i0, i1)
	if err != nil {
		return piece{}, err
	}
	times := make([]time.Time, len(y))
	for i := range times {
		tm, err := waveformAt(t0, inc, i0+i)
		if err != nil {
			return piece{}, err
		}
		times[i] = tm
	}
	p.y = y
	p.times = times
	if len(times) > 0 {
		p.start = times[0]
	}
	return p, nil
}

func waveformBounds(t0 time.Time, inc float64, n int, winStart, winEnd time.Time) (int, int, error) {
	if _, err := waveformAt(t0, inc, 0); err != nil {
		return 0, 0, err
	}
	if n > 1 {
		if _, err := waveformAt(t0, inc, n-1); err != nil {
			return 0, 0, err
		}
	}
	if inc == 0 {
		if !t0.Before(winStart) && t0.Before(winEnd) {
			return 0, n, nil
		}
		return 0, 0, nil
	}
	if inc > 0 {
		i0 := firstAtOrAfter(t0, inc, n, winStart)
		i1 := firstAtOrAfter(t0, inc, n, winEnd)
		return i0, i1, nil
	}
	i0 := firstBefore(t0, inc, n, winEnd)
	i1 := firstBefore(t0, inc, n, winStart)
	return i0, i1, nil
}

func firstAtOrAfter(t0 time.Time, inc float64, n int, target time.Time) int {
	lo, hi := 0, n
	for lo < hi {
		mid := (lo + hi) / 2
		tm, _ := waveformAt(t0, inc, mid)
		if tm.Before(target) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func firstBefore(t0 time.Time, inc float64, n int, target time.Time) int {
	lo, hi := 0, n
	for lo < hi {
		mid := (lo + hi) / 2
		tm, _ := waveformAt(t0, inc, mid)
		if !tm.Before(target) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func readTimestampAxis(g *tdms.Group, ch *tdms.Channel, o Occurrence, opt options, start, end time.Time, window bool) (piece, error) {
	axis := ch
	if !o.IsTimestamp {
		var ok bool
		axis, ok = g.Channel(o.Timestamp)
		if !ok {
			return piece{}, ErrNotFound
		}
	}
	times, usable, err := loadDeclaredTimes(axis, opt)
	if err != nil {
		return piece{}, err
	}
	if !usable {
		return piece{}, ErrUntimed
	}
	if err := checkMonotonic(times); err != nil {
		return piece{}, err
	}
	y, err := valuesForAxis(ch, o.IsTimestamp)
	if err != nil {
		return piece{}, err
	}
	if len(y) != len(times) {
		return piece{}, ErrArgument
	}
	dt := 0.0
	if d, ok := uniformDt(times); ok {
		dt = d
	}
	p := piece{file: o.File, dt: dt, timed: true}
	for i := range y {
		if window && (times[i].Before(start) || !times[i].Before(end)) {
			continue
		}
		p.y = append(p.y, y[i])
		p.times = append(p.times, times[i])
	}
	if len(p.times) > 0 {
		p.start = p.times[0]
	}
	return p, nil
}

func valuesForAxis(ch *tdms.Channel, isTimestamp bool) ([]float64, error) {
	if isTimestamp && ch.DataType() == tdms.TypeTimestamp {
		// Y is seconds since the Unix epoch. RelativeTimes uses Times, not Y.
		times, err := ch.Times()
		if err != nil {
			return nil, err
		}
		out := make([]float64, len(times))
		for i, t := range times {
			out[i] = float64(t.UnixNano()) / 1e9
		}
		return out, nil
	}
	return ch.Float64s()
}

func checkMonotonic(times []time.Time) error {
	for i := 1; i < len(times); i++ {
		if times[i].Before(times[i-1]) {
			return errWrap(ErrMonotonic, "sample steps backwards")
		}
	}
	return nil
}

func readFloatSpan(ch *tdms.Channel, i0, i1 int) ([]float64, error) {
	if i1 <= i0 {
		return []float64{}, nil
	}
	// Cursor has no seek. Batches before i0 are decoded and discarded.
	// The walk stops once i1 is reached, so the tail of the channel is not read.
	out := make([]float64, 0, i1-i0)
	cur := ch.Cursor(8192)
	idx := 0
	for cur.Next() {
		if idx >= i1 {
			break
		}
		batch, err := cur.Float64s()
		if err != nil {
			return nil, err
		}
		if idx+len(batch) <= i0 {
			idx += len(batch)
			continue
		}
		for i, v := range batch {
			abs := idx + i
			if abs < i0 {
				continue
			}
			if abs >= i1 {
				return out, nil
			}
			out = append(out, v)
		}
		idx += len(batch)
		if idx >= i1 {
			break
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func mergePieces(pieces []piece, policy OverlapPolicy) (Series, error) {
	var y []float64
	var times []time.Time
	var dts []float64
	var src []string
	var gaps []Gap
	for _, p := range pieces {
		if len(p.y) == 0 {
			continue
		}
		if p.timed && len(p.times) != len(p.y) {
			return Series{}, ErrArgument
		}
		for i := range p.y {
			// Samples inside one file stay in file order, including equal
			// timestamps and a zero waveform increment. Overlap and gap policy
			// applies where a new file meets samples already kept.
			sameFile := len(src) > 0 && src[len(src)-1] == p.file
			if p.timed && len(times) > 0 && !sameFile {
				last := times[len(times)-1]
				lastDt := dts[len(dts)-1]
				t := p.times[i]
				if overlaps(t, last, lastDt) {
					switch policy {
					case OverlapError:
						return Series{}, fmtOverlap(uniqueFiles(src), p.file, t)
					case OverlapKeepFirst:
						continue
					case OverlapKeepLast:
						for len(times) > 0 && !times[len(times)-1].Before(t) {
							times = times[:len(times)-1]
							y = y[:len(y)-1]
							dts = dts[:len(dts)-1]
							src = src[:len(src)-1]
						}
						trimGaps(&gaps, times)
					default:
						return Series{}, ErrArgument
					}
				}
				if len(times) > 0 {
					last = times[len(times)-1]
					lastDt = dts[len(dts)-1]
					if gapBetween(t, last, lastDt) {
						gaps = append(gaps, Gap{From: addDt(last, lastDt), To: t})
					}
				}
			}
			y = append(y, p.y[i])
			dts = append(dts, p.dt)
			src = append(src, p.file)
			if p.timed {
				times = append(times, p.times[i])
			}
		}
	}
	s := Series{Files: uniqueFiles(src), Y: y, Gaps: gaps}
	if len(times) > 0 {
		s.Times = times
		s.Timed = true
	}
	if dt, ok := uniformSampleDt(dts); ok {
		s.Dt = dt
	}
	return s, nil
}

func uniformSampleDt(dts []float64) (float64, bool) {
	if len(dts) == 0 {
		return 0, false
	}
	dt := dts[0]
	for _, d := range dts[1:] {
		if d <= 0 || math.Abs(d-dt) > 1e-9*math.Max(1, math.Abs(dt)) {
			return 0, false
		}
	}
	return dt, true
}

func trimGaps(gaps *[]Gap, times []time.Time) {
	g := *gaps
	for len(g) > 0 && (len(times) == 0 || g[len(g)-1].To.After(times[len(times)-1])) {
		g = g[:len(g)-1]
	}
	*gaps = g
}

func uniqueFiles(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(src))
	out := make([]string, 0, len(src))
	for _, f := range src {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

func overlaps(t, last time.Time, dt float64) bool {
	if dt > 0 {
		return t.Before(addDt(last, dt).Add(-timeSlack))
	}
	if dt < 0 {
		return t.After(addDt(last, dt).Add(timeSlack))
	}
	return !t.After(last)
}

func gapBetween(t, last time.Time, dt float64) bool {
	if dt <= 0 {
		return false
	}
	return t.After(addDt(last, dt).Add(timeSlack))
}

func addDt(t time.Time, dt float64) time.Time {
	if dt == 0 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return t
	}
	ns := math.Round(dt * 1e9)
	if ns >= float64(math.MaxInt64) {
		return t.Add(time.Duration(math.MaxInt64))
	}
	if ns <= float64(math.MinInt64) {
		return t.Add(time.Duration(math.MinInt64))
	}
	return t.Add(time.Duration(ns))
}

func fmtOverlap(kept []string, file string, t time.Time) error {
	msg := "sample at " + t.UTC().Format(time.RFC3339Nano) + " from " + file
	if len(kept) > 0 {
		msg += " overlaps " + strings.Join(kept, ", ")
	}
	return errWrap(ErrOverlap, msg)
}

func errWrap(err error, msg string) error {
	return &wrapped{msg: msg, err: err}
}

type wrapped struct {
	msg string
	err error
}

func (w *wrapped) Error() string { return w.err.Error() + ": " + w.msg }
func (w *wrapped) Unwrap() error { return w.err }

// Resample builds a uniform grid from start (inclusive) to end (exclusive)
// with step dt seconds. method is InterpLinear or InterpPrevious.
//
// Grid points before the first sample, after the last sample, or inside a
// Gap are omitted. The result can be shorter than the grid. Values are not
// extrapolated. An untimed series returns ErrUntimed. dt must be positive.
// A grid of more than 10 million points returns ErrArgument.
func (s Series) Resample(start, end time.Time, dt float64, method Interp) (Series, error) {
	if !s.Timed {
		return Series{}, errWrap(ErrUntimed, "resample")
	}
	if dt <= 0 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return Series{}, errWrap(ErrArgument, "resample dt")
	}
	if !end.After(start) {
		return Series{}, errWrap(ErrArgument, "resample window")
	}
	steps := end.Sub(start).Seconds() / dt
	if math.IsNaN(steps) || math.IsInf(steps, 0) || steps > 10_000_000 {
		return Series{}, errWrap(ErrArgument, "resample grid too long")
	}
	out := Series{
		Name:        s.Name,
		Group:       s.Group,
		Files:       append([]string(nil), s.Files...),
		Timed:       true,
		IsTimestamp: s.IsTimestamp,
		Source:      s.Source,
		Dt:          dt,
		Origin:      s.Origin,
		Y:           []float64{},
		Times:       []time.Time{},
	}
	if len(s.Times) == 0 {
		return out, nil
	}
	for t := start; t.Before(end); t = addDt(t, dt) {
		if t.Before(s.Times[0]) || t.After(s.Times[len(s.Times)-1]) {
			continue
		}
		if insideGap(s.Gaps, t) {
			continue
		}
		v, ok := sampleAt(s, t, method)
		if !ok {
			continue
		}
		out.Times = append(out.Times, t)
		out.Y = append(out.Y, v)
	}
	return out, nil
}

func insideGap(gaps []Gap, t time.Time) bool {
	for _, g := range gaps {
		if !t.Before(g.From) && t.Before(g.To) {
			return true
		}
	}
	return false
}

func sampleAt(s Series, t time.Time, method Interp) (float64, bool) {
	i := 0
	j := len(s.Times)
	for i < j {
		mid := (i + j) / 2
		if s.Times[mid].Before(t) {
			i = mid + 1
		} else {
			j = mid
		}
	}
	if i < len(s.Times) && s.Times[i].Equal(t) {
		return s.Y[i], true
	}
	switch method {
	case InterpPrevious:
		if i == 0 {
			return 0, false
		}
		return s.Y[i-1], true
	case InterpLinear:
		if i == 0 || i >= len(s.Times) {
			return 0, false
		}
		t0 := s.Times[i-1]
		t1 := s.Times[i]
		span := t1.Sub(t0).Seconds()
		if span == 0 {
			return s.Y[i-1], true
		}
		frac := t.Sub(t0).Seconds() / span
		return s.Y[i-1] + frac*(s.Y[i]-s.Y[i-1]), true
	default:
		return 0, false
	}
}
