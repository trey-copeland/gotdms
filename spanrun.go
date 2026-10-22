package tdms

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
	ch.spans, ch.runs = coalesceSpans(ch.spans, ch.runs)
	if ch.daqmx == nil || ch.daqmx.spec == nil {
		return
	}
	for i := range ch.daqmx.spans {
		if i == ch.daqmx.spec.primary {
			ch.daqmx.spans[i] = ch.spans
			ch.daqmx.runs[i] = ch.runs
			continue
		}
		ch.daqmx.spans[i], ch.daqmx.runs[i] = coalesceSpans(ch.daqmx.spans[i], ch.daqmx.runs[i])
	}
}

// coalesceSpans merges consecutive spans that share a layout and whose data
// positions advance by a constant step. A string, a live span, or a partial
// tail stays as it is. An existing run keeps its count and step and may
// absorb the single segments that continue it, but is never folded into an
// earlier span. Only version 1 sidecars are coalesced on decode.
func coalesceSpans(in []span, runs []spanRun) ([]span, []spanRun) {
	if len(in) == 0 {
		return in, runs
	}
	if len(in) == 1 {
		in[0] = normRun(in[0])
		return in, runs
	}
	// Two passes so a file that collapses to a handful of runs does not keep
	// a backing array sized to the segment count. The count pass remembers
	// the running step in locals and does not allocate run records.
	n := 1
	cur := normRun(in[0])
	curCount := cur.repeats(runs)
	curStep := cur.repeatStep(runs)
	for i := 1; i < len(in); i++ {
		next := normRun(in[i])
		if cur.nPerChunk != next.nPerChunk || cur.nChunks != next.nChunks || cur.typeSize != next.typeSize || !canExtendState(cur, curCount, curStep, next) {
			n++
			cur = next
			curCount = next.repeats(runs)
			curStep = next.repeatStep(runs)
			continue
		}
		nextCount, err := add64(curCount, 1)
		if err != nil {
			n++
			cur = next
			curCount = next.repeats(runs)
			curStep = next.repeatStep(runs)
			continue
		}
		if curCount <= 1 {
			curStep = next.dataPos - cur.dataPos
		}
		curCount = nextCount
	}
	if n == len(in) {
		for i := range in {
			in[i] = normRun(in[i])
		}
		return in, runs
	}
	out := make([]span, 0, n)
	built := make([]spanRun, 1, n+1)
	// A span that is already a run keeps its count and step: its record
	// moves into the new table. Dropping the index would turn the run back
	// into one segment and lose its other repeats.
	carry := func(sp span) span {
		if sp.runIdx == 0 {
			return sp
		}
		count, step := sp.repeats(runs), sp.repeatStep(runs)
		sp.runIdx = 0
		built = sp.setRun(built, count, step)
		return sp
	}
	cur = carry(normRun(in[0]))
	for i := 1; i < len(in); i++ {
		next := carry(normRun(in[i]))
		if cur.nPerChunk != next.nPerChunk || cur.nChunks != next.nChunks || cur.typeSize != next.typeSize {
			out = append(out, cur)
			cur = next
			continue
		}
		ext, ok := tryExtend(&built, cur, next)
		if !ok {
			out = append(out, cur)
			cur = next
			continue
		}
		cur = ext
	}
	out = append(out, cur)
	return out, built
}

// appendSpan keeps a channel's span slice in run form. A new segment that
// continues the last run bumps that run instead of allocating another span.
func appendSpan(spans []span, runs []spanRun, sp span) ([]span, []spanRun) {
	sp.runIdx = 0
	if len(spans) == 0 {
		return append(spans, sp), runs
	}
	// Sample count is what changes on a file that cannot form a run.
	// Compare in place so a rejected span is not copied before append.
	last := &spans[len(spans)-1]
	if last.nPerChunk != sp.nPerChunk || last.nChunks != sp.nChunks || last.typeSize != sp.typeSize {
		return append(spans, sp), runs
	}
	if last.runIdx != 0 {
		if extendRun(last, runs, sp) {
			return spans, runs
		}
		return append(spans, sp), runs
	}
	ext, ok := tryExtend(&runs, *last, sp)
	if !ok {
		return append(spans, sp), runs
	}
	spans[len(spans)-1] = ext
	return spans, runs
}

// extendRun continues an existing run. The span stays where it is, so a
// uniform file does not copy it on every segment.
func extendRun(last *span, runs []spanRun, next span) bool {
	idx := int(last.runIdx)
	if idx <= 0 || idx >= len(runs) {
		return false
	}
	rec := &runs[idx]
	count := rec.count
	step := rec.step
	if count < 2 || step <= 0 || next.runIdx != 0 {
		return false
	}
	if last.str || next.str || last.live || next.live || last.partialN != 0 || next.partialN != 0 {
		return false
	}
	if !sameLayout(last, &next) {
		return false
	}
	delta, err := mul64(count, step)
	if err != nil {
		return false
	}
	want, err := add64(last.dataPos, delta)
	if err != nil || want != next.dataPos {
		return false
	}
	n, err := add64(count, 1)
	if err != nil {
		return false
	}
	rec.count = n
	return true
}

