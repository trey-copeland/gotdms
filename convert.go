package tdms

import (
	"fmt"
	"math"
	"time"
)

// Float64s reads the channel and converts numeric values to float64.
// Bools become 0 or 1. Extended values use Extended.Float64 (best effort).
// Integers above 2^53 are not exact in float64. Strings, timestamps, and
// complex values return ErrType.
func (c *Channel) Float64s() ([]float64, error) {
	if err := fitsInt(c.total, 8); err != nil {
		return nil, wrap("read", c.path, err)
	}
	v, err := c.Values()
	if err != nil {
		return nil, err
	}
	out, err := toFloat64s(v)
	if err != nil {
		return nil, wrap("read", c.path, err)
	}
	return out, nil
}

// Int64s reads an integer or bool channel into []int64. uint64 values above
// math.MaxInt64 and non-integer channels return ErrType.
func (c *Channel) Int64s() ([]int64, error) {
	if err := fitsInt(c.total, 8); err != nil {
		return nil, wrap("read", c.path, err)
	}
	v, err := c.Values()
	if err != nil {
		return nil, err
	}
	out, err := toInt64s(v)
	if err != nil {
		return nil, wrap("read", c.path, err)
	}
	return out, nil
}

// Strings reads a string channel.
func (c *Channel) Strings() ([]string, error) {
	v, err := c.Values()
	if err != nil {
		return nil, err
	}
	s, ok := v.([]string)
	if !ok {
		return nil, wrap("read", c.path, fmt.Errorf("%w: channel type %s", ErrType, c.dt))
	}
	return s, nil
}

// Bools reads a boolean channel.
func (c *Channel) Bools() ([]bool, error) {
	v, err := c.Values()
	if err != nil {
		return nil, err
	}
	s, ok := v.([]bool)
	if !ok {
		return nil, wrap("read", c.path, fmt.Errorf("%w: channel type %s", ErrType, c.dt))
	}
	return s, nil
}

// Timestamps reads a timestamp channel without rounding to nanoseconds.
func (c *Channel) Timestamps() ([]Timestamp, error) {
	v, err := c.Values()
	if err != nil {
		return nil, err
	}
	s, ok := v.([]Timestamp)
	if !ok {
		return nil, wrap("read", c.path, fmt.Errorf("%w: channel type %s", ErrType, c.dt))
	}
	return s, nil
}

// Times reads a timestamp channel as UTC time.Time values. A timestamp that
// does not fit in time.Time returns an error; use Timestamps for the raw fields.
func (c *Channel) Times() ([]time.Time, error) {
	ts, err := c.Timestamps()
	if err != nil {
		return nil, err
	}
	out := make([]time.Time, len(ts))
	for i, t := range ts {
		tv, ok := t.Time()
		if !ok {
			return nil, wrapf("read", c.path, ErrArgument, "timestamp %d (%d s) does not fit in time.Time", i, t.Seconds)
		}
		out[i] = tv
	}
	return out, nil
}

func toFloat64s(v any) ([]float64, error) {
	switch s := v.(type) {
	case []float64:
		return s, nil
	case []float32:
		out := make([]float64, len(s))
		for i, x := range s {
			out[i] = float64(x)
		}
		return out, nil
	case []int8:
		return mapNum(len(s), func(i int) float64 { return float64(s[i]) }), nil
	case []int16:
		return mapNum(len(s), func(i int) float64 { return float64(s[i]) }), nil
	case []int32:
		return mapNum(len(s), func(i int) float64 { return float64(s[i]) }), nil
	case []int64:
		return mapNum(len(s), func(i int) float64 { return float64(s[i]) }), nil
	case []uint8:
		return mapNum(len(s), func(i int) float64 { return float64(s[i]) }), nil
	case []uint16:
		return mapNum(len(s), func(i int) float64 { return float64(s[i]) }), nil
	case []uint32:
		return mapNum(len(s), func(i int) float64 { return float64(s[i]) }), nil
	case []uint64:
		return mapNum(len(s), func(i int) float64 { return float64(s[i]) }), nil
	case []bool:
		return mapNum(len(s), func(i int) float64 {
			if s[i] {
				return 1
			}
			return 0
		}), nil
	case []Extended:
		return mapNum(len(s), func(i int) float64 { return s[i].Float64() }), nil
	case nil:
		return []float64{}, nil
	default:
		return nil, fmt.Errorf("%w: cannot convert %T to float64", ErrType, v)
	}
}

func toInt64s(v any) ([]int64, error) {
	switch s := v.(type) {
	case []int64:
		return s, nil
	case []int8:
		return mapI64(len(s), func(i int) int64 { return int64(s[i]) }), nil
	case []int16:
		return mapI64(len(s), func(i int) int64 { return int64(s[i]) }), nil
	case []int32:
		return mapI64(len(s), func(i int) int64 { return int64(s[i]) }), nil
	case []uint8:
		return mapI64(len(s), func(i int) int64 { return int64(s[i]) }), nil
	case []uint16:
		return mapI64(len(s), func(i int) int64 { return int64(s[i]) }), nil
	case []uint32:
		return mapI64(len(s), func(i int) int64 { return int64(s[i]) }), nil
	case []uint64:
		out := make([]int64, len(s))
		for i, x := range s {
			if x > math.MaxInt64 {
				return nil, fmt.Errorf("%w: uint64 value exceeds int64", ErrType)
			}
			out[i] = int64(x)
		}
		return out, nil
	case []bool:
		return mapI64(len(s), func(i int) int64 {
			if s[i] {
				return 1
			}
			return 0
		}), nil
	case nil:
		return []int64{}, nil
	default:
		return nil, fmt.Errorf("%w: cannot convert %T to int64", ErrType, v)
	}
}

// fitsInt returns ErrUnsupported when n values of width bytes take more
// than math.MaxInt bytes. It only binds on 32-bit platforms, where the
// []float64 or []int64 a conversion allocates could not be indexed.
func fitsInt(n int64, width int) error {
	if n < 0 || width <= 0 || n > int64(math.MaxInt/width) {
		return fmt.Errorf("%w: %d values of %d bytes exceed int", ErrUnsupported, n, width)
	}
	return nil
}

func mapNum(n int, f func(int) float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}

func mapI64(n int, f func(int) int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}
