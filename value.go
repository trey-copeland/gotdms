package tdms

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
	"unsafe"
)

// Property is one named value attached to the file, a group, or a channel.
// Value's concrete type is the Go type listed in DataType's documentation:
// int8/16/32/64, uint8/16/32/64, float32/64, bool, string, Timestamp,
// Extended, complex64, or complex128.
type Property struct {
	Name  string
	Type  DataType
	Value any
}

// Well-known waveform and unit property names written by LabVIEW.
const (
	PropStartTime   = "wf_start_time"
	PropStartOffset = "wf_start_offset"
	PropIncrement   = "wf_increment"
	PropSamples     = "wf_samples"
	PropUnitString  = "unit_string"
)

func decodeProperty(d *decoder) (Property, error) {
	name, err := d.str()
	if err != nil {
		return Property{}, err
	}
	code, err := d.u32()
	if err != nil {
		return Property{}, err
	}
	dt := DataType(code)
	v, err := d.value(dt)
	if err != nil {
		return Property{}, err
	}
	return Property{Name: name, Type: dt, Value: v}, nil
}

func encodeProperty(p Property) ([]byte, error) {
	body, err := encodeStdValue(p.Type, p.Value)
	if err != nil {
		return nil, err
	}
	n := 4 + len(p.Name) + 4 + len(body)
	out := make([]byte, 0, n)
	out = appendU32(out, uint32(len(p.Name)))
	out = append(out, p.Name...)
	out = appendU32(out, uint32(p.Type))
	out = append(out, body...)
	return out, nil
}

// encodeStdValue encodes a property value or a single standard-layout field.
// Strings are uint32 length + UTF-8 bytes (not the channel offset table).
func encodeStdValue(dt DataType, v any) ([]byte, error) {
	switch dt.storage() {
	case TypeInt8:
		n, ok := asInt64(v)
		if !ok || n < math.MinInt8 || n > math.MaxInt8 {
			return nil, typeErr(dt, v)
		}
		return []byte{byte(int8(n))}, nil
	case TypeInt16:
		n, ok := asInt64(v)
		if !ok || n < math.MinInt16 || n > math.MaxInt16 {
			return nil, typeErr(dt, v)
		}
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(int16(n)))
		return b[:], nil
	case TypeInt32:
		n, ok := asInt64(v)
		if !ok || n < math.MinInt32 || n > math.MaxInt32 {
			return nil, typeErr(dt, v)
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(int32(n)))
		return b[:], nil
	case TypeInt64:
		n, ok := asInt64(v)
		if !ok {
			return nil, typeErr(dt, v)
		}
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(n))
		return b[:], nil
	case TypeUint8:
		n, ok := asUint64(v)
		if !ok || n > math.MaxUint8 {
			return nil, typeErr(dt, v)
		}
		return []byte{byte(n)}, nil
	case TypeUint16:
		n, ok := asUint64(v)
		if !ok || n > math.MaxUint16 {
			return nil, typeErr(dt, v)
		}
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(n))
		return b[:], nil
	case TypeUint32:
		n, ok := asUint64(v)
		if !ok || n > math.MaxUint32 {
			return nil, typeErr(dt, v)
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(n))
		return b[:], nil
	case TypeUint64:
		n, ok := asUint64(v)
		if !ok {
			return nil, typeErr(dt, v)
		}
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], n)
		return b[:], nil
	case TypeFloat32:
		f, ok := asFloat64(v)
		if !ok {
			return nil, typeErr(dt, v)
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(float32(f)))
		return b[:], nil
	case TypeFloat64:
		f, ok := asFloat64(v)
		if !ok {
			return nil, typeErr(dt, v)
		}
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(f))
		return b[:], nil
	case TypeBool:
		bv, ok := v.(bool)
		if !ok {
			return nil, typeErr(dt, v)
		}
		if bv {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case TypeString:
		s, ok := v.(string)
		if !ok {
			return nil, typeErr(dt, v)
		}
		if len(s) > math.MaxUint32 {
			return nil, fmt.Errorf("%w: string longer than 4 GiB", ErrArgument)
		}
		out := appendU32(nil, uint32(len(s)))
		out = append(out, s...)
		return out, nil
	case TypeTimestamp:
		ts, ok := asTimestamp(v)
		if !ok {
			return nil, typeErr(dt, v)
		}
		var b [16]byte
		binary.LittleEndian.PutUint64(b[0:8], ts.Fraction)
		binary.LittleEndian.PutUint64(b[8:16], uint64(ts.Seconds))
		return b[:], nil
	case TypeExtendedFloat:
		e, ok := asExtended(v)
		if !ok {
			return nil, typeErr(dt, v)
		}
		return encodeExtended(e), nil
	case TypeComplex64:
		c, ok := v.(complex64)
		if !ok {
			return nil, typeErr(dt, v)
		}
		var b [8]byte
		binary.LittleEndian.PutUint32(b[0:4], math.Float32bits(real(c)))
		binary.LittleEndian.PutUint32(b[4:8], math.Float32bits(imag(c)))
		return b[:], nil
	case TypeComplex128:
		c, ok := v.(complex128)
		if !ok {
			return nil, typeErr(dt, v)
		}
		var b [16]byte
		binary.LittleEndian.PutUint64(b[0:8], math.Float64bits(real(c)))
		binary.LittleEndian.PutUint64(b[8:16], math.Float64bits(imag(c)))
		return b[:], nil
	case TypeVoid, TypeFixedPoint:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, dt)
	case TypeDAQmxRaw:
		return nil, ErrDAQmx
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, dt)
	}
}

