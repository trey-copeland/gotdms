package tdms

import (
	"io"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// fixedNative is a Go value whose in-memory bytes match a little-endian TDMS
// value of the same width. Timestamp and Extended do not: timestamp fields
// are stored fraction-then-seconds, and Extended is 10 bytes with padding.
type fixedNative interface {
	int8 | int16 | int32 | int64 |
		uint8 | uint16 | uint32 | uint64 |
		float32 | float64 |
		complex64 | complex128
}

// strideChunk is the source window copied while de-interleaving. A full-channel
// interleaved read used to allocate the entire raw span per channel.
const strideChunk = 1 << 20

var (
	readBufPool = sync.Pool{
		New: func() any { return make([]byte, DefaultReadBuffer) },
	}
	stridePool = sync.Pool{
		New: func() any { return make([]byte, strideChunk) },
	}
	// readBufPuts counts buffers returned to readBufPool. Tests use it to
	// check Close, early end, and error paths. Gets that miss the pool call
	// New and are not counted here.
	readBufPuts atomic.Int64

	// contigDirect selects how a contiguous fixed-size Cursor reads.
	// 0 always uses the sequential buffer. 1 (the default) ReadAts straight
	// into the batch when that batch is at least cursorDirectMin bytes, which
	// is the measured win, and keeps the buffer for smaller batches. 2 always
	// ReadAts, including sizes that lost the comparison. Benchmarks set this.
	contigDirect = 1

	// cursorDirectMin is the batch size, in bytes, where skipping the
	// sequential buffer won. 8 KiB lost (BenchmarkCursorDirectTiny); 32 KiB
	// and up won (BenchmarkCursorDirectSmall, Boundary, Large).
	cursorDirectMin = 32 << 10
)

// seqReader serves sequential Cursor reads from a pooled buffer. Reads at
// least as large as the buffer bypass it and hit ReadAt directly, which is
// what Values does for a contiguous channel.
type seqReader struct {
	r     io.ReaderAt
	buf   []byte
	base  int64
	valid int
}

func newSeqReader(r io.ReaderAt, size int) *seqReader {
	if size <= 0 {
		size = DefaultReadBuffer
	}
	var buf []byte
	if size == DefaultReadBuffer {
		if p, ok := readBufPool.Get().([]byte); ok && cap(p) >= size {
			buf = p[:size]
		}
	}
	if buf == nil {
		buf = make([]byte, size)
	}
	s := &seqReader{r: r, buf: buf}
	runtime.SetFinalizer(s, (*seqReader).release)
	return s
}

func (s *seqReader) release() {
	if s == nil {
		return
	}
	runtime.SetFinalizer(s, nil)
	if s.buf == nil {
		return
	}
	buf := s.buf
	s.buf = nil
	s.valid = 0
	if cap(buf) == DefaultReadBuffer {
		readBufPool.Put(buf[:DefaultReadBuffer])
		readBufPuts.Add(1)
	}
}

func (s *seqReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s == nil || s.r == nil || len(s.buf) == 0 || len(p) >= len(s.buf) {
		if s != nil && s.r != nil {
			return s.r.ReadAt(p, off)
		}
		return 0, ErrClosed
	}
	if off >= s.base && off+int64(len(p)) <= s.base+int64(s.valid) {
		copy(p, s.buf[int(off-s.base):])
		return len(p), nil
	}
	n, err := s.r.ReadAt(s.buf, off)
	s.base = off
	s.valid = n
	if n >= len(p) {
		copy(p, s.buf[:len(p)])
		return len(p), nil
	}
	copy(p, s.buf[:n])
	if err == nil {
		err = io.EOF
	}
	return n, err
}

func borrowScratch(n int) []byte {
	if n <= 0 {
		return nil
	}
	if n <= strideChunk {
		if p, ok := stridePool.Get().([]byte); ok && cap(p) >= n {
			return p[:n]
		}
		return make([]byte, n, strideChunk)
	}
	return make([]byte, n)
}

func releaseScratch(b []byte) {
	if cap(b) == strideChunk {
		stridePool.Put(b[:strideChunk])
	}
}

// readFast decodes a fixed-size little-endian channel directly into a typed
// slice. On a big-endian machine it declines and the scalar path runs.
// reuse, when it is a []T with sufficient capacity, is overwritten; Cursor
// relies on that to keep one batch buffer. Big-endian segments are byte-swapped
// in place after the read, which yields native values on a little-endian host.
func (c *Channel) readFast(r io.ReaderAt, start, n int, reuse any) (any, bool, error) {
	return readFastSpans(c.dt, c.spans, c.runs, r, start, n, reuse)
}

func readFastSpans(dt DataType, spans []span, runs []spanRun, r io.ReaderAt, start, n int, reuse any) (any, bool, error) {
	if !nativeLittleEndian {
		return nil, false, nil
	}
	sz, ok := dt.Size()
	if !ok || sz <= 0 {
		return nil, false, nil
	}
	switch dt.storage() {
	case TypeInt8:
		return readFixed[int8](dt, spans, runs, r, start, n, sz, reuse)
	case TypeInt16:
		return readFixed[int16](dt, spans, runs, r, start, n, sz, reuse)
	case TypeInt32:
		return readFixed[int32](dt, spans, runs, r, start, n, sz, reuse)
	case TypeInt64:
		return readFixed[int64](dt, spans, runs, r, start, n, sz, reuse)
	case TypeUint8:
		return readFixed[uint8](dt, spans, runs, r, start, n, sz, reuse)
	case TypeUint16:
		return readFixed[uint16](dt, spans, runs, r, start, n, sz, reuse)
	case TypeUint32:
		return readFixed[uint32](dt, spans, runs, r, start, n, sz, reuse)
	case TypeUint64:
		return readFixed[uint64](dt, spans, runs, r, start, n, sz, reuse)
	case TypeFloat32:
		return readFixed[float32](dt, spans, runs, r, start, n, sz, reuse)
	case TypeFloat64:
		return readFixed[float64](dt, spans, runs, r, start, n, sz, reuse)
	case TypeComplex64:
		return readFixed[complex64](dt, spans, runs, r, start, n, sz, reuse)
	case TypeComplex128:
		return readFixed[complex128](dt, spans, runs, r, start, n, sz, reuse)
	default:
		return nil, false, nil
	}
}

func readFixed[T fixedNative](dt DataType, spans []span, runs []spanRun, r io.ReaderAt, start, n, sz int, reuse any) (any, bool, error) {
	var out []T
	if s, ok := reuse.([]T); ok && cap(s) >= n {
		out = s[:n]
	} else {
		out = make([]T, n)
	}
	if n > 0 {
		raw := unsafe.Slice((*byte)(unsafe.Pointer(&out[0])), n*sz)
		if err := fillPackedSpans(spans, runs, dt, r, start, n, sz, raw); err != nil {
			return nil, true, err
		}
	}
	return out, true, nil
}

func (c *Channel) fillPacked(r io.ReaderAt, start, n, size int, dst []byte) error {
	return fillPackedSpans(c.spans, c.runs, c.dt, r, start, n, size, dst)
}

func fillPackedSpans(spans []span, runs []spanRun, dt DataType, r io.ReaderAt, start, n, size int, dst []byte) error {
	if n == 0 {
		return nil
	}
	if size <= 0 || len(dst) < n*size {
		return ErrCorrupt
	}
	filled := 0
	need := n
	pos := int64(start)
	for i := range spans {
		if need == 0 {
			break
		}
		sp := &spans[i]
		avail, err := spanSamples(sp, runs)
		if err != nil {
			return err
		}
		if pos >= avail {
			pos -= avail
			continue
		}
		take := avail - pos
		if take > int64(need) {
			take = int64(need)
		}
		if sp.typeSize != size {
			// The output is sized by the data type. A span of another
			// width would write past it.
			return ErrCorrupt
		}
		region := dst[filled*size : (filled+int(take))*size]
		if err := sp.copyValues(runs, r, pos, take, region); err != nil {
			return err
		}
		normalize(dt, region, sp.big)
		filled += int(take)
		need -= int(take)
		pos = 0
	}
	if need != 0 {
		return ErrCorrupt
	}
	return nil
}

// fitsSpan reports whether n samples of size bytes at stride fit in dst and
// src without an int overflow. Word loads are only used when this is true, so
// a short tail cannot make an unsafe load read past the slice.
func fitsSpan(n, stride, size, dstLen, srcLen int) bool {
	if n <= 0 || stride <= 0 || size <= 0 || n > math.MaxInt/size {
		return false
	}
	if dstLen < n*size {
		return false
	}
	if n > 1 && stride > (math.MaxInt-size)/(n-1) {
		return false
	}
	return srcLen >= (n-1)*stride+size
}

func deinterleave(dst, src []byte, size, stride, n int) {
	if n <= 0 || size <= 0 {
		return
	}
	// Contiguous span: one move. Interleaved values are pulled out of a
	// stride window. When every sample of this width stays naturally aligned
	// and the last sample's bytes sit inside src, copy a machine word.
	// Mixed-width layouts whose stride is not a multiple of the sample width,
	// and windows whose first byte is unaligned, stay on the byte path so an
	// unaligned access is never performed. The byte path also covers a short
	// buffer: the index expression panics instead of an unsafe load reading
	// past the slice.
	safe := fitsSpan(n, stride, size, len(dst), len(src))
	switch size {
	case 1:
		for i := 0; i < n; i++ {
			dst[i] = src[i*stride]
		}
	case 2:
		if safe && wordAligned(dst, src, stride, 2) {
			deinterleave16(dst, src, stride, n)
			return
		}
		for i := 0; i < n; i++ {
			s := i * stride
			d := i * 2
			dst[d] = src[s]
			dst[d+1] = src[s+1]
		}
	case 4:
		if safe && wordAligned(dst, src, stride, 4) {
			deinterleave32(dst, src, stride, n)
			return
		}
		for i := 0; i < n; i++ {
			s := i * stride
			d := i * 4
			dst[d] = src[s]
			dst[d+1] = src[s+1]
			dst[d+2] = src[s+2]
			dst[d+3] = src[s+3]
		}
	case 8:
		if safe && wordAligned(dst, src, stride, 8) {
			deinterleave64(dst, src, stride, n)
			return
		}
		for i := 0; i < n; i++ {
			s := i * stride
			d := i * 8
			dst[d] = src[s]
			dst[d+1] = src[s+1]
			dst[d+2] = src[s+2]
			dst[d+3] = src[s+3]
			dst[d+4] = src[s+4]
			dst[d+5] = src[s+5]
			dst[d+6] = src[s+6]
			dst[d+7] = src[s+7]
		}
	case 16:
		if safe && wordAligned(dst, src, stride, 8) {
			deinterleave128(dst, src, stride, n)
			return
		}
		for i := 0; i < n; i++ {
			s := i * stride
			copy(dst[i*16:i*16+16], src[s:s+16])
		}
	default:
		for i := 0; i < n; i++ {
			s := i * stride
			copy(dst[i*size:(i+1)*size], src[s:s+size])
		}
	}
}

// wordAligned reports whether sample 0 is align-byte aligned and every later
// sample stays aligned. dst and src are the packed destination and the
// strided window; both are indexed from the first byte of this channel.
func wordAligned(dst, src []byte, stride, align int) bool {
	if align <= 0 || len(dst) == 0 || len(src) == 0 || stride%align != 0 {
		return false
	}
	return uintptr(unsafe.Pointer(&dst[0]))%uintptr(align) == 0 &&
		uintptr(unsafe.Pointer(&src[0]))%uintptr(align) == 0
}

func deinterleave16(dst, src []byte, stride, n int) {
	_ = dst[n*2-1]
	_ = src[(n-1)*stride+1]
	for i := 0; i < n; i++ {
		s := i * stride
		d := i * 2
		*(*uint16)(unsafe.Pointer(&dst[d])) = *(*uint16)(unsafe.Pointer(&src[s]))
	}
}

func deinterleave32(dst, src []byte, stride, n int) {
	_ = dst[n*4-1]
	_ = src[(n-1)*stride+3]
	for i := 0; i < n; i++ {
		s := i * stride
		d := i * 4
		*(*uint32)(unsafe.Pointer(&dst[d])) = *(*uint32)(unsafe.Pointer(&src[s]))
	}
}

func deinterleave64(dst, src []byte, stride, n int) {
	_ = dst[n*8-1]
	_ = src[(n-1)*stride+7]
	for i := 0; i < n; i++ {
		s := i * stride
		d := i * 8
		*(*uint64)(unsafe.Pointer(&dst[d])) = *(*uint64)(unsafe.Pointer(&src[s]))
	}
}

func deinterleave128(dst, src []byte, stride, n int) {
	_ = dst[n*16-1]
	_ = src[(n-1)*stride+15]
	for i := 0; i < n; i++ {
		s := i * stride
		d := i * 16
		*(*uint64)(unsafe.Pointer(&dst[d])) = *(*uint64)(unsafe.Pointer(&src[s]))
		*(*uint64)(unsafe.Pointer(&dst[d+8])) = *(*uint64)(unsafe.Pointer(&src[s+8]))
	}
}

// contiguousBytes reports whether every span of c is a packed fixed-size run
// (stride equals the sample width). Cursor can then ReadAt straight into the
// batch. Interleaved and string channels are not contiguous.
func (c *Channel) contiguousBytes() bool {
	if c == nil || len(c.spans) == 0 {
		return false
	}
	sz, ok := c.dt.Size()
	if !ok || sz <= 0 {
		return false
	}
	for i := range c.spans {
		sp := &c.spans[i]
		if sp.str || sp.stride != int64(sz) || sp.typeSize != sz {
			return false
		}
	}
	return true
}

func fixedNativeDT(dt DataType) bool {
	switch dt.storage() {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64,
		TypeUint8, TypeUint16, TypeUint32, TypeUint64,
		TypeFloat32, TypeFloat64,
		TypeComplex64, TypeComplex128:
		return true
	default:
		return false
	}
}

// overlayAt is the byte view of dst[sample:sample+n] for a fixed-native slice.
// ok is false when dst is not that kind of slice or the range does not fit.
// n == 0 returns a nil view and ok.
func overlayAt(dst any, sample, n, sz int) ([]byte, bool) {
	if n == 0 {
		return nil, true
	}
	if sample < 0 || sz <= 0 {
		return nil, false
	}
	switch s := dst.(type) {
	case []int8:
		return overlaySlice(s, sample, n, sz, 1)
	case []int16:
		return overlaySlice(s, sample, n, sz, 2)
	case []int32:
		return overlaySlice(s, sample, n, sz, 4)
	case []int64:
		return overlaySlice(s, sample, n, sz, 8)
	case []uint8:
		return overlaySlice(s, sample, n, sz, 1)
	case []uint16:
		return overlaySlice(s, sample, n, sz, 2)
	case []uint32:
		return overlaySlice(s, sample, n, sz, 4)
	case []uint64:
		return overlaySlice(s, sample, n, sz, 8)
	case []float32:
		return overlaySlice(s, sample, n, sz, 4)
	case []float64:
		return overlaySlice(s, sample, n, sz, 8)
	case []complex64:
		return overlaySlice(s, sample, n, sz, 8)
	case []complex128:
		return overlaySlice(s, sample, n, sz, 16)
	default:
		return nil, false
	}
}

func overlaySlice[T fixedNative](s []T, sample, n, sz, want int) ([]byte, bool) {
	if sz != want || sample+n > len(s) {
		return nil, false
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[sample])), n*sz), true
}

