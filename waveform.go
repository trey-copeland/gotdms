package tdms

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"time"
	"unsafe"
)

// Numeric is the set of analog-waveform sample types. LabVIEW stores a
// waveform as a numeric Y array plus wf_start_time, wf_increment, and
// wf_samples. Bool, string, timestamp, extended, and complex channels are
// not waveforms.
type Numeric interface {
	int8 | int16 | int32 | int64 |
		uint8 | uint16 | uint32 | uint64 |
		float32 | float64
}

// Waveform is one analog waveform: t0, dt, Y, and an optional unit.
//
// Start is wf_start_time as a TDMS Timestamp, not a time.Time, so the full
// 2^-64 second fraction round-trips. Call Start.Time for UTC at nanosecond
// resolution. HasStart reports whether the file stored wf_start_time. A zero
// Start with HasStart false means the property was absent. A zero Start with
// HasStart true is the TDMS epoch (1904-01-01 UTC), which is a real t0.
//
// Increment is wf_increment in seconds (float64, the on-disk type). Dt rounds
// that value to the nearest time.Duration. HasIncrement false means the
// property was absent; a stored 0 is a real increment and yields Dt 0.
//
// Y holds raw channel samples. ReadWaveform and Waveform do not apply a
// LinearScale. Call ApplyScale or ScaledWaveform for engineering units.
// WriteWaveform writes Y as given and does not invert a scale.
//
// Unit is the channel unit from Unit (unit_string, then NI_UnitDescription).
// WriteWaveform writes unit_string only when Unit is non-empty, and does not
// clear a unit that was already set.
type Waveform[T Numeric] struct {
	Start     Timestamp
	Increment float64
	Y         []T
	Unit      string

	hasStart     bool
	hasIncrement bool
}

// HasStart reports whether wf_start_time was present.
func (w Waveform[T]) HasStart() bool { return w.hasStart }

// HasIncrement reports whether wf_increment was present.
func (w Waveform[T]) HasIncrement() bool { return w.hasIncrement }

// Dt returns Increment rounded to the nearest nanosecond. Values outside
// time.Duration's range saturate at the extremes. NaN and Inf yield 0.
// Increment remains the authoritative wf_increment value.
func (w Waveform[T]) Dt() time.Duration { return durationFromSeconds(w.Increment) }

// TimeAt returns the time of sample i, t0 + i*dt, computed from the index
// in one step rather than by adding dt repeatedly. ok is false when i is
// outside Y, when no start time was stored, or when the result does not fit
// in time.Time. A missing increment is treated as 0, so every sample is t0.
func (w Waveform[T]) TimeAt(i int) (time.Time, bool) {
	if i < 0 || i >= len(w.Y) || !w.hasStart {
		return time.Time{}, false
	}
	return timeAt(w.Start, w.Increment, i)
}

// Times returns one time.Time per sample using the same rule as TimeAt.
// A missing start time or a value that does not fit in time.Time returns an error.
//
// The result is one time.Time per sample. That allocation is inherent to the
// return type; there is no packed timestamp slice behind it.
func (w Waveform[T]) Times() ([]time.Time, error) {
	if !w.hasStart {
		return nil, fmt.Errorf("%w: waveform has no wf_start_time", ErrArgument)
	}
	out := make([]time.Time, len(w.Y))
	for i := range w.Y {
		t, ok := timeAt(w.Start, w.Increment, i)
		if !ok {
			return nil, fmt.Errorf("%w: sample %d time does not fit in time.Time", ErrArgument, i)
		}
		out[i] = t
	}
	return out, nil
}

// ApplyScale returns a float64 waveform whose samples are scale.Apply of Y.
// Start, increment, presence flags, and Unit are copied. The unit string is
// not rewritten. Integers wider than 2^53 are not exact in float64, matching
// Float64s. This does not run during ReadWaveform.
func (w Waveform[T]) ApplyScale(scale LinearScale) Waveform[float64] {
	y := make([]float64, len(w.Y))
	for i, v := range w.Y {
		y[i] = scale.Apply(float64(v))
	}
	return Waveform[float64]{
		Start:        w.Start,
		Increment:    w.Increment,
		Y:            y,
		Unit:         w.Unit,
		hasStart:     w.hasStart,
		hasIncrement: w.hasIncrement,
	}
}

// WaveformHeader is a channel's waveform timing without its samples.
//
// Waveform and ReadWaveform load every sample, the same cost as Values. A
// multi-gigabyte channel should be walked with Cursor. Timestamp each batch
// with TimeAt on this header, passing the channel's absolute sample index
// (the index of the first value in the batch, plus the offset inside it),
// not the index within the batch alone.
type WaveformHeader struct {
	Start        Timestamp
	Increment    float64
	Unit         string
	Samples      uint64
	HasStart     bool
	HasIncrement bool
	HasUnit      bool
	HasSamples   bool
}