func typeErr(dt DataType, v any) error {
	return fmt.Errorf("%w: %s value has type %T", ErrType, dt, v)
}

type decoder struct {
	b   []byte
	off int
	bo  binary.ByteOrder
}

func (d *decoder) remain() int { return len(d.b) - d.off }

func (d *decoder) u32() (uint32, error) {
	if d.remain() < 4 {
		return 0, ErrTruncated
	}
	v := d.bo.Uint32(d.b[d.off:])
	d.off += 4
	return v, nil
}

func (d *decoder) u64() (uint64, error) {
	if d.remain() < 8 {
		return 0, ErrTruncated
	}
	v := d.bo.Uint64(d.b[d.off:])
	d.off += 8
	return v, nil
}

func (d *decoder) bytes(n int) ([]byte, error) {
	if n < 0 || d.remain() < n {
		return nil, ErrTruncated
	}
	b := d.b[d.off : d.off+n]
	d.off += n
	return b, nil
}

func (d *decoder) str() (string, error) {
	n, err := d.u32()
	if err != nil {
		return "", err
	}
	if n > uint32(d.remain()) {
		return "", ErrTruncated
	}
	b, err := d.bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (d *decoder) value(dt DataType) (any, error) {
	if dt == TypeDAQmxRaw {
		return nil, ErrDAQmx
	}
	if !dt.supported() && dt != TypeString && dt.storage() != TypeVoid {
		if dt == TypeFixedPoint || dt == TypeVoid {
			return nil, fmt.Errorf("%w: property type %s", ErrUnsupported, dt)
		}
		if _, known := typeNames[dt]; !known {
			return nil, fmt.Errorf("%w: property type %s", ErrUnsupported, dt)
		}
	}
	switch dt.storage() {
	case TypeString:
		s, err := d.str()
		return s, err
	case TypeBool:
		b, err := d.bytes(1)
		if err != nil {
			return nil, err
		}
		return b[0] != 0, nil
	default:
		sz, ok := dt.Size()
		if !ok {
			return nil, fmt.Errorf("%w: property type %s", ErrUnsupported, dt)
		}
		raw, err := d.bytes(sz)
		if err != nil {
			return nil, err
		}
		buf := append([]byte(nil), raw...)
		normalize(dt, buf, d.bo == binary.BigEndian)
		v, err := decodePacked(dt, buf)
		if err != nil {
			return nil, err
		}
		return firstOnly(v)
	}
}

func firstOnly(v any) (any, error) {
	switch s := v.(type) {
	case []int8:
		return s[0], nil
	case []int16:
		return s[0], nil
	case []int32:
		return s[0], nil
	case []int64:
		return s[0], nil
	case []uint8:
		return s[0], nil
	case []uint16:
		return s[0], nil
	case []uint32:
		return s[0], nil
	case []uint64:
		return s[0], nil
	case []float32:
		return s[0], nil
	case []float64:
		return s[0], nil
	case []bool:
		return s[0], nil
	case []Timestamp:
		return s[0], nil
	case []Extended:
		return s[0], nil
	case []complex64:
		return s[0], nil
	case []complex128:
		return s[0], nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrType, v)
	}
}

