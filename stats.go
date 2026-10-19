package tdms

import (
	"fmt"
	"math"
)

// ChunkStat is the count, minimum, maximum, mean, and RMS of one chunk of a
// numeric channel. RMS is the square root of the mean of the squared samples
// (the population RMS, not a sample standard deviation).
//
// Values are accumulated as float64, the same conversion Channel.Float64s
// uses. Integers wider than 2^53 are not exact. Extended floats use
// Extended.Float64. A NaN sample makes Min, Max, Mean, and RMS NaN. Count
// still includes that sample. +Inf and -Inf participate in the usual way.
type ChunkStat struct {
	// Index is the zero-based chunk number.
	Index int
	// Start is the index of the first sample in this chunk.
	Start int
	// Count is the number of samples. The last chunk may be shorter than the
	// size passed to ChunkStats. Count is at least 1.
	Count int
	Min   float64
	Max   float64
	Mean  float64
	RMS   float64
}

// ChunkStatCursor walks a numeric channel one chunk at a time. It uses Cursor
// and does not retain samples from earlier chunks. The Stat value is valid
// until the next Next call.
type ChunkStatCursor struct {
	cur   *Cursor
	index int
	start int
	stat  ChunkStat
	err   error
	done  bool
}

// ChunkStats starts a single streaming pass that reports one ChunkStat per
// chunk of samplesPerChunk samples. samplesPerChunk must be positive. The
// final chunk is shorter when Len is not a multiple of the size.
//
// The channel must be numeric: an integer, a float, a float-with-unit, or an
// extended float. String, boolean, timestamp, complex, and void channels
// return ErrType. The decision uses DataType, not the channel name.
func (c *Channel) ChunkStats(samplesPerChunk int) (*ChunkStatCursor, error) {
	if c == nil {
		return nil, wrap("read", "", fmt.Errorf("%w: nil channel", ErrArgument))
	}
	if c.file == nil || c.file.r == nil {
		return nil, wrap("read", c.path, ErrClosed)
	}
	if samplesPerChunk <= 0 {
		return nil, wrap("read", c.path, fmt.Errorf("%w: chunk size must be positive", ErrArgument))
	}
	if !statsNumeric(c.dt) {
		return nil, wrap("read", c.path, fmt.Errorf("%w: chunk statistics require a numeric channel, not %s", ErrType, c.dt))
	}
	return &ChunkStatCursor{cur: c.Cursor(samplesPerChunk)}, nil
}

func statsNumeric(dt DataType) bool {
	switch dt.storage() {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64,
		TypeUint8, TypeUint16, TypeUint32, TypeUint64,
		TypeFloat32, TypeFloat64, TypeExtendedFloat:
		return true
	default:
		return false
	}
}

// Next computes the next chunk. It returns false at the end or after an error.
// Err distinguishes those two. Close is optional; Next releases the underlying
// cursor when the scan finishes or fails.
func (s *ChunkStatCursor) Next() bool {
	if s == nil || s.err != nil || s.done || s.cur == nil {
		return false
	}
	if !s.cur.Next() {
		s.done = true
		s.err = s.cur.Err()
		s.cur.Close()
		return false
	}
	var a chunkAcc
	if err := a.add(s.cur.Values()); err != nil {
		path := ""
		if s.cur.ch != nil {
			path = s.cur.ch.path
		}
		s.err = wrap("read", path, err)
		s.done = true
		s.cur.Close()
		return false
	}
	st := a.stat()
	st.Index = s.index
	st.Start = s.start
	s.stat = st
	s.index++
	s.start += st.Count
	return true
}

// Stat returns the chunk computed by the last successful Next.
func (s *ChunkStatCursor) Stat() ChunkStat {
	if s == nil {
		return ChunkStat{}
	}
	return s.stat
}

// Err returns the error from the last Next, or nil.
func (s *ChunkStatCursor) Err() error {
	if s == nil {
		return nil
	}
	return s.err
}

// Close releases the underlying cursor. Next after Close returns false.
// Close is safe to call more than once.
func (s *ChunkStatCursor) Close() {
	if s == nil || s.cur == nil {
		return
	}
	s.cur.Close()
	s.done = true
}

type chunkAcc struct {
	n          int
	sum, sumsq float64
	min, max   float64
	nan        bool
	any        bool
}

func (a *chunkAcc) add(v any) error {
	switch s := v.(type) {
	case []float64:
		for _, x := range s {
			a.push(x)
		}
	case []float32:
		for _, x := range s {
			a.push(float64(x))
		}
	case []int8:
		for _, x := range s {
			a.push(float64(x))
		}
	case []int16:
		for _, x := range s {
			a.push(float64(x))
		}
	case []int32:
		for _, x := range s {
			a.push(float64(x))
		}
	case []int64:
		for _, x := range s {
			a.push(float64(x))
		}
	case []uint8:
		for _, x := range s {
			a.push(float64(x))
		}
	case []uint16:
		for _, x := range s {
			a.push(float64(x))
		}
	case []uint32:
		for _, x := range s {
			a.push(float64(x))
		}
	case []uint64:
		for _, x := range s {
			a.push(float64(x))
		}
	case []Extended:
		for _, x := range s {
			a.push(x.Float64())
		}
	default:
		return fmt.Errorf("%w: cannot accumulate %T", ErrType, v)
	}
	return nil
}

func (a *chunkAcc) push(x float64) {
	if !a.any {
		a.min = x
		a.max = x
		a.any = true
	}
	a.n++
	a.sum += x
	a.sumsq += x * x
	if math.IsNaN(x) {
		a.nan = true
		return
	}
	if math.IsNaN(a.min) || x < a.min {
		a.min = x
	}
	if math.IsNaN(a.max) || x > a.max {
		a.max = x
	}
}

func (a chunkAcc) stat() ChunkStat {
	st := ChunkStat{Count: a.n, Min: a.min, Max: a.max}
	if a.n == 0 {
		return st
	}
	nf := float64(a.n)
	st.Mean = a.sum / nf
	st.RMS = math.Sqrt(a.sumsq / nf)
	if a.nan {
		st.Min = math.NaN()
		st.Max = math.NaN()
	}
	return st
}
