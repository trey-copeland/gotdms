package tdms

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"
	"unsafe"
)

// growExact extends dst by n bytes. An empty dst is allocated at exactly n so
// a single large Write does not zero a second buffer. Existing bytes are copied.
func growExact(dst []byte, n int) []byte {
	if n <= 0 {
		return dst
	}
	if cap(dst)-len(dst) >= n {
		return dst[:len(dst)+n]
	}
	out := make([]byte, len(dst)+n)
	copy(out, dst)
	return out
}

// borrowedView is the little-endian byte view of a fixed numeric or complex
// slice. handled is true when Write should retain that view instead of
// copying. Empty slices are handled and contribute no bytes. A big-endian
// host returns handled false for widths above one byte so appendEncoded can
// byte-swap. Bool, timestamp, extended, and string values are not views.
func borrowedView(dt DataType, values any) (b []byte, n int, handled bool) {
	if values == nil {
		return nil, 0, true
	}
	switch dt.storage() {
	case TypeInt8:
		s, ok := values.([]int8)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeUint8:
		s, ok := values.([]uint8)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeInt16:
		s, ok := values.([]int16)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeUint16:
		s, ok := values.([]uint16)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeInt32:
		s, ok := values.([]int32)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeUint32:
		s, ok := values.([]uint32)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeFloat32:
		s, ok := values.([]float32)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeInt64:
		s, ok := values.([]int64)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeUint64:
		s, ok := values.([]uint64)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeFloat64:
		s, ok := values.([]float64)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeComplex64:
		s, ok := values.([]complex64)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	case TypeComplex128:
		s, ok := values.([]complex128)
		if !ok {
			return nil, 0, false
		}
		return aliasSlice(s)
	default:
		return nil, 0, false
	}
}

func aliasSlice[T fixedNative](s []T) ([]byte, int, bool) {
	if len(s) == 0 {
		return nil, 0, true
	}
	sz := int(unsafe.Sizeof(s[0]))
	if sz != 1 && !nativeLittleEndian {
		return nil, 0, false
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*sz), len(s), true
}

// appendEncoded appends the on-disk form of values to dst. String channels
// return the strings separately; their offset table is streamed at flush.
func appendEncoded(dst []byte, dt DataType, values any) ([]byte, []string, int, error) {
	if values == nil {
		return dst, nil, 0, nil
	}
	switch dt.storage() {
	case TypeInt8:
		s, ok := values.([]int8)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeUint8:
		s, ok := values.([]uint8)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeInt16:
		s, ok := values.([]int16)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeUint16:
		s, ok := values.([]uint16)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeInt32:
		s, ok := values.([]int32)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeUint32:
		s, ok := values.([]uint32)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeFloat32:
		s, ok := values.([]float32)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeInt64:
		s, ok := values.([]int64)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeUint64:
		s, ok := values.([]uint64)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeFloat64:
		s, ok := values.([]float64)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeComplex64:
		s, ok := values.([]complex64)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeComplex128:
		s, ok := values.([]complex128)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return appendFixed(dst, s), nil, len(s), nil
	case TypeBool:
		s, ok := values.([]bool)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		off := len(dst)
		dst = growExact(dst, len(s))
		b := dst[off:]
		for i, v := range s {
			if v {
				b[i] = 1
			} else {
				b[i] = 0
			}
		}
		return dst, nil, len(s), nil
	case TypeString:
		s, ok := values.([]string)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		return dst, s, len(s), nil
	case TypeTimestamp:
		switch s := values.(type) {
		case []Timestamp:
			off := len(dst)
			dst = growExact(dst, len(s)*16)
			b := dst[off:]
			for i, ts := range s {
				binary.LittleEndian.PutUint64(b[i*16:], ts.Fraction)
				binary.LittleEndian.PutUint64(b[i*16+8:], uint64(ts.Seconds))
			}
			return dst, nil, len(s), nil
		case []time.Time:
			off := len(dst)
			dst = growExact(dst, len(s)*16)
			b := dst[off:]
			for i, t := range s {
				ts := NewTimestamp(t)
				binary.LittleEndian.PutUint64(b[i*16:], ts.Fraction)
				binary.LittleEndian.PutUint64(b[i*16+8:], uint64(ts.Seconds))
			}
			return dst, nil, len(s), nil
		default:
			return nil, nil, 0, typeErr(dt, values)
		}
	case TypeExtendedFloat:
		s, ok := values.([]Extended)
		if !ok {
			return nil, nil, 0, typeErr(dt, values)
		}
		off := len(dst)
		dst = growExact(dst, len(s)*extendedSize)
		b := dst[off:]
		for i, e := range s {
			copy(b[i*extendedSize:], encodeExtended(e))
		}
		return dst, nil, len(s), nil
	case TypeVoid:
		return nil, nil, 0, fmt.Errorf("%w: void channels have no values", ErrType)
	default:
		return nil, nil, 0, fmt.Errorf("%w: %s", ErrUnsupported, dt)
	}
}

// appendFixed copies s into dst in little-endian TDMS order. On a
// little-endian machine the copy is a bulk move. On a big-endian machine
// appendFixedScalar stores each value little-endian. Single-byte types match
// either endianness.
func appendFixed[T fixedNative](dst []byte, s []T) []byte {
	if len(s) == 0 {
		return dst
	}
	sz := int(unsafe.Sizeof(s[0]))
	off := len(dst)
	dst = growExact(dst, len(s)*sz)
	b := dst[off:]
	if nativeLittleEndian || sz == 1 {
		copy(b, unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(b)))
		return dst
	}
	appendFixedScalar(b, s)
	return dst
}