// normalize rewrites one or more file-endian values into little-endian
// canonical layout, in place. See Timestamp and Extended for aggregate order.
func normalize(dt DataType, b []byte, big bool) {
	if !big || len(b) == 0 {
		return
	}
	switch dt.storage() {
	case TypeTimestamp:
		for i := 0; i+16 <= len(b); i += 16 {
			var sec, frac [8]byte
			copy(sec[:], b[i:i+8])
			copy(frac[:], b[i+8:i+16])
			for k := 0; k < 8; k++ {
				b[i+k] = frac[7-k]
				b[i+8+k] = sec[7-k]
			}
		}
	case TypeExtendedFloat:
		for i := 0; i+extendedSize <= len(b); i += extendedSize {
			// BE: exponent uint16, significand uint64. Canonical LE is the reverse.
			var exp [2]byte
			var sig [8]byte
			copy(exp[:], b[i:i+2])
			copy(sig[:], b[i+2:i+10])
			for k := 0; k < 8; k++ {
				b[i+k] = sig[7-k]
			}
			b[i+8] = exp[1]
			b[i+9] = exp[0]
		}
	case TypeComplex64:
		swapEach(b, 4)
	case TypeComplex128:
		swapEach(b, 8)
	default:
		sz, ok := dt.Size()
		if ok && sz > 1 {
			swapEach(b, sz)
		}
	}
}

func swapEach(b []byte, size int) {
	if size <= 1 {
		return
	}
	for i := 0; i+size <= len(b); i += size {
		for l, r := i, i+size-1; l < r; l, r = l+1, r-1 {
			b[l], b[r] = b[r], b[l]
		}
	}
}

func encodeExtended(e Extended) []byte {
	var b [extendedSize]byte
	binary.LittleEndian.PutUint64(b[0:8], e.Significand)
	binary.LittleEndian.PutUint16(b[8:10], e.Exponent)
	return b[:]
}

func decodeExtended(b []byte) Extended {
	return Extended{
		Significand: binary.LittleEndian.Uint64(b[0:8]),
		Exponent:    binary.LittleEndian.Uint16(b[8:10]),
	}
}

// decodePacked decodes a little-endian canonical buffer of n values.
// On a little-endian machine, plain numeric and complex values are copied
// in one move. Bool, timestamp, and extended values stay on the scalar path,
// as do all types on a big-endian machine (the bytes are little-endian
// canonical, which is not the native layout there).
func decodePacked(dt DataType, b []byte) (any, error) {
	sz, ok := dt.Size()
	if !ok || sz == 0 || len(b)%sz != 0 {
		return nil, ErrCorrupt
	}
	if nativeLittleEndian {
		if v, ok := decodePackedMemcpy(dt, b); ok {
			return v, nil
		}
	}
	return decodePackedScalar(dt, b)
}

