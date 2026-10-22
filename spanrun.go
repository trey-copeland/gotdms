package tdms

// coalesceAll folds each channel's consecutive regular spans into runs.
// Values, Slice, and Cursor walk a run in place. They do not allocate one
// span per segment.
func (f *File) coalesceAll() {
	if f == nil {
		return
	}
	for _, g := range f.groups {
		for _, ch := range g.channels {
			coalesceChannel(ch)
		}
	}
}

func (img *indexImage) coalesce() {
	if img == nil {
		return
	}
	for _, g := range img.groups {
		for _, ch := range g.channels {
			coalesceChannel(ch)
		}
	}
}

func coalesceChannel(ch *Channel) {
	if ch == nil {
		return
	}
	ch.spans = coalesceSpans(ch.spans)
	if ch.daqmx == nil || ch.daqmx.spec == nil {
		return
	}
	for i := range ch.daqmx.spans {
		if i == ch.daqmx.spec.primary {
			ch.daqmx.spans[i] = ch.spans
			continue
		}
		ch.daqmx.spans[i] = coalesceSpans(ch.daqmx.spans[i])
	}
}

// coalesceSpans merges consecutive spans that share a layout and whose data
// positions advance by a constant step. A string, a live span, a partial
// tail, or a span that is already a run stays as it is: a run of runs is not
// folded again, so decoding a version 2 sidecar does not rebuild it.
func coalesceSpans(in []span) []span {
	if len(in) == 0 {
		return in
	}
	if len(in) == 1 {
		if in[0].runCount < 1 {
			in[0].runCount = 1
		}
		return in
	}
	// Two passes so a file that collapses to a handful of runs does not keep
	// a backing array sized to the segment count.
	n := 1
	cur := normRun(in[0])
	for i := 1; i < len(in); i++ {
		next := normRun(in[i])
		ext, ok := tryExtend(cur, next)
		if !ok {
			n++
			cur = next
			continue
		}
		cur = ext
	}
	if n == len(in) {
		for i := range in {
			if in[i].runCount < 1 {
				in[i].runCount = 1
			}
		}
		return in
	}
	out := make([]span, 0, n)
	cur = normRun(in[0])
	for i := 1; i < len(in); i++ {
		next := normRun(in[i])
		ext, ok := tryExtend(cur, next)
		if !ok {
			out = append(out, cur)
			cur = next
			continue
		}
		cur = ext
	}
	out = append(out, cur)
	return out
}

// appendSpan keeps a channel's span slice in run form. A new segment that
// continues the last run bumps that run instead of allocating another span.
func appendSpan(spans []span, sp span) []span {
	sp = normRun(sp)
	if len(spans) == 0 {
		return append(spans, sp)
	}
	ext, ok := tryExtend(spans[len(spans)-1], sp)
	if !ok {
		return append(spans, sp)
	}
	spans[len(spans)-1] = ext
	return spans
}

func normRun(sp span) span {
	if sp.runCount < 1 {
		sp.runCount = 1
	}
	return sp
}

func tryExtend(cur, next span) (span, bool) {
	n, err := add64(cur.runCount, 1)
	if err != nil || !canExtend(cur, next) {
		return span{}, false
	}
	if cur.runCount <= 1 {
		cur.runStep = next.dataPos - cur.dataPos
	}
	cur.runCount = n
	return cur, true
}

func canExtend(cur, next span) bool {
	if next.runCount != 1 || cur.runCount < 1 {
		return false
	}
	if cur.str || next.str || cur.live || next.live || cur.partialN != 0 || next.partialN != 0 {
		return false
	}
	one, err := mul64(cur.nChunks, cur.nPerChunk)
	if err != nil || one <= 0 {
		return false
	}
	if !sameLayout(cur, next) {
		return false
	}
	var step int64
	if cur.runCount <= 1 {
		if next.dataPos <= cur.dataPos {
			return false
		}
		step = next.dataPos - cur.dataPos
	} else {
		step = cur.runStep
		if step <= 0 {
			return false
		}
	}
	delta, err := mul64(cur.runCount, step)
	if err != nil {
		return false
	}
	want, err := add64(cur.dataPos, delta)
	if err != nil || want != next.dataPos {
		return false
	}
	return true
}

