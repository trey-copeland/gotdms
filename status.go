package tdms

import "fmt"

// FileStatus describes how an open file ended. It does not change which
// samples Values, Slice, or Cursor return. A file that Open rejects (a
// next-segment offset past EOF without WithLenientTail, a short trailing
// lead-in, a bad tag) has no FileStatus; Open still returns that error.
//
// Complete, Incomplete, and Truncated are mutually exclusive for a file that
// opened. Incomplete means the final lead-in uses the NI incomplete-segment
// sentinel (next-segment offset 0xFFFFFFFFFFFFFFFF). Writers publish that
// sentinel while a segment is in progress and patch it when the segment is
// finished. A crash leaves the sentinel in place. Whole values in that
// segment are still readable, which is the behavior Open has always had.
//
// Truncated means WithLenientTail recovered the file's end in one of two
// ways. Either a final segment's finite next-segment offset runs past EOF
// (whole values in that tail are readable, and are not committed: Follow
// waits until a later open sees the declared length), or bytes after the
// last whole segment do not parse and were dropped: a torn tail. A torn tail
// is what a power loss or crash leaves while a segment is being written,
// for example a partial lead-in, zero-filled blocks, a lead-in whose
// metadata runs past EOF, or garbage. DroppedTail is its length and
// TailError the error a strict Open returns for it. Every whole segment
// before it is read normally. A segment whose finite length lies within
// the file, and an ErrUnsupported anywhere, are never dropped; see
// WithLenientTail. Truncating the file to its size minus
// DroppedTail removes the torn bytes, and the file then opens strictly.
type FileStatus struct {
	// Complete is true when every parsed segment has a finite next-segment
	// offset and the file ends on the last segment's boundary.
	Complete bool
	// Incomplete is true when the final segment uses the incomplete-segment
	// sentinel. PartialTail and UnusedTail describe that segment.
	Incomplete bool
	// Truncated is true when WithLenientTail kept a final segment whose
	// next-segment offset runs past EOF. PartialTail and UnusedTail describe
	// that segment the same way they describe an incomplete sentinel.
	Truncated bool
	// PartialTail is true when the final incomplete or truncated segment's
	// raw bytes do not end on a whole chunk boundary. Whole values in the
	// short chunk are included in Channel.Len.
	PartialTail bool
	// UnusedTail is the number of raw bytes at the end of an incomplete or
	// truncated segment that were not consumed as whole values. It is 0 when
	// Complete is true, and when that segment ends on a value boundary.
	UnusedTail int64
	// DroppedTail is the number of bytes after the last whole segment that
	// WithLenientTail dropped because they are not a parseable segment. It
	// is 0 unless Truncated is true; a torn tail and a past-EOF final
	// segment cannot both occur.
	DroppedTail int64
	// TailError is the error a strict Open returns for the dropped bytes,
	// such as ErrTruncated (a partial lead-in, or metadata past EOF),
	// ErrBadTag (zeros or garbage), ErrVersion, or ErrCorrupt (metadata of
	// a segment that never got its length). It is never ErrUnsupported. It
	// is nil when DroppedTail is 0.
	TailError error
}

// String returns "complete", "incomplete", or "truncated".
func (s FileStatus) String() string {
	if s.Incomplete {
		return "incomplete"
	}
	if s.Truncated {
		return "truncated"
	}
	if s.Complete {
		return "complete"
	}
	return "unknown"
}

// Status reports whether f ends on a finished segment or on the NI
// incomplete-segment sentinel. A nil File reports the zero FileStatus.
func (f *File) Status() FileStatus {
	if f == nil {
		return FileStatus{}
	}
	return FileStatus{
		Complete:    !f.incomplete && !f.truncated,
		Incomplete:  f.incomplete,
		Truncated:   f.truncated,
		PartialTail: f.partialTail,
		UnusedTail:  f.unusedTail,
		DroppedTail: f.droppedTail,
		TailError:   f.tailErr,
	}
}

// GoString formats s for tests and %#v dumps.
func (s FileStatus) GoString() string {
	return fmt.Sprintf("FileStatus{Complete:%v Incomplete:%v Truncated:%v PartialTail:%v UnusedTail:%d DroppedTail:%d TailError:%v}",
		s.Complete, s.Incomplete, s.Truncated, s.PartialTail, s.UnusedTail, s.DroppedTail, s.TailError)
}

// committed is the number of samples that sit in finished segments.
// Samples from an incomplete sentinel, and samples recovered by
// WithLenientTail from a final segment whose next offset runs past EOF,
// are omitted. Channel.Len still counts those samples.
func (c *Channel) committed() int64 {
	if c == nil {
		return 0
	}
	var n int64
	for i := range c.spans {
		sp := &c.spans[i]
		if sp.live {
			continue
		}
		s, err := spanSamples(sp, c.runs)
		if err != nil {
			return 0
		}
		n, err = add64(n, s)
		if err != nil {
			return 0
		}
	}
	return n
}
