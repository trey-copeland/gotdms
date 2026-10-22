package tdms

import (
	"fmt"
	"io"
	"math"
)

// splitWindow is the multiplexed byte window ReadValues holds while splitting
// interleaved channels. It stays bounded so a multi-GB span is never pinned.
// 128 KiB won a sweep from 32 KiB through 32 MiB: each channel's destination
// write stays in cache, and 4- and 32-channel reads both beat N separate
// passes. Larger windows (1 MiB and up) made those destination writes miss
// and were slower.
var splitWindow = 128 << 10

// ReadValues reads each channel's raw values. results[i] has the same
// concrete type as channels[i].Values().
//
// Channels must belong to one file. Fixed-size channels that share an
// interleaved segment are split from each multiplexed window once (128 KiB),
// so N channels move those bytes once instead of N times. A request
// that names only one channel of an interleaved segment uses the same path
// as Values. Contiguous channels are read with the same ReadAt path as
// Values. String channels are decoded from their own offset tables; the
// format does not byte-interleave strings with other channels (see
// ErrLayout), so they are not part of the shared window.
//
// The result slices are newly allocated. Group.Values reads every channel
// in the group.
func ReadValues(channels []*Channel) ([]any, error) {
	if len(channels) == 0 {
		return nil, nil
	}
	var file *File
	for _, c := range channels {
		if c == nil {
			return nil, wrap("read", "", fmt.Errorf("%w: nil channel", ErrArgument))
		}
		if file == nil {
			file = c.file
		}
		if c.file == nil || c.file != file {
			return nil, wrap("read", c.path, fmt.Errorf("%w: channels must belong to one file", ErrArgument))
		}
	}
	if file.r == nil {
		return nil, wrap("read", "", ErrClosed)
	}
	out := make([]any, len(channels))
	groups := map[ileaveKey][]ileaveMember{}
	for i, c := range channels {
		if !c.hasDT {
			if c.total == 0 {
				continue
			}
			return nil, wrap("read", c.path, ErrCorrupt)
		}
		if c.dt == TypeString || c.dt.storage() == TypeString {
			if c.total == 0 {
				out[i] = []string{}
				continue
			}
			s, err := c.readStrings(file.r, 0, int(c.total))
			if err != nil {
				return nil, wrap("read", c.path, err)
			}
			out[i] = s
			continue
		}
		if c.total > int64(math.MaxInt) {
			return nil, wrap("read", c.path, fmt.Errorf("%w: channel longer than int", ErrUnsupported))
		}
		dst, err := makeValues(c.dt, int(c.total))
		if err != nil {
			return nil, wrap("read", c.path, err)
		}
		out[i] = dst
		sample := 0
		for si := range c.spans {
			sp := &c.spans[si]
			cnt64, err := spanSamples(sp)
			if err != nil {
				return nil, wrap("read", c.path, err)
			}
			if cnt64 > int64(math.MaxInt-sample) {
				return nil, wrap("read", c.path, ErrCorrupt)
			}
			cnt := int(cnt64)
			if interleavedSpan(sp) {
				groups[ileaveKeyOf(sp)] = append(groups[ileaveKeyOf(sp)], ileaveMember{
					req: i, ch: c, sp: sp, sampleBase: sample,
				})
			} else if err := c.readSpanInto(file.r, sp, dst, sample, cnt); err != nil {
				return nil, wrap("read", c.path, err)
			}
			sample += cnt
		}
		if int64(sample) != c.total {
			return nil, wrap("read", c.path, ErrCorrupt)
		}
	}
	for _, members := range groups {
		if len(members) < 2 {
			m := members[0]
			cnt64, err := spanSamples(m.sp)
			if err != nil {
				return nil, wrap("read", m.ch.path, err)
			}
			if err := m.ch.readSpanInto(file.r, m.sp, out[m.req], m.sampleBase, int(cnt64)); err != nil {
				return nil, wrap("read", m.ch.path, err)
			}
			continue
		}
		if err := splitInterleaved(file.r, members, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Values reads every channel in the group, in first-appearance order.
// See ReadValues. String channels are included and decoded on their own.
func (g *Group) Values() ([]any, error) {
	if g == nil || g.file == nil {
		return nil, wrap("read", "", fmt.Errorf("%w: nil group", ErrArgument))
	}
	if g.file.r == nil {
		return nil, wrap("read", g.Path(), ErrClosed)
	}
	return ReadValues(g.channels)
}

type ileaveKey struct {
	dataPos, chunkSize, stride int64
	nChunks, nPerChunk         int64
	partialPos, partialStride  int64
	partialN                   int64
	big                        bool
}

type ileaveMember struct {
	req        int
	ch         *Channel
	sp         *span
	sampleBase int
}

func ileaveKeyOf(sp *span) ileaveKey {
	return ileaveKey{
		dataPos: sp.dataPos, chunkSize: sp.chunkSize, stride: sp.stride,
		nChunks: sp.nChunks, nPerChunk: sp.nPerChunk,
		partialPos: sp.partialPos, partialStride: sp.partialStride, partialN: sp.partialN,
		big: sp.big,
	}
}

func interleavedSpan(sp *span) bool {
	if sp == nil || sp.str || sp.digital || sp.solo || sp.typeSize <= 0 {
		return false
	}
	if sp.stride <= int64(sp.typeSize) || sp.col < 0 || int64(sp.typeSize) > sp.stride-sp.col {
		return false
	}
	return true
}

func spanSamples(sp *span) (int64, error) {
	return add64(mustMul(sp.nChunks, sp.nPerChunk), sp.partialN)
}

func (c *Channel) readSpanInto(r io.ReaderAt, sp *span, dst any, dstSample, n int) error {
	if n == 0 {
		return nil
	}
	if sp.str {
		return fmt.Errorf("%w: string span in fixed read", ErrCorrupt)
	}
	sz := sp.typeSize
	if sz <= 0 {
		return ErrCorrupt
	}
	if raw, ok := overlayAt(dst, dstSample, n, sz); ok && fixedNativeDT(c.dt) {
		if err := sp.copyValues(r, 0, int64(n), raw); err != nil {
			return err
		}
		normalize(c.dt, raw, sp.big)
		return nil
	}
	buf := make([]byte, n*sz)
	if err := sp.copyValues(r, 0, int64(n), buf); err != nil {
		return err
	}
	normalize(c.dt, buf, sp.big)
	return decodeInto(c.dt, buf, dst, dstSample)
}

func splitInterleaved(r io.ReaderAt, members []ileaveMember, out []any) error {
	sp0 := members[0].sp
	if sp0.stride <= 0 || sp0.nPerChunk < 0 {
		return wrap("read", members[0].ch.path, ErrCorrupt)
	}
	for _, m := range members[1:] {
		if m.sp.nChunks != sp0.nChunks || m.sp.nPerChunk != sp0.nPerChunk || m.sp.stride != sp0.stride || m.sp.partialN != sp0.partialN || m.sp.dataPos != sp0.dataPos {
			return wrap("read", m.ch.path, ErrCorrupt)
		}
	}
	for chunk := int64(0); chunk < sp0.nChunks; chunk++ {
		base, err := add64(sp0.dataPos, mustMul(chunk, sp0.chunkSize))
		if err != nil {
			return wrap("read", members[0].ch.path, err)
		}
		off, err := mul64(chunk, sp0.nPerChunk)
		if err != nil || off > int64(math.MaxInt) {
			return wrap("read", members[0].ch.path, ErrCorrupt)
		}
		if err := splitRows(r, members, out, base, sp0.stride, sp0.nPerChunk, int(off)); err != nil {
			return err
		}
	}
	if sp0.partialN > 0 {
		off, err := mul64(sp0.nChunks, sp0.nPerChunk)
		if err != nil || off > int64(math.MaxInt) {
			return wrap("read", members[0].ch.path, ErrCorrupt)
		}
		stride := sp0.partialStride
		if stride <= 0 {
			stride = sp0.stride
		}
		if err := splitRows(r, members, out, sp0.partialPos, stride, sp0.partialN, int(off)); err != nil {
			return err
		}
	}
	return nil
}

func splitRows(r io.ReaderAt, members []ileaveMember, out []any, region, stride, count int64, spanAt int) error {
	if count == 0 {
		return nil
	}
	if stride <= 0 {
		return wrap("read", members[0].ch.path, ErrCorrupt)
	}
	window := splitWindow
	if window < int(stride) {
		window = int(stride)
	}
	maxTake := int64(window) / stride
	if maxTake < 1 {
		maxTake = 1
	}
	// One scratch for every window of this span. Returning it to the pool
	// each iteration just to borrow it again adds a lock and drops the
	// buffer from cache. The window stays bounded (splitWindow).
	var scratch []byte
	start := region
	var done int64
	for done < count {
		take := count - done
		if take > maxTake {
			take = maxTake
		}
		spanBytes, err := mul64(take, stride)
		if err != nil || spanBytes > int64(math.MaxInt) {
			return wrap("read", members[0].ch.path, ErrCorrupt)
		}
		if cap(scratch) < int(spanBytes) {
			scratch = make([]byte, spanBytes)
		} else {
			scratch = scratch[:spanBytes]
		}
		if err = readFull(r, scratch, start); err != nil {
			return wrap("read", members[0].ch.path, err)
		}
		at := spanAt + int(done)
		for _, m := range members {
			if err := scatter(m, out[m.req], scratch, int(stride), at, int(take)); err != nil {
				return wrap("read", m.ch.path, err)
			}
		}
		step, err := mul64(take, stride)
		if err != nil {
			return wrap("read", members[0].ch.path, err)
		}
		start, err = add64(start, step)
		if err != nil {
			return wrap("read", members[0].ch.path, err)
		}
		done += take
	}
	return nil
}

func scatter(m ileaveMember, dst any, buf []byte, stride, dstSample, n int) error {
	if n == 0 {
		return nil
	}
	col := int(m.sp.col)
	sz := m.sp.typeSize
	if col < 0 || sz <= 0 || stride < col+sz || len(buf) < n*stride {
		return ErrCorrupt
	}
	src := buf[col:]
	if raw, ok := overlayAt(dst, dstSample, n, sz); ok && fixedNativeDT(m.ch.dt) {
		deinterleave(raw, src, sz, stride, n)
		normalize(m.ch.dt, raw, m.sp.big)
		return nil
	}
	tmp := make([]byte, n*sz)
	deinterleave(tmp, src, sz, stride, n)
	normalize(m.ch.dt, tmp, m.sp.big)
	return decodeInto(m.ch.dt, tmp, dst, dstSample)
}

func makeValues(dt DataType, n int) (any, error) {
	switch dt.storage() {
	case TypeInt8:
		return make([]int8, n), nil
	case TypeInt16:
		return make([]int16, n), nil
	case TypeInt32:
		return make([]int32, n), nil
	case TypeInt64:
		return make([]int64, n), nil
	case TypeUint8:
		return make([]uint8, n), nil
	case TypeUint16:
		return make([]uint16, n), nil
	case TypeUint32:
		return make([]uint32, n), nil
	case TypeUint64:
		return make([]uint64, n), nil
	case TypeFloat32:
		return make([]float32, n), nil
	case TypeFloat64:
		return make([]float64, n), nil
	case TypeComplex64:
		return make([]complex64, n), nil
	case TypeComplex128:
		return make([]complex128, n), nil
	case TypeBool:
		return make([]bool, n), nil
	case TypeTimestamp:
		return make([]Timestamp, n), nil
	case TypeExtendedFloat:
		return make([]Extended, n), nil
	case TypeString:
		return make([]string, n), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, dt)
	}
}

func decodeInto(dt DataType, b []byte, dst any, at int) error {
	v, err := decodePacked(dt, b)
	if err != nil {
		return err
	}
	return copyAt(dst, at, v)
}

func copyAt(dst any, at int, src any) error {
	switch d := dst.(type) {
	case []int8:
		s, ok := src.([]int8)
		return copyInto(ok, d, at, s)
	case []int16:
		s, ok := src.([]int16)
		return copyInto(ok, d, at, s)
	case []int32:
		s, ok := src.([]int32)
		return copyInto(ok, d, at, s)
	case []int64:
		s, ok := src.([]int64)
		return copyInto(ok, d, at, s)
	case []uint8:
		s, ok := src.([]uint8)
		return copyInto(ok, d, at, s)
	case []uint16:
		s, ok := src.([]uint16)
		return copyInto(ok, d, at, s)
	case []uint32:
		s, ok := src.([]uint32)
		return copyInto(ok, d, at, s)
	case []uint64:
		s, ok := src.([]uint64)
		return copyInto(ok, d, at, s)
	case []float32:
		s, ok := src.([]float32)
		return copyInto(ok, d, at, s)
	case []float64:
		s, ok := src.([]float64)
		return copyInto(ok, d, at, s)
	case []complex64:
		s, ok := src.([]complex64)
		return copyInto(ok, d, at, s)
	case []complex128:
		s, ok := src.([]complex128)
		return copyInto(ok, d, at, s)
	case []bool:
		s, ok := src.([]bool)
		return copyInto(ok, d, at, s)
	case []Timestamp:
		s, ok := src.([]Timestamp)
		return copyInto(ok, d, at, s)
	case []Extended:
		s, ok := src.([]Extended)
		return copyInto(ok, d, at, s)
	case []string:
		s, ok := src.([]string)
		return copyInto(ok, d, at, s)
	default:
		return ErrUnsupported
	}
}

func copyInto[T any](ok bool, dst []T, at int, src []T) error {
	if !ok || at < 0 || at+len(src) > len(dst) {
		return ErrCorrupt
	}
	copy(dst[at:], src)
	return nil
}
