package tdms

import "math"

// maxInt is math.MaxInt: the largest sample count or byte length an int
// can hold on this platform. The bounds checks that only bind on a 32-bit
// build compare against it instead of the constant, so the
// tests in intlimit_test.go can lower it and exercise those checks, and
// their boundaries, on a 64-bit build. Production code never assigns it.
var maxInt = math.MaxInt
