package tdms

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

// encodeSegmentIndexV1 writes a version 1 sidecar with one span per segment.
func encodeSegmentIndexV1(f *File, size, mtime int64, dev, ino uint64, fp uint32) ([]byte, error) {
	payload, err := encodeIndexPayloadV1(f)
	if err != nil {
		return nil, err
	}
	return finishSegmentIndex(f, payload, size, mtime, dev, ino, fp, segmentIndexV1)
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
			spans, err := expandRuns(ch.spans, ch.runs)
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
					p.dx[i], err = expandRuns(ch.daqmx.spans[i], ch.daqmx.runs[i])
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

func expandRuns(in []span, runs []spanRun) ([]span, error) {
	out := make([]span, 0, len(in))
	for _, sp := range in {
		reps := sp.repeats(runs)
		step := sp.repeatStep(runs)
		for i := int64(0); i < reps; i++ {
			one := sp
			if i > 0 {
				shift, err := mul64(i, step)
				if err != nil {
					return nil, err
				}
				pos, err := add64(sp.dataPos, shift)
				if err != nil {
					return nil, err
				}
				one.dataPos = pos
			}
			one.runIdx = 0
			out = append(out, one)
		}
	}
	return out, nil
}