// Dt returns Increment rounded to the nearest nanosecond. See Waveform.Dt.
func (h WaveformHeader) Dt() time.Duration { return durationFromSeconds(h.Increment) }

// TimeAt returns t0 + i*dt for absolute sample index i. ok is false when i
// is negative, when no start time was stored, or when the result does not
// fit in time.Time. i is not checked against Channel.Len; chunk callers pass
// the absolute index they are decoding. A missing increment is treated as 0.
func (h WaveformHeader) TimeAt(i int) (time.Time, bool) {
	if i < 0 || !h.HasStart {
		return time.Time{}, false
	}
	return timeAt(h.Start, h.Increment, i)
}

// WaveformHeader reads wf_start_time, wf_increment, wf_samples, and the unit
// without loading raw samples. Unit follows Channel.Unit (unit_string, then
// NI_UnitDescription). Samples is the wf_samples property, which can differ
// from Len when later segments append values.
func (c *Channel) WaveformHeader() WaveformHeader {
	var h WaveformHeader
	if c == nil {
		return h
	}
	if p, ok := findProp(c.props, PropStartTime); ok {
		switch v := p.Value.(type) {
		case Timestamp:
			h.Start = v
			h.HasStart = true
		case time.Time:
			h.Start = NewTimestamp(v)
			h.HasStart = true
		}
	}
	if inc, ok := c.Increment(); ok {
		h.Increment = inc
		h.HasIncrement = true
	}
	if u, ok := c.Unit(); ok {
		h.Unit = u
		h.HasUnit = true
	}
	if n, ok := c.SampleCount(); ok {
		h.Samples = n
		h.HasSamples = true
	}
	return h
}

// Waveform reads a float64 channel as an analog waveform. Other numeric
// types use ReadWaveform. Missing wf_start_time or wf_increment is not an
// error; the field is zero and HasStart or HasIncrement is false. This loads
// every sample. Stream with Cursor and WaveformHeader for large channels.
func (c *Channel) Waveform() (Waveform[float64], error) {
	return ReadWaveform[float64](c)
}

// ReadWaveform loads every sample of c as T plus the waveform properties.
//
// The channel storage type must be T. Float-with-unit channels use the same
// Go slice as the underlying float. A channel that never received a raw-data
// index yields an empty Y and a nil error. A type mismatch returns ErrType.
//
// Missing wf_start_time or wf_increment yields a zero field and a false
// HasStart or HasIncrement. Y is still returned. Linear scales are not applied.
//
// The read allocates the full Y slice, like Values. For chunked access use
// Cursor and timestamp samples with WaveformHeader.TimeAt.
func ReadWaveform[T Numeric](c *Channel) (Waveform[T], error) {
	if c == nil {
		return Waveform[T]{}, wrap("read", "", fmt.Errorf("%w: nil channel", ErrArgument))
	}
	h := c.WaveformHeader()
	v, err := c.Values()
	if err != nil {
		return Waveform[T]{}, err
	}
	y, ok := waveformSamples[T](v)
	if !ok {
		return Waveform[T]{}, wrap("read", c.path, fmt.Errorf("%w: channel type %s", ErrType, c.dt))
	}
	return Waveform[T]{
		Start:        h.Start,
		Increment:    h.Increment,
		Y:            y,
		Unit:         h.Unit,
		hasStart:     h.HasStart,
		hasIncrement: h.HasIncrement,
	}, nil
}

func waveformSamples[T Numeric](v any) ([]T, bool) {
	switch s := v.(type) {
	case []T:
		return s, true
	case nil:
		return []T{}, true
	default:
		return nil, false
	}
}

// ScaledWaveform reads the channel as T and, when the channel has a
// LinearScale, applies it to produce float64 engineering samples. With no
// scale, Y is converted to float64 and left unchanged. ReadWaveform never
// scales. WriteWaveform does not invert this conversion.
//
// The returned waveform is the only sample allocation. A float64 channel is
// scaled in place in that slice. Other numeric types are decoded from a
// short scratch window into the same slice. The channel's stored samples
// are not modified. ApplyScale on an existing Waveform still allocates a
// new Y so the receiver is left unchanged.
func ScaledWaveform[T Numeric](c *Channel) (Waveform[float64], error) {
	if c == nil {
		return Waveform[float64]{}, wrap("read", "", fmt.Errorf("%w: nil channel", ErrArgument))
	}
	h := c.WaveformHeader()
	if !c.hasDT {
		if c.total == 0 {
			return Waveform[float64]{
				Start: h.Start, Increment: h.Increment, Y: []float64{}, Unit: h.Unit,
				hasStart: h.HasStart, hasIncrement: h.HasIncrement,
			}, nil
		}
		return Waveform[float64]{}, wrap("read", c.path, ErrCorrupt)
	}
	var zero T
	if err := sampleMatches(c.dt, zero); err != nil {
		return Waveform[float64]{}, wrap("read", c.path, err)
	}
	scale := LinearScale{Slope: 1, Intercept: 0}
	if sc, ok := c.LinearScale(); ok {
		scale = sc
	}
	y, err := c.readScaled(scale)
	if err != nil {
		return Waveform[float64]{}, wrap("read", c.path, err)
	}
	return Waveform[float64]{
		Start: h.Start, Increment: h.Increment, Y: y, Unit: h.Unit,
		hasStart: h.HasStart, hasIncrement: h.HasIncrement,
	}, nil
}