func decodePackedMemcpy(dt DataType, b []byte) (any, bool) {
	sz, ok := dt.Size()
	if !ok || sz == 0 || len(b)%sz != 0 {
		return nil, false
	}
	n := len(b) / sz
	switch dt.storage() {
	case TypeInt8:
		return memcpySlice[int8](b, n, sz), true
	case TypeInt16:
		return memcpySlice[int16](b, n, sz), true
	case TypeInt32:
		return memcpySlice[int32](b, n, sz), true
	case TypeInt64:
		return memcpySlice[int64](b, n, sz), true
	case TypeUint8:
		return memcpySlice[uint8](b, n, sz), true
	case TypeUint16:
		return memcpySlice[uint16](b, n, sz), true
	case TypeUint32:
		return memcpySlice[uint32](b, n, sz), true
	case TypeUint64:
		return memcpySlice[uint64](b, n, sz), true
	case TypeFloat32:
		return memcpySlice[float32](b, n, sz), true
	case TypeFloat64:
		return memcpySlice[float64](b, n, sz), true
	case TypeComplex64:
		return memcpySlice[complex64](b, n, sz), true
	case TypeComplex128:
		return memcpySlice[complex128](b, n, sz), true
	default:
		return nil, false
	}
}

func memcpySlice[T fixedNative](b []byte, n, sz int) []T {
	out := make([]T, n)
	if n == 0 {
		return out
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&out[0])), n*sz), b)
	return out
}

func decodePackedScalar(dt DataType, b []byte) (any, error) {
	sz, ok := dt.Size()
	if !ok || sz == 0 || len(b)%sz != 0 {
		return nil, ErrCorrupt
	}
	n := len(b) / sz
	switch dt.storage() {
	case TypeInt8:
		out := make([]int8, n)
		for i := range out {
			out[i] = int8(b[i])
		}
		return out, nil
	case TypeUint8:
		out := make([]uint8, n)
		copy(out, b)
		return out, nil
	case TypeBool:
		out := make([]bool, n)
		for i := range out {
			out[i] = b[i] != 0
		}
		return out, nil
	case TypeInt16:
		out := make([]int16, n)
		for i := range out {
			out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
		}
		return out, nil
	case TypeUint16:
		out := make([]uint16, n)
		for i := range out {
			out[i] = binary.LittleEndian.Uint16(b[i*2:])
		}
		return out, nil
	case TypeInt32:
		out := make([]int32, n)
		for i := range out {
			out[i] = int32(binary.LittleEndian.Uint32(b[i*4:]))
		}
		return out, nil
	case TypeUint32:
		out := make([]uint32, n)
		for i := range out {
			out[i] = binary.LittleEndian.Uint32(b[i*4:])
		}
		return out, nil
	case TypeFloat32:
		out := make([]float32, n)
		for i := range out {
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		}
		return out, nil
	case TypeInt64:
		out := make([]int64, n)
		for i := range out {
			out[i] = int64(binary.LittleEndian.Uint64(b[i*8:]))
		}
		return out, nil
	case TypeUint64:
		out := make([]uint64, n)
		for i := range out {
			out[i] = binary.LittleEndian.Uint64(b[i*8:])
		}
		return out, nil
	case TypeFloat64:
		out := make([]float64, n)
		for i := range out {
			out[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[i*8:]))
		}
		return out, nil
	case TypeComplex64:
		out := make([]complex64, n)
		for i := range out {
			re := math.Float32frombits(binary.LittleEndian.Uint32(b[i*8:]))
			im := math.Float32frombits(binary.LittleEndian.Uint32(b[i*8+4:]))
			out[i] = complex(re, im)
		}
		return out, nil
	case TypeExtendedFloat:
		out := make([]Extended, n)
		for i := range out {
			out[i] = decodeExtended(b[i*extendedSize:])
		}
		return out, nil
	case TypeTimestamp:
		out := make([]Timestamp, n)
		for i := range out {
			off := i * 16
			out[i] = Timestamp{
				Fraction: binary.LittleEndian.Uint64(b[off:]),
				Seconds:  int64(binary.LittleEndian.Uint64(b[off+8:])),
			}
		}
		return out, nil
	case TypeComplex128:
		out := make([]complex128, n)
		for i := range out {
			off := i * 16
			re := math.Float64frombits(binary.LittleEndian.Uint64(b[off:]))
			im := math.Float64frombits(binary.LittleEndian.Uint64(b[off+8:]))
			out[i] = complex(re, im)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, dt)
	}
}