// refill reuses cur.cur's backing array when it is already a []T for this
// channel. The interface value is left in place when the length does not
// change, so a long scan does not box a new slice header per batch.
func (cur *Cursor) refill(r io.ReaderAt, n int) error {
	if done, err := cur.refillInPlace(r, n); done {
		return err
	}
	v, err := cur.ch.readRangeFrom(r, cur.pos, n, cur.cur)
	if err != nil {
		return err
	}
	cur.cur = v
	return nil
}

func (cur *Cursor) refillInPlace(r io.ReaderAt, n int) (bool, error) {
	if !nativeLittleEndian || cur.cur == nil || cur.ch == nil || !fixedNativeDT(cur.ch.dt) {
		return false, nil
	}
	sz, ok := cur.ch.dt.Size()
	if !ok {
		return false, nil
	}
	switch s := cur.cur.(type) {
	case []int8:
		return refillSlice(cur, r, s, n, sz)
	case []int16:
		return refillSlice(cur, r, s, n, sz)
	case []int32:
		return refillSlice(cur, r, s, n, sz)
	case []int64:
		return refillSlice(cur, r, s, n, sz)
	case []uint8:
		return refillSlice(cur, r, s, n, sz)
	case []uint16:
		return refillSlice(cur, r, s, n, sz)
	case []uint32:
		return refillSlice(cur, r, s, n, sz)
	case []uint64:
		return refillSlice(cur, r, s, n, sz)
	case []float32:
		return refillSlice(cur, r, s, n, sz)
	case []float64:
		return refillSlice(cur, r, s, n, sz)
	case []complex64:
		return refillSlice(cur, r, s, n, sz)
	case []complex128:
		return refillSlice(cur, r, s, n, sz)
	default:
		return false, nil
	}
}

func refillSlice[T fixedNative](cur *Cursor, r io.ReaderAt, s []T, n, sz int) (bool, error) {
	if cap(s) < n || sz != int(unsafe.Sizeof(s[0])) {
		return false, nil
	}
	out := s[:n]
	if n > 0 {
		raw := unsafe.Slice((*byte)(unsafe.Pointer(&out[0])), n*sz)
		if err := cur.ch.fillPacked(r, cur.pos, n, sz, raw); err != nil {
			return true, err
		}
	}
	if len(out) != len(s) {
		cur.cur = out
	}
	return true, nil
}