// appendFixedScalar writes s as little-endian bytes into b, which must already
// be len(s)*sizeof(T) bytes. It is the big-endian-host fallback and is safe to
// call on any host.
func appendFixedScalar[T fixedNative](b []byte, s []T) {
	switch s := any(s).(type) {
	case []int16:
		for i, v := range s {
			binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
		}
	case []uint16:
		for i, v := range s {
			binary.LittleEndian.PutUint16(b[i*2:], v)
		}
	case []int32:
		for i, v := range s {
			binary.LittleEndian.PutUint32(b[i*4:], uint32(v))
		}
	case []uint32:
		for i, v := range s {
			binary.LittleEndian.PutUint32(b[i*4:], v)
		}
	case []float32:
		for i, v := range s {
			binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
		}
	case []int64:
		for i, v := range s {
			binary.LittleEndian.PutUint64(b[i*8:], uint64(v))
		}
	case []uint64:
		for i, v := range s {
			binary.LittleEndian.PutUint64(b[i*8:], v)
		}
	case []float64:
		for i, v := range s {
			binary.LittleEndian.PutUint64(b[i*8:], math.Float64bits(v))
		}
	case []complex64:
		for i, v := range s {
			binary.LittleEndian.PutUint32(b[i*8:], math.Float32bits(real(v)))
			binary.LittleEndian.PutUint32(b[i*8+4:], math.Float32bits(imag(v)))
		}
	case []complex128:
		for i, v := range s {
			binary.LittleEndian.PutUint64(b[i*16:], math.Float64bits(real(v)))
			binary.LittleEndian.PutUint64(b[i*16+8:], math.Float64bits(imag(v)))
		}
	default:
		// int8 and uint8 are handled by the bulk copy before this call.
		// Reaching this means a fixedNative width was added without a store.
		panic(fmt.Sprintf("tdms: appendFixedScalar missing case for %T", s))
	}
}

func rawByteSize(data []*wChannel, interleaved bool) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if !interleaved || len(data) == 1 {
		var n int64
		for _, ch := range data {
			if ch.dt == TypeString {
				sz, err := stringBlockSize(ch.strs)
				if err != nil {
					return 0, err
				}
				if sz > uint64(math.MaxInt64-n) {
					return 0, fmt.Errorf("%w: segment larger than int64", ErrArgument)
				}
				n += int64(sz)
				continue
			}
			if int64(len(ch.raw)) > math.MaxInt64-n {
				return 0, fmt.Errorf("%w: segment larger than int64", ErrArgument)
			}
			n += int64(len(ch.raw))
		}
		return n, nil
	}
	stride := 0
	for _, ch := range data {
		sz, ok := ch.dt.Size()
		if !ok {
			return 0, ErrLayout
		}
		stride += sz
	}
	n := data[0].n
	if stride != 0 && n > math.MaxInt/stride {
		return 0, fmt.Errorf("%w: segment larger than int", ErrArgument)
	}
	return int64(n) * int64(stride), nil
}

// writeRaw streams the segment's raw section. Contiguous channels are written
// from the buffer Write already filled. Interleaved channels are mixed into a
// window of about strideChunk bytes so Flush does not allocate a second copy
// of the whole segment.
func writeRaw(w io.Writer, data []*wChannel, interleaved bool) error {
	if len(data) == 0 {
		return nil
	}
	if !interleaved || len(data) == 1 {
		for _, ch := range data {
			if ch.dt == TypeString {
				if err := writeStringBlock(w, ch.strs); err != nil {
					return err
				}
				continue
			}
			if len(ch.raw) == 0 {
				continue
			}
			if _, err := w.Write(ch.raw); err != nil {
				return err
			}
		}
		return nil
	}
	n := data[0].n
	sizes := make([]int, len(data))
	stride := 0
	for i, ch := range data {
		sz, ok := ch.dt.Size()
		if !ok {
			return ErrLayout
		}
		sizes[i] = sz
		stride += sz
		if len(ch.raw) < n*sz {
			return ErrCorrupt
		}
	}
	if stride == 0 || n == 0 {
		return nil
	}
	rows := 1
	if stride < strideChunk {
		rows = (strideChunk - sizes[0]) / stride
		if rows < 1 {
			rows = 1
		}
	}
	buf := borrowScratch(rows * stride)
	defer releaseScratch(buf)
	for done := 0; done < n; {
		take := rows
		if done+take > n {
			take = n - done
		}
		dst := 0
		for i := 0; i < take; i++ {
			sample := done + i
			for c, ch := range data {
				sz := sizes[c]
				src := sample * sz
				copy(buf[dst:dst+sz], ch.raw[src:src+sz])
				dst += sz
			}
		}
		if _, err := w.Write(buf[:take*stride]); err != nil {
			return err
		}
		done += take
	}
	return nil
}

func writeStringBlock(w io.Writer, strs []string) error {
	var total uint64
	for _, s := range strs {
		if uint64(len(s)) > math.MaxUint32 {
			return fmt.Errorf("%w: string longer than 4 GiB", ErrArgument)
		}
		total += uint64(len(s))
		if total > math.MaxUint32 {
			return fmt.Errorf("%w: string block longer than 4 GiB", ErrArgument)
		}
	}
	const batch = 4096
	buf := make([]byte, batch*4)
	off := uint32(0)
	for i := 0; i < len(strs); {
		n := len(strs) - i
		if n > batch {
			n = batch
		}
		for j := 0; j < n; j++ {
			off += uint32(len(strs[i+j]))
			binary.LittleEndian.PutUint32(buf[j*4:], off)
		}
		if _, err := w.Write(buf[:n*4]); err != nil {
			return err
		}
		i += n
	}
	for _, s := range strs {
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
	}
	return nil
}