func (sp *span) repeats(runs []spanRun) int64 {
	if sp == nil || sp.runIdx == 0 || int(sp.runIdx) >= len(runs) {
		return 1
	}
	n := runs[sp.runIdx].count
	if n < 1 {
		return 1
	}
	return n
}

func (sp *span) repeatStep(runs []spanRun) int64 {
	if sp == nil || sp.runIdx == 0 || int(sp.runIdx) >= len(runs) || runs[sp.runIdx].count <= 1 {
		return 0
	}
	return runs[sp.runIdx].step
}

// setRun installs a repeat on sp and returns the table that holds it.
// count <= 1 clears the run.
func (sp *span) setRun(runs []spanRun, count, step int64) []spanRun {
	if sp == nil || count <= 1 {
		if sp != nil {
			sp.runIdx = 0
		}
		return runs
	}
	runs, sp.runIdx = allocRun(runs, count, step)
	return runs
}

// allocRun appends one run record. Index 0 is unused. The first record is
// allocated with room for itself so a channel that collapses to one run
// does not grow the table twice.
func allocRun(runs []spanRun, count, step int64) ([]spanRun, uint32) {
	if len(runs) == 0 {
		runs = make([]spanRun, 1, 2)
	}
	runs = append(runs, spanRun{count: count, step: step})
	return runs, uint32(len(runs) - 1)
}

func normRun(sp span) span {
	if sp.runIdx != 0 {
		return sp
	}
	return sp
}

func tryExtend(runs *[]spanRun, cur, next span) (span, bool) {
	if !canExtend(*runs, cur, next) {
		return span{}, false
	}
	n, err := add64(cur.repeats(*runs), 1)
	if err != nil {
		return span{}, false
	}
	step := cur.repeatStep(*runs)
	if cur.repeats(*runs) <= 1 {
		step = next.dataPos - cur.dataPos
	}
	if cur.runIdx == 0 {
		*runs, cur.runIdx = allocRun(*runs, n, step)
		return cur, true
	}
	(*runs)[cur.runIdx].count = n
	(*runs)[cur.runIdx].step = step
	return cur, true
}

func canExtendState(cur span, curCount, curStep int64, next span) bool {
	if next.runIdx != 0 || curCount < 1 {
		return false
	}
	if cur.str || next.str || cur.live || next.live || cur.partialN != 0 || next.partialN != 0 {
		return false
	}
	if cur.nChunks != next.nChunks || cur.nPerChunk != next.nPerChunk || cur.chunkSize != next.chunkSize || cur.typeSize != next.typeSize {
		return false
	}
	if !sameLayout(&cur, &next) {
		return false
	}
	one, err := mul64(cur.nChunks, cur.nPerChunk)
	if err != nil || one <= 0 {
		return false
	}
	var step int64
	if curCount <= 1 {
		if next.dataPos <= cur.dataPos {
			return false
		}
		step = next.dataPos - cur.dataPos
	} else {
		step = curStep
		if step <= 0 {
			return false
		}
	}
	delta, err := mul64(curCount, step)
	if err != nil {
		return false
	}
	want, err := add64(cur.dataPos, delta)
	if err != nil || want != next.dataPos {
		return false
	}
	return true
}

func canExtend(runs []spanRun, cur, next span) bool {
	if next.repeats(runs) != 1 || cur.repeats(runs) < 1 {
		return false
	}
	if cur.str || next.str || cur.live || next.live || cur.partialN != 0 || next.partialN != 0 {
		return false
	}
	// Chunk geometry is the usual reason a merge fails. Reject it before
	// the overflow-checked multiply so alternating sample counts stay cheap.
	if cur.nChunks != next.nChunks || cur.nPerChunk != next.nPerChunk || cur.chunkSize != next.chunkSize || cur.typeSize != next.typeSize {
		return false
	}
	if !sameLayout(&cur, &next) {
		return false
	}
	one, err := mul64(cur.nChunks, cur.nPerChunk)
	if err != nil || one <= 0 {
		return false
	}
	var step int64
	if cur.repeats(runs) <= 1 {
		if next.dataPos <= cur.dataPos {
			return false
		}
		step = next.dataPos - cur.dataPos
	} else {
		step = cur.repeatStep(runs)
		if step <= 0 {
			return false
		}
	}
	delta, err := mul64(cur.repeats(runs), step)
	if err != nil {
		return false
	}
	want, err := add64(cur.dataPos, delta)
	if err != nil || want != next.dataPos {
		return false
	}
	return true
}

func sameLayout(a, b *span) bool {
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
