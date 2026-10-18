package tdms

import (
	"io"
	"sync"
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
	return &seqReader{r: r, buf: buf}
}

func (s *seqReader) release() {
	if s == nil || s.buf == nil {
		return
	}
	if cap(s.buf) == DefaultReadBuffer {
		readBufPool.Put(s.buf[:DefaultReadBuffer])
	}
	s.buf = nil
	s.valid = 0
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
	if !nativeLittleEndian {
		return nil, false, nil
	}
	sz, ok := c.dt.Size()
	if !ok || sz <= 0 {
		return nil, false, nil
	}
	switch c.dt.storage() {
	case TypeInt8:
		return readFixed[int8](c, r, start, n, sz, reuse)
	case TypeInt16:
		return readFixed[int16](c, r, start, n, sz, reuse)
	case TypeInt32:
		return readFixed[int32](c, r, start, n, sz, reuse)
	case TypeInt64:
		return readFixed[int64](c, r, start, n, sz, reuse)
	case TypeUint8:
		return readFixed[uint8](c, r, start, n, sz, reuse)
	case TypeUint16:
		return readFixed[uint16](c, r, start, n, sz, reuse)
	case TypeUint32:
		return readFixed[uint32](c, r, start, n, sz, reuse)
	case TypeUint64:
		return readFixed[uint64](c, r, start, n, sz, reuse)
	case TypeFloat32:
		return readFixed[float32](c, r, start, n, sz, reuse)
	case TypeFloat64:
		return readFixed[float64](c, r, start, n, sz, reuse)
	case TypeComplex64:
		return readFixed[complex64](c, r, start, n, sz, reuse)
	case TypeComplex128:
		return readFixed[complex128](c, r, start, n, sz, reuse)
	default:
		return nil, false, nil
	}
}

func readFixed[T fixedNative](c *Channel, r io.ReaderAt, start, n, sz int, reuse any) (any, bool, error) {
	var out []T
	if s, ok := reuse.([]T); ok && cap(s) >= n {
		out = s[:n]
	} else {
		out = make([]T, n)
	}
	if n > 0 {
		raw := unsafe.Slice((*byte)(unsafe.Pointer(&out[0])), n*sz)
		if err := c.fillPacked(r, start, n, sz, raw); err != nil {
			return nil, true, err
		}
	}
	return out, true, nil
}

func (c *Channel) fillPacked(r io.ReaderAt, start, n, size int, dst []byte) error {
	if n == 0 {
		return nil
	}
	if size <= 0 || len(dst) < n*size {
		return ErrCorrupt
	}
	filled := 0
	need := n
	pos := int64(start)
	for i := range c.spans {
		if need == 0 {
			break
		}
		sp := &c.spans[i]
		avail, err := add64(mustMul(sp.nChunks, sp.nPerChunk), sp.partialN)
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
		region := dst[filled*size : (filled+int(take))*size]
		if err := sp.copyValues(r, pos, take, region); err != nil {
			return err
		}
		normalize(c.dt, region, sp.big)
		filled += int(take)
		need -= int(take)
		pos = 0
	}
	if need != 0 {
		return ErrCorrupt
	}
	return nil
}

func deinterleave(dst, src []byte, size, stride, n int) {
	if n <= 0 || size <= 0 {
		return
	}
	// Contiguous span: one move. Interleaved values are pulled out of a
	// stride window. When every sample of this width stays naturally aligned,
	// copy a machine word instead of one byte at a time. Mixed-width layouts
	// whose stride is not a multiple of the sample width stay on the byte path
	// so an unaligned access is never performed.
	switch size {
	case 1:
		for i := 0; i < n; i++ {
			dst[i] = src[i*stride]
		}
	case 2:
		if wordAligned(dst, src, stride, 2) {
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
		if wordAligned(dst, src, stride, 4) {
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
		if wordAligned(dst, src, stride, 8) {
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
		if wordAligned(dst, src, stride, 8) {
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