func sameLayout(a, b span) bool {
	return a.chunkSize == b.chunkSize &&
		a.nChunks == b.nChunks &&
		a.nPerChunk == b.nPerChunk &&
		a.col == b.col &&
		a.stride == b.stride &&
		a.typeSize == b.typeSize &&
		a.big == b.big &&
		a.str == b.str &&
		a.strSize == b.strSize &&
		a.digital == b.digital &&
		a.bit == b.bit &&
		a.solo == b.solo &&
		a.partialN == b.partialN &&
		a.partialPos == b.partialPos &&
		a.partialCol == b.partialCol &&
		a.partialStride == b.partialStride &&
		a.live == b.live
}

// encodeIndexPayloadV1 writes the version 1 payload: one 96-byte span per
// segment. Runs are expanded for the duration of the encode and then restored.
func encodeIndexPayloadV1(f *File) ([]byte, error) {
	restore, err := expandAllRuns(f)
	if err != nil {
		return nil, err
	}
	defer restore()
	return encodeIndexPayload(f, false)
}

func expandAllRuns(f *File) (func(), error) {
	type snap struct {
		ch    *Channel
		spans []span
		dx    [][]span
	}
	type pending struct {
		ch      *Channel
		spans   []span
		dx      [][]span
		primary int
	}
	var saved []snap
	var next []pending
	for _, g := range f.groups {
		if g == nil {
			continue
		}
		for _, ch := range g.channels {
			if ch == nil {
				continue
			}
			s := snap{ch: ch, spans: ch.spans}
			if ch.daqmx != nil {
				s.dx = append([][]span(nil), ch.daqmx.spans...)
			}
			spans, err := expandRuns(ch.spans)
			if err != nil {
				return nil, err
			}
			p := pending{ch: ch, spans: spans, primary: -1}
			if ch.daqmx != nil && ch.daqmx.spec != nil {
				p.primary = ch.daqmx.spec.primary
				p.dx = make([][]span, len(ch.daqmx.spans))
				for i := range ch.daqmx.spans {
					if i == p.primary {
						continue
					}
					p.dx[i], err = expandRuns(ch.daqmx.spans[i])
					if err != nil {
						return nil, err
					}
				}
			}
			saved = append(saved, s)
			next = append(next, p)
		}
	}
	for _, p := range next {
		p.ch.spans = p.spans
		if p.ch.daqmx == nil || p.dx == nil {
			continue
		}
		for i := range p.dx {
			if i == p.primary {
				p.ch.daqmx.spans[i] = p.ch.spans
				continue
			}
			if p.dx[i] != nil {
				p.ch.daqmx.spans[i] = p.dx[i]
			}
		}
	}
	return func() {
		for _, s := range saved {
			s.ch.spans = s.spans
			if s.ch.daqmx != nil {
				s.ch.daqmx.spans = s.dx
			}
		}
	}, nil
}

func expandRuns(in []span) ([]span, error) {
	out := make([]span, 0, len(in))
	for _, sp := range in {
		reps := int64(1)
		if sp.runCount > 1 {
			reps = sp.runCount
		}
		for i := int64(0); i < reps; i++ {
			one := sp
			if i > 0 {
				shift, err := mul64(i, sp.runStep)
				if err != nil {
					return nil, err
				}
				pos, err := add64(sp.dataPos, shift)
				if err != nil {
					return nil, err
				}
				one.dataPos = pos
			}
			one.runCount = 1
			one.runStep = 0
			out = append(out, one)
		}
	}
	return out, nil
}
