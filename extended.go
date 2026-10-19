package tdms

import "math"

// extendedSize is the on-disk size of tdsTypeExtendedFloat.
//
// The NI article does not state the width. This package uses 10 bytes, the
// x86 80-bit extended format described by the readers that document a layout:
// a 64-bit significand with an explicit integer bit, then a 15-bit biased
// exponent and a sign bit. The layout is derived from that description of
// the spec. It has not been checked against a file written by LabVIEW or
// another NI tool, and npTDMS does not produce this type, so there is no
// external fixture in this repository. Platforms that pad extended floats
// to 16 bytes are not auto-detected. Do not change the 10-byte width without
// an NI fixture.
const extendedSize = 10

const extendedBias = 16383

// Extended is one 80-bit extended-precision value.
//
// Significand is the raw 64-bit field (bit 63 is the explicit integer bit).
// Exponent is the raw 16-bit field: bits 0-14 are the biased exponent and bit
// 15 is the sign.
//
// Little-endian segments store the significand first, then the exponent, each
// field little-endian. Big-endian segments store the exponent first, then the
// significand, each field big-endian. Float64 is a best-effort conversion to
// IEEE-754 binary64; signaling NaNs and the low significand bits are not
// preserved by that conversion. Round-trip the Extended value itself when the
// bits matter.
type Extended struct {
	Significand uint64
	Exponent    uint16
}

// Float64 converts e to float64. Infinities and NaNs map to the corresponding
// float64 values. The conversion is best-effort: a 64-bit significand does not
// fit in a float64 mantissa.
func (e Extended) Float64() float64 {
	sign := e.Exponent >> 15
	exp := int(e.Exponent & 0x7FFF)
	sig := e.Significand
	var f float64
	switch {
	case exp == 0 && sig == 0:
		f = 0
	case exp == 0x7FFF && sig == 0:
		f = math.Inf(1)
	case exp == 0x7FFF:
		f = math.NaN()
	default:
		// value = ± significand × 2^(exp - bias - 63)
		f = math.Ldexp(float64(sig), exp-extendedBias-63)
	}
	if sign != 0 {
		f = math.Copysign(f, -1)
	}
	return f
}

// ExtendedFromFloat64 encodes f as an 80-bit extended value. The result's
// Float64 equals f for every finite float64, ±Inf, and NaN (NaN payloads are
// not preserved).
func ExtendedFromFloat64(f float64) Extended {
	if f == 0 {
		if math.Signbit(f) {
			return Extended{Exponent: 0x8000}
		}
		return Extended{}
	}
	if math.IsNaN(f) {
		return Extended{Significand: 0xC000000000000000, Exponent: 0x7FFF}
	}
	sign := uint16(0)
	if math.Signbit(f) {
		sign = 0x8000
		f = math.Copysign(f, 1)
	}
	if math.IsInf(f, 1) {
		return Extended{Exponent: sign | 0x7FFF}
	}
	frac, exp := math.Frexp(f) // f = frac × 2^exp, frac in [0.5, 1)
	// Move to [1, 2) so the explicit integer bit is set: frac × 2.
	frac *= 2
	exp--
	sig := uint64(math.Ldexp(frac, 63))
	biased := exp + extendedBias
	if biased <= 0 {
		// Underflow to zero. Subnormals are not synthesized.
		return Extended{Exponent: sign}
	}
	if biased >= 0x7FFF {
		return Extended{Exponent: sign | 0x7FFF}
	}
	return Extended{Significand: sig, Exponent: sign | uint16(biased)}
}
