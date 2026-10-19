package dataset

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
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
	f, err := openTDMS(o.File, opt.segIndex)
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
	n := int64(ch.Len())
	p := piece{file: o.File, dt: inc, timed: true}
	if n == 0 {
		return p, nil
	}
	i0, i1 := int64(0), n
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
		tm, err := waveformAt(t0, inc, i0+int64(i))
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

func waveformBounds(t0 time.Time, inc float64, n int64, winStart, winEnd time.Time) (int64, int64, error) {
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

func firstAtOrAfter(t0 time.Time, inc float64, n int64, target time.Time) int64 {
	lo, hi := int64(0), n
	for lo < hi {
		mid := lo + (hi-lo)/2
		tm, _ := waveformAt(t0, inc, mid)
		if tm.Before(target) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func firstBefore(t0 time.Time, inc float64, n int64, target time.Time) int64 {
	lo, hi := int64(0), n
	for lo < hi {
		mid := lo + (hi-lo)/2
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
	// wantY only when this call's series is the clock. A data channel must not
	// decode the axis a second time to build Y.
	times, axisY, err := loadAxis(axis, opt, o.IsTimestamp)
	if err != nil {
		return piece{}, err
	}
	if err := checkMonotonic(times); err != nil {
		return piece{}, err
	}
	var y []float64
	if o.IsTimestamp {
		y = axisY
	} else {
		y, err = ch.Float64s()
		if err != nil {
			return piece{}, err
		}
	}
	if len(y) != len(times) {
		return piece{}, ErrArgument
	}
	dt := 0.0
	if d, ok := uniformDt(times); ok {
		dt = d
	}
	i0, i1 := timeWindow(times, start, end, window)
	p := piece{
		file:  o.File,
		dt:    dt,
		timed: true,
		y:     clipFloat(y, i0, i1, false),
		times: clipTime(times, i0, i1, false),
	}
	if len(p.times) > 0 {
		p.start = p.times[0]
	}
	return p, nil
}

func checkMonotonic(times []time.Time) error {
	for i := 1; i < len(times); i++ {
		if times[i].Before(times[i-1]) {
			return errWrap(ErrMonotonic, "sample steps backwards")
		}
	}
	return nil
}

func timeWindow(times []time.Time, start, end time.Time, window bool) (int, int) {
	if !window {
		return 0, len(times)
	}
	i0 := sortSearch(times, start)
	i1 := sortSearch(times, end)
	if i1 < i0 {
		return i0, i0
	}
	return i0, i1
}

func sortSearch(times []time.Time, target time.Time) int {
	lo, hi := 0, len(times)
	for lo < hi {
		mid := (lo + hi) / 2
		if times[mid].Before(target) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// clipFloat returns src[i0:i1]. shared means another series still holds src,
// so the result must be its own backing array. A partial window is always
// copied so the full channel can be released.
func clipFloat(src []float64, i0, i1 int, shared bool) []float64 {
	if i1 < i0 {
		i1 = i0
	}
	n := i1 - i0
	if n == 0 || i0 > len(src) {
		return nil
	}
	if i1 > len(src) {
		i1 = len(src)
		n = i1 - i0
	}
	if !shared && i0 == 0 && i1 == len(src) {
		return src
	}
	out := make([]float64, n)
	copy(out, src[i0:i1])
	return out
}

func clipTime(src []time.Time, i0, i1 int, shared bool) []time.Time {
	if i1 < i0 {
		i1 = i0
	}
	n := i1 - i0
	if n == 0 || i0 > len(src) {
		return nil
	}
	if i1 > len(src) {
		i1 = len(src)
		n = i1 - i0
	}
	if !shared && i0 == 0 && i1 == len(src) {
		return src
	}
	out := make([]time.Time, n)
	copy(out, src[i0:i1])
	return out
}

type readSlot struct {
	piece piece
	err   error
}

type fileRead struct {
	path     string
	segIndex bool
	once     sync.Once
	f        *tdms.File
	err      error
	clocks   map[string]*clockRead
	pending  int32
}

type clockRead struct {
	once    sync.Once
	times   []time.Time
	y       []float64
	dt      float64
	i0, i1  int
	err     error
	users   int
	tsUsers int
}

func (d *Dataset) readShared(sels []Selector, start, end time.Time, basis time.Time) ([]Series, error) {
	type plan struct {
		hits  []Occurrence
		slots []int
	}
	plans := make([]plan, len(sels))
	var slots []readSlot
	files := map[string]*fileRead{}
	var items []readItem
	for i, sel := range sels {
		hits, err := d.resolve(sel)
		if err != nil {
			return nil, err
		}
		for j := 1; j < len(hits); j++ {
			if hits[j].Timed != hits[0].Timed || hits[j].Source != hits[0].Source || hits[j].IsTimestamp != hits[0].IsTimestamp {
				return nil, fmt.Errorf("%w: %q group %q has mixed time axes", ErrArgument, hits[0].Name, hits[0].Group)
			}
		}
		if err := validateWindow(hits, true); err != nil {
			return nil, err
		}
		plans[i].hits = hits
		plans[i].slots = make([]int, len(hits))
		for hi, o := range hits {
			if !hitsWindow(o, start, end) {
				plans[i].slots[hi] = -1
				continue
			}
			fr := files[o.File]
			if fr == nil {
				fr = &fileRead{path: o.File, segIndex: d.opt.segIndex, clocks: map[string]*clockRead{}}
				files[o.File] = fr
			}
			if o.Source == TimeTimestamp {
				name := o.Timestamp
				if o.IsTimestamp {
					name = o.Name
				}
				key := o.Group + "\x00" + name
				ck := fr.clocks[key]
				if ck == nil {
					ck = &clockRead{}
					fr.clocks[key] = ck
				}
				ck.users++
				if o.IsTimestamp {
					ck.tsUsers++
				}
			}
			id := len(slots)
			slots = append(slots, readSlot{})
			plans[i].slots[hi] = id
			items = append(items, readItem{slot: id, o: o, fr: fr})
		}
	}
	for _, it := range items {
		it.fr.pending++
	}
	run(d.opt.readWorkers, len(items), func(i int) {
		items[i].run(slots, d.opt, start, end)
	})
	out := make([]Series, len(sels))
	for i, sel := range sels {
		pieces := make([]piece, 0, len(plans[i].hits))
		for hi, o := range plans[i].hits {
			id := plans[i].slots[hi]
			if id < 0 {
				continue
			}
			if err := slots[id].err; err != nil {
				return nil, fmt.Errorf("dataset: read %s %q/%q: %w", o.File, o.Group, o.Name, err)
			}
			pieces = append(pieces, slots[id].piece)
		}
		merged, err := d.finish(sel, plans[i].hits, pieces, basis)
		if err != nil {
			return nil, err
		}
		out[i] = merged
	}
	return out, nil
}

type readItem struct {
	slot int
	o    Occurrence
	fr   *fileRead
}

func (it readItem) run(slots []readSlot, opt options, start, end time.Time) {
	defer it.fr.done()
	it.fr.open()
	if it.fr.err != nil {
		slots[it.slot].err = it.fr.err
		return
	}
	p, err := readItemPiece(it.fr, it.o, opt, start, end)
	slots[it.slot].piece = p
	slots[it.slot].err = err
}

func (fr *fileRead) open() {
	fr.once.Do(func() {
		fr.f, fr.err = openTDMS(fr.path, fr.segIndex)
	})
}

func (fr *fileRead) done() {
	if atomic.AddInt32(&fr.pending, -1) == 0 && fr.f != nil {
		fr.f.Close()
	}
}

func readItemPiece(fr *fileRead, o Occurrence, opt options, start, end time.Time) (piece, error) {
	g, ok := fr.f.Group(o.Group)
	if !ok {
		return piece{}, ErrNotFound
	}
	ch, ok := g.Channel(o.Name)
	if !ok {
		return piece{}, ErrNotFound
	}
	switch o.Source {
	case TimeWaveform:
		return readWaveform(ch, o, start, end, true)
	case TimeTimestamp:
		return readSharedTimestamp(fr, g, ch, o, opt, start, end)
	default:
		return piece{}, ErrUntimed
	}
}

func readSharedTimestamp(fr *fileRead, g *tdms.Group, ch *tdms.Channel, o Occurrence, opt options, start, end time.Time) (piece, error) {
	name := o.Timestamp
	if o.IsTimestamp {
		name = o.Name
	}
	ck := fr.clocks[o.Group+"\x00"+name]
	if ck == nil {
		return piece{}, ErrNotFound
	}
	ck.once.Do(func() {
		axis := ch
		if !o.IsTimestamp {
			var ok bool
			axis, ok = g.Channel(name)
			if !ok {
				ck.err = ErrNotFound
				return
			}
		}
		ck.times, ck.y, ck.err = loadAxis(axis, opt, ck.tsUsers > 0)
		if ck.err != nil {
			return
		}
		if ck.err = checkMonotonic(ck.times); ck.err != nil {
			return
		}
		ck.i0, ck.i1 = timeWindow(ck.times, start, end, true)
		if d, ok := uniformDt(ck.times); ok {
			ck.dt = d
		}
	})
	if ck.err != nil {
		return piece{}, ck.err
	}
	var y []float64
	if o.IsTimestamp {
		y = ck.y
	} else {
		var err error
		y, err = ch.Float64s()
		if err != nil {
			return piece{}, err
		}
	}
	if len(y) != len(ck.times) {
		return piece{}, ErrArgument
	}
	p := piece{
		file:  o.File,
		dt:    ck.dt,
		timed: true,
		y:     clipFloat(y, ck.i0, ck.i1, o.IsTimestamp && ck.tsUsers > 1),
		times: clipTime(ck.times, ck.i0, ck.i1, ck.users > 1),
	}
	if len(p.times) > 0 {
		p.start = p.times[0]
	}
	return p, nil
}

func readFloatSpan(ch *tdms.Channel, i0, i1 int64) ([]float64, error) {
	if i1 <= i0 {
		return []float64{}, nil
	}
	// Cursor's index is an int, matching Channel.Len. A span that does not
	// fit is rejected instead of narrowing.
	s0, err := indexToInt(i0)
	if err != nil {
		return nil, err
	}
	s1, err := indexToInt(i1)
	if err != nil {
		return nil, err
	}
	// Cursor has no seek and reuses the batch slice on the next Next.
	// Values are copied into out before the next batch. Leading batches are
	// decoded and discarded. The walk stops at i1, so the tail is not read.
	// main does not export per-segment sample bounds, so whole leading
	// segments cannot be skipped from this package.
	out := make([]float64, 0, s1-s0)
	cur := ch.Cursor(8192)
	idx := 0
	for cur.Next() {
		if idx >= s1 {
			break
		}
		batch, err := cur.Float64s()
		if err != nil {
			return nil, err
		}
		if idx+len(batch) <= s0 {
			idx += len(batch)
			continue
		}
		for i, v := range batch {
			abs := idx + i
			if abs < s0 {
				continue
			}
			if abs >= s1 {
				return out, nil
			}
			out = append(out, v)
		}
		idx += len(batch)
		if idx >= s1 {
			break
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func indexToInt(v int64) (int, error) {
	if v < 0 || int64(int(v)) != v {
		return 0, fmt.Errorf("%w: sample index %d", ErrArgument, v)
	}
	return int(v), nil
}

func mergePieces(pieces []piece, policy OverlapPolicy) (Series, error) {
	kept := 0
	var only piece
	for _, p := range pieces {
		if len(p.y) == 0 {
			continue
		}
		kept++
		only = p
	}
	// One file (or a window that hit one file) already owns its slices.
	// Copying them into a new series buffer is a full extra pass.
	if kept == 1 {
		s := Series{Files: []string{only.file}, Y: only.y}
		if only.timed && len(only.times) > 0 {
			s.Times = only.times
			s.Timed = true
			if only.dt > 0 {
				s.Dt = only.dt
			}
		}
		return s, nil
	}
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

// roundedStep is dt rounded to nanoseconds, the same rounding addDt applies.
// ok is false when the rounded step is 0 (a positive dt below 0.5ns, or a
// non-finite dt). A loop that advances with this step would otherwise spin:
// Resample is the only such loop. waveformAt and secondsToTime round one
// absolute instant, and the overlap helpers call addDt once per sample.
// This branch does not call a core TimeAt.
func roundedStep(dt float64) (time.Duration, bool) {
	if math.IsNaN(dt) || math.IsInf(dt, 0) {
		return 0, false
	}
	ns := math.Round(dt * 1e9)
	if math.IsNaN(ns) || math.IsInf(ns, 0) || ns == 0 {
		return 0, false
	}
	if ns >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64), true
	}
	if ns <= float64(math.MinInt64) {
		return time.Duration(math.MinInt64), true
	}
	return time.Duration(int64(ns)), true
}

func addDt(t time.Time, dt float64) time.Time {
	step, ok := roundedStep(dt)
	if !ok {
		return t
	}
	return t.Add(step)
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
// extrapolated. An untimed series returns ErrUntimed. dt must be positive
// and round to at least 1ns. A grid of more than 10 million points returns
// ErrArgument.
//
// The result keeps absolute Times and copies Basis. RelativeTimes is still
// sample time minus that basis, not seconds since the first grid point.
// Y and Times are allocated once each, at the grid length. Points that fall
// outside the series or inside a gap are omitted, so the length can be shorter
// than the capacity. There is no second buffer and no trim copy.
//
// dt is rounded to nanoseconds the same way addDt rounds. A step that rounds
// below 1ns returns ErrArgument. Otherwise the grid loop would not advance,
// and the 10_000_000-point cap would not fire for a short window.
func (s Series) Resample(start, end time.Time, dt float64, method Interp) (Series, error) {
	if !s.Timed {
		return Series{}, errWrap(ErrUntimed, "resample")
	}
	if dt <= 0 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return Series{}, errWrap(ErrArgument, "resample dt")
	}
	step, ok := roundedStep(dt)
	if !ok || step < time.Nanosecond {
		return Series{}, errWrap(ErrArgument, "resample dt")
	}
	if !end.After(start) {
		return Series{}, errWrap(ErrArgument, "resample window")
	}
	n := gridCount(start, end, step)
	if n > 10_000_000 {
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
		Basis:       s.Basis,
	}
	if len(s.Times) == 0 || n == 0 {
		return out, nil
	}
	out.Y = make([]float64, 0, n)
	out.Times = make([]time.Time, 0, n)
	for t := start; t.Before(end); t = t.Add(step) {
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

// gridCount is the number of instants in [start, end) spaced by step.
func gridCount(start, end time.Time, step time.Duration) int64 {
	if step <= 0 {
		return 0
	}
	span := end.Sub(start)
	if span <= 0 {
		return 0
	}
	n := int64(span / step)
	if span%step != 0 {
		n++
	}
	return n
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