func (c *Channel) readScaled(scale LinearScale) ([]float64, error) {
	if c.file == nil || c.file.r == nil {
		return nil, ErrClosed
	}
	n := c.Len()
	y := make([]float64, n)
	if n == 0 {
		return y, nil
	}
	if c.dt.storage() == TypeFloat64 && nativeLittleEndian {
		raw := unsafe.Slice((*byte)(unsafe.Pointer(&y[0])), n*8)
		if err := c.fillPacked(c.file.r, 0, n, 8, raw); err != nil {
			return nil, err
		}
		applyInPlace(y, scale)
		return y, nil
	}
	sz, ok := c.dt.Size()
	if !ok || sz <= 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, c.dt)
	}
	done := 0
	for done < n {
		take := n - done
		if take > strideChunk/sz {
			take = strideChunk / sz
		}
		if take < 1 {
			take = 1
		}
		buf := borrowScratch(take * sz)
		err := c.fillPacked(c.file.r, done, take, sz, buf[:take*sz])
		if err != nil {
			releaseScratch(buf)
			return nil, err
		}
		expandScaled(c.dt, buf[:take*sz], y[done:done+take], scale)
		releaseScratch(buf)
		done += take
	}
	return y, nil
}

func applyInPlace(y []float64, scale LinearScale) {
	if scale.Slope == 1 && scale.Intercept == 0 {
		return
	}
	s, b := scale.Slope, scale.Intercept
	for i, v := range y {
		y[i] = v*s + b
	}
}

func expandScaled(dt DataType, b []byte, y []float64, scale LinearScale) {
	s, icept := scale.Slope, scale.Intercept
	switch dt.storage() {
	case TypeInt8:
		for i := range y {
			y[i] = float64(int8(b[i]))*s + icept
		}
	case TypeUint8:
		for i := range y {
			y[i] = float64(b[i])*s + icept
		}
	case TypeInt16:
		for i := range y {
			y[i] = float64(int16(binary.LittleEndian.Uint16(b[i*2:])))*s + icept
		}
	case TypeUint16:
		for i := range y {
			y[i] = float64(binary.LittleEndian.Uint16(b[i*2:]))*s + icept
		}
	case TypeInt32:
		for i := range y {
			y[i] = float64(int32(binary.LittleEndian.Uint32(b[i*4:])))*s + icept
		}
	case TypeUint32:
		for i := range y {
			y[i] = float64(binary.LittleEndian.Uint32(b[i*4:]))*s + icept
		}
	case TypeFloat32:
		for i := range y {
			y[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:])))*s + icept
		}
	case TypeInt64:
		for i := range y {
			y[i] = float64(int64(binary.LittleEndian.Uint64(b[i*8:])))*s + icept
		}
	case TypeUint64:
		for i := range y {
			y[i] = float64(binary.LittleEndian.Uint64(b[i*8:]))*s + icept
		}
	case TypeFloat64:
		for i := range y {
			y[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[i*8:]))*s + icept
		}
	default:
		for i := range y {
			y[i] = math.NaN()
		}
	}
}

// WriteWaveform appends Y and sets the LabVIEW waveform properties
// wf_start_time, wf_increment, and wf_samples. When Unit is non-empty it also
// sets unit_string. On a channel that does not already have those names, the
// properties are stored in that order. An existing name is updated in place.
//
// wf_samples is len(Y) for this call (int32 when it fits in int32, otherwise
// int64), not the total length after later appends. Start and Increment are
// written even when they are zero, so a waveform at the TDMS epoch or with
// dt 0 is representable. Y is written raw; a LinearScale is not inverted.
//
// wf must be a Waveform[T] for a numeric T, and the channel type must match T.
// WriteWaveform buffers Y until Flush. StreamWaveform declares the count and
// writes Y in chunks.
func (c *ChannelWriter) WriteWaveform(wf any) error {
	if c == nil || c.w == nil {
		return wrap("write", "", fmt.Errorf("%w: nil channel", ErrArgument))
	}
	switch v := wf.(type) {
	case Waveform[int8]:
		return writeWaveform(c, v)
	case Waveform[int16]:
		return writeWaveform(c, v)
	case Waveform[int32]:
		return writeWaveform(c, v)
	case Waveform[int64]:
		return writeWaveform(c, v)
	case Waveform[uint8]:
		return writeWaveform(c, v)
	case Waveform[uint16]:
		return writeWaveform(c, v)
	case Waveform[uint32]:
		return writeWaveform(c, v)
	case Waveform[uint64]:
		return writeWaveform(c, v)
	case Waveform[float32]:
		return writeWaveform(c, v)
	case Waveform[float64]:
		return writeWaveform(c, v)
	default:
		return wrap("write", c.ch.path, fmt.Errorf("%w: WriteWaveform wants Waveform[T]", ErrType))
	}
}

