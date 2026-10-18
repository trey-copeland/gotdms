package tdms

import "encoding/binary"

// nativeLittleEndian reports whether plain integers, IEEE floats, and Go
// complex values have the same in-memory byte order as a little-endian TDMS
// segment. Bulk copies use this; big-endian hosts and big-endian segments
// fall back to scalar decoding after the bytes are normalized to little-endian.
//
// binary.NativeEndian and binary.LittleEndian have distinct named types, so
// they are compared by the uint16 they read from the same bytes.
var nativeLittleEndian = binary.NativeEndian.Uint16([]byte{0x12, 0x34}) == binary.LittleEndian.Uint16([]byte{0x12, 0x34})
