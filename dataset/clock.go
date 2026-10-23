package dataset

import (
	"math"
	"time"
)

// Unix nanoseconds cover these instants; outside them a series keeps Times.
var (
	minNsTime = time.Unix(0, math.MinInt64)
	maxNsTime = time.Unix(0, math.MaxInt64)
)

// timesKey identifies a decoded timestamp clock by its backing array.
// readSharedTimestamp gives every user of one clock the same slice, so equal
// keys mean the same samples.
type timesKey struct {
	p *time.Time
	n int
}

// waveKey identifies a waveform axis: equal keys produce equal times, so the
// series can share one Clock even across files.
type waveKey struct {
	sec  int64
	nsec int
	inc  uint64 // math.Float64bits of wf_increment
	i0   int64
	n    int64
}

// clockCache is the per-call time handling of one Read, ReadWindow, or
// ReadMany call. A nil cache means neither WithSharedClock nor WithoutTimes
// was passed: pieces carry Times. Otherwise waveform pieces are lazy and
// timestamp pieces alias the decoded axis. noTimes (WithoutTimes) merges
// without building any time axis; else the maps dedupe Clocks. finish runs
// on one goroutine, so the maps need no lock.
type clockCache struct {
	noTimes bool
	byTimes map[timesKey]*Clock
	byWave  map[waveKey]*Clock
}

// merge is mergePieces plus the shared clock: a single piece becomes a
// shared Clock, several pieces are merged as before and the result converted.
func (cc *clockCache) merge(pieces []piece, policy OverlapPolicy) (Series, error) {
	if cc == nil {
		return mergePieces(pieces, policy, true)
	}
	if cc.noTimes {
		return mergePieces(pieces, policy, false)
	}
	var only *piece
	kept := 0
	for i := range pieces {
		if len(pieces[i].y) > 0 {
			kept++
			only = &pieces[i]
		}
	}
	if kept == 1 && only.wave != nil {
		if c := cc.wave(only); c != nil {
			s := Series{Files: []string{only.file}, Y: only.y, Timed: true, Clock: c}
			if only.dt > 0 {
				s.Dt = only.dt
			}
			return s, nil
		}
	}
	for i := range pieces {
		if pieces[i].wave == nil {
			continue
		}
		times, err := waveTimes(&pieces[i])
		if err != nil {
			return Series{}, err
		}
		pieces[i].times, pieces[i].wave = times, nil
	}
	s, err := mergePieces(pieces, policy, true)
	if err != nil || !s.Timed || len(s.Times) == 0 {
		return s, err
	}
	if c := cc.times(s.Times); c != nil {
		s.Clock, s.Times = c, nil
		return s, nil
	}
	// Out of nanosecond range. The slice may alias a clock other series
	// share, and Series.Times is owned by its series, so copy it.
	s.Times = append([]time.Time(nil), s.Times...)
	return s, nil
}

func (cc *clockCache) times(ts []time.Time) *Clock {
	k := timesKey{&ts[0], len(ts)}
	if c, ok := cc.byTimes[k]; ok {
		return c
	}
	ns := make([]int64, len(ts))
	for i, t := range ts {
		if t.Before(minNsTime) || t.After(maxNsTime) {
			return nil
		}
		ns[i] = t.UnixNano()
	}
	c := &Clock{Ns: ns}
	cc.byTimes[k] = c
	return c
}

func (cc *clockCache) wave(p *piece) *Clock {
	k := *p.wave
	if c, ok := cc.byWave[k]; ok {
		return c
	}
	ns := make([]int64, k.n)
	inc := math.Float64frombits(k.inc)
	for i := range ns {
		t, err := waveformAt(p.waveT0, inc, k.i0+int64(i))
		if err != nil || t.Before(minNsTime) || t.After(maxNsTime) {
			return nil
		}
		ns[i] = t.UnixNano()
	}
	c := &Clock{Ns: ns}
	cc.byWave[k] = c
	return c
}

// waveTimes builds the Times a lazy waveform piece skipped.
func waveTimes(p *piece) ([]time.Time, error) {
	k := *p.wave
	inc := math.Float64frombits(k.inc)
	out := make([]time.Time, k.n)
	for i := range out {
		t, err := waveformAt(p.waveT0, inc, k.i0+int64(i))
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}
