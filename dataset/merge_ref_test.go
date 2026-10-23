package dataset

import (
	"math"
	"time"
)

// refMergePieces is mergePieces as it was before WithoutTimes: one time, dt,
// and file per kept sample. The differential tests hold the run-based merge
// to it.
func refMergePieces(pieces []piece, policy OverlapPolicy) (Series, error) {
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
						refTrimGaps(&gaps, times)
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

func refTrimGaps(gaps *[]Gap, times []time.Time) {
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
