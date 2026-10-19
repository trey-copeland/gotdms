package tdms

import "fmt"

// FileStatus describes how an open file ended. It does not change which
// samples Values, Slice, or Cursor return. A file that Open rejects (a
// next-segment offset that runs past EOF, a short trailing lead-in, a bad
// tag) has no FileStatus; Open still returns that error.
//
// Complete and Incomplete are mutually exclusive for a file that opened.
// Incomplete means the final lead-in uses the NI incomplete-segment sentinel
// (next-segment offset 0xFFFFFFFFFFFFFFFF). Writers publish that sentinel
// while a segment is in progress and patch it when the segment is finished.
// A crash leaves the sentinel in place. Whole values in that segment are
// still readable, which is the behavior Open has always had.
type FileStatus struct {
	// Complete is true when every parsed segment has a finite next-segment
	// offset and the file ends on the last segment's boundary.
	Complete bool
	// Incomplete is true when the final segment uses the incomplete-segment
	// sentinel. PartialTail and UnusedTail describe that segment.
	Incomplete bool
	// PartialTail is true when the incomplete segment's raw bytes do not end
	// on a whole chunk boundary. Whole values in the short chunk are included
	// in Channel.Len.
	PartialTail bool
	// UnusedTail is the number of raw bytes at the end of an incomplete
	// segment that were not consumed as whole values. It is 0 when Complete
	// is true, and when an incomplete segment ends on a value boundary.
	UnusedTail int64
}

// String returns "complete" or "incomplete".
func (s FileStatus) String() string {
	if s.Incomplete {
		return "incomplete"
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
		Complete:    !f.incomplete,
		Incomplete:  f.incomplete,
		PartialTail: f.partialTail,
		UnusedTail:  f.unusedTail,
	}
}

// GoString formats s for tests and %#v dumps.
func (s FileStatus) GoString() string {
	return fmt.Sprintf("FileStatus{Complete:%v Incomplete:%v PartialTail:%v UnusedTail:%d}",
		s.Complete, s.Incomplete, s.PartialTail, s.UnusedTail)
}

// committed is the number of samples that sit in finished segments.
// Samples that belong to a span from the incomplete final segment are omitted.
// Channel.Len still counts those samples.
func (c *Channel) committed() int64 {
	if c == nil {
		return 0
	}
	var n int64
	for _, sp := range c.spans {
		if sp.live {
			continue
		}
		n += sp.nChunks*sp.nPerChunk + sp.partialN
	}
	return n
}