func writeWaveform[T Numeric](c *ChannelWriter, wf Waveform[T]) error {
	if c.w.closed {
		return wrap("write", c.ch.path, ErrClosed)
	}
	var zero T
	if err := sampleMatches(c.ch.dt, zero); err != nil {
		return wrap("write", c.ch.path, err)
	}
	if err := c.SetProperty(PropStartTime, wf.Start); err != nil {
		return err
	}
	if err := c.SetProperty(PropIncrement, wf.Increment); err != nil {
		return err
	}
	if err := c.SetProperty(PropSamples, len(wf.Y)); err != nil {
		return err
	}
	if wf.Unit != "" {
		if err := c.SetProperty(PropUnitString, wf.Unit); err != nil {
			return err
		}
	}
	return c.Write(wf.Y)
}

func sampleMatches(dt DataType, sample any) error {
	switch dt.storage() {
	case TypeInt8:
		_, ok := sample.(int8)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeInt16:
		_, ok := sample.(int16)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeInt32:
		_, ok := sample.(int32)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeInt64:
		_, ok := sample.(int64)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeUint8:
		_, ok := sample.(uint8)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeUint16:
		_, ok := sample.(uint16)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeUint32:
		_, ok := sample.(uint32)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeUint64:
		_, ok := sample.(uint64)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeFloat32:
		_, ok := sample.(float32)
		if !ok {
			return typeErr(dt, sample)
		}
	case TypeFloat64:
		_, ok := sample.(float64)
		if !ok {
			return typeErr(dt, sample)
		}
	default:
		return fmt.Errorf("%w: %s is not a waveform sample type", ErrType, dt)
	}
	return nil
}

func durationFromSeconds(sec float64) time.Duration {
	if math.IsNaN(sec) || math.IsInf(sec, 0) {
		return 0
	}
	ns := sec * 1e9
	if ns >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	if ns <= float64(math.MinInt64) {
		return time.Duration(math.MinInt64)
	}
	return time.Duration(math.Round(ns))
}

// timeAt computes t0 + i*dt. i*dt is one multiplication, not a running sum of dt.
func timeAt(start Timestamp, dt float64, i int) (time.Time, bool) {
	ts, ok := addSeconds(start, float64(i)*dt)
	if !ok {
		return time.Time{}, false
	}
	return ts.Time()
}

// addSeconds adds a float64 second offset to ts. The fractional second is an
// absolute 2^-64 offset and stays non-negative, including when sec is negative.
func addSeconds(ts Timestamp, sec float64) (Timestamp, bool) {
	if math.IsNaN(sec) || math.IsInf(sec, 0) {
		return Timestamp{}, false
	}
	whole := math.Trunc(sec)
	frac := sec - whole
	if frac < 0 {
		whole--
		frac++
	}
	if whole > float64(math.MaxInt64) || whole < float64(math.MinInt64) {
		return Timestamp{}, false
	}
	w := int64(whole)
	seconds, ok := addSigned(ts.Seconds, w)
	if !ok {
		return Timestamp{}, false
	}
	extra := fractionFromSeconds(frac)
	sum, carry := bits.Add64(ts.Fraction, extra, 0)
	if carry != 0 {
		seconds, ok = addSigned(seconds, 1)
		if !ok {
			return Timestamp{}, false
		}
	}
	return Timestamp{Seconds: seconds, Fraction: sum}, true
}

// fractionFromSeconds converts a fraction in [0, 1) to a 2^-64 second.
func fractionFromSeconds(frac float64) uint64 {
	if frac <= 0 {
		return 0
	}
	if frac >= 1 {
		return ^uint64(0)
	}
	const two32 = 4294967296.0
	hi := frac * two32
	hiInt := uint64(hi)
	if hiInt > 0xFFFFFFFF {
		hiInt = 0xFFFFFFFF
	}
	rem := hi - float64(hiInt)
	lo := math.Round(rem * two32)
	if lo >= two32 {
		lo -= two32
		hiInt++
	}
	if lo < 0 {
		lo = 0
	}
	if hiInt > 0xFFFFFFFF {
		return ^uint64(0)
	}
	return hiInt<<32 | uint64(lo)
}