func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendU64(b []byte, v uint64) []byte {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], v)
	return append(b, tmp[:]...)
}

// decodeStrings reads a channel string block: n cumulative end-offsets
// (uint32) followed by the concatenated UTF-8 bytes.
//
// The NI article says the table holds "the offset of the first character of
// each string". LabVIEW files, and the readers that open them (npTDMS and the
// Rust tdms crate), store the cumulative end offset of each string instead,
// with an implicit start of zero. Empty strings repeat the previous end
// offset. This package reads and writes that end-offset table. A start-offset
// table (first entry 0) would not round-trip through LabVIEW.
func decodeStrings(block []byte, n int, bo binary.ByteOrder) ([]string, error) {
	if n == 0 {
		if len(block) != 0 {
			return nil, ErrCorrupt
		}
		return []string{}, nil
	}
	if n < 0 || len(block) < 4*n {
		return nil, ErrTruncated
	}
	payload := block[4*n:]
	out := make([]string, n)
	var prev uint32
	for i := 0; i < n; i++ {
		end := bo.Uint32(block[i*4:])
		if end < prev || int(end) > len(payload) {
			return nil, ErrCorrupt
		}
		out[i] = string(payload[prev:end])
		prev = end
	}
	if int(prev) != len(payload) {
		return nil, ErrCorrupt
	}
	return out, nil
}

func appendStringBlock(dst []byte, strs []string) ([]byte, error) {
	var total uint64
	for _, s := range strs {
		if uint64(len(s)) > math.MaxUint32 {
			return nil, fmt.Errorf("%w: string longer than 4 GiB", ErrArgument)
		}
		total += uint64(len(s))
		if total > math.MaxUint32 {
			return nil, fmt.Errorf("%w: string block longer than 4 GiB", ErrArgument)
		}
	}
	base := len(dst)
	dst = append(dst, make([]byte, 4*len(strs))...)
	off := uint32(0)
	for i, s := range strs {
		off += uint32(len(s))
		binary.LittleEndian.PutUint32(dst[base+i*4:], off)
	}
	for _, s := range strs {
		dst = append(dst, s...)
	}
	return dst, nil
}

func stringBlockSize(strs []string) (uint64, error) {
	var n uint64
	var bytes uint64
	for _, s := range strs {
		n += 4
		bytes += uint64(len(s))
		if bytes > math.MaxUint32 {
			return 0, fmt.Errorf("%w: string block longer than 4 GiB", ErrArgument)
		}
	}
	return n + bytes, nil
}

func asTimestamp(v any) (Timestamp, bool) {
	switch t := v.(type) {
	case Timestamp:
		return t, true
	case time.Time:
		return NewTimestamp(t), true
	default:
		return Timestamp{}, false
	}
}

func asExtended(v any) (Extended, bool) {
	switch t := v.(type) {
	case Extended:
		return t, true
	case float64:
		return ExtendedFromFloat64(t), true
	case float32:
		return ExtendedFromFloat64(float64(t)), true
	default:
		return Extended{}, false
	}
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

func asUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint:
		return uint64(n), true
	case uint8:
		return uint64(n), true
	case uint16:
		return uint64(n), true
	case uint32:
		return uint64(n), true
	case uint64:
		return n, true
	default:
		return 0, false
	}
}

func asFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		if i, ok := asInt64(v); ok {
			return float64(i), true
		}
		if u, ok := asUint64(v); ok {
			return float64(u), true
		}
		return 0, false
	}
}
