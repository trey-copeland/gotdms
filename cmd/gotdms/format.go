package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/trey-copeland/gotdms"
)

func formatScalar(v any) (string, error) {
	switch x := v.(type) {
	case int8:
		return strconv.FormatInt(int64(x), 10), nil
	case int16:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint8:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	case string:
		return x, nil
	case tdms.Timestamp:
		tv, ok := x.Time()
		if !ok {
			return fmt.Sprintf("seconds=%d fraction=%d", x.Seconds, x.Fraction), nil
		}
		return tv.UTC().Format(time.RFC3339Nano), nil
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano), nil
	case tdms.Extended:
		return fmt.Sprintf("significand=%#x exponent=%#x", x.Significand, x.Exponent), nil
	case complex64:
		return formatComplex(float64(real(x)), float64(imag(x)), 32), nil
	case complex128:
		return formatComplex(real(x), imag(x), 64), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("unsupported value %T", v)
	}
}

func formatComplex(re, im float64, bits int) string {
	return strconv.FormatFloat(re, 'g', -1, bits) + formatImag(im, bits)
}

func formatImag(im float64, bits int) string {
	s := strconv.FormatFloat(im, 'g', -1, bits)
	if len(s) == 0 || s[0] != '-' {
		s = "+" + s
	}
	return s + "i"
}

func formatSample(batch any, i int) (string, error) {
	switch s := batch.(type) {
	case []int8:
		return formatScalar(s[i])
	case []int16:
		return formatScalar(s[i])
	case []int32:
		return formatScalar(s[i])
	case []int64:
		return formatScalar(s[i])
	case []uint8:
		return formatScalar(s[i])
	case []uint16:
		return formatScalar(s[i])
	case []uint32:
		return formatScalar(s[i])
	case []uint64:
		return formatScalar(s[i])
	case []float32:
		return formatScalar(s[i])
	case []float64:
		return formatScalar(s[i])
	case []bool:
		return formatScalar(s[i])
	case []string:
		return formatScalar(s[i])
	case []tdms.Timestamp:
		return formatScalar(s[i])
	case []tdms.Extended:
		return formatScalar(s[i])
	case []complex64:
		return formatScalar(s[i])
	case []complex128:
		return formatScalar(s[i])
	default:
		return "", fmt.Errorf("unsupported batch %T", batch)
	}
}

func propJSONValue(v any) any {
	switch x := v.(type) {
	case tdms.Timestamp:
		tv, ok := x.Time()
		if !ok {
			return map[string]any{"seconds": x.Seconds, "fraction": x.Fraction}
		}
		return tv.UTC().Format(time.RFC3339Nano)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case tdms.Extended:
		return map[string]any{"significand": x.Significand, "exponent": x.Exponent}
	case complex64:
		return map[string]any{"real": real(x), "imag": imag(x)}
	case complex128:
		return map[string]any{"real": real(x), "imag": imag(x)}
	default:
		return v
	}
}
