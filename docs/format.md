# Format and support

[Back to the docs index](README.md)

TDMS (Technical Data Management Streaming) is the binary format LabVIEW and other National Instruments tools use for measurement data. A file is a sequence of segments. Each segment has a 28-byte lead-in, optional metadata (object paths, raw-data indexes, properties), and optional raw samples. Objects form a tree of one file (`/`), any number of groups (`/'Group'`), and channels (`/'Group'/'Channel'`). Only channels hold raw data.

The byte layout follows [TDMS File Format Internal Structure](https://www.ni.com/en/support/documentation/supplemental/07/tdms-file-format-internal-structure.html) (NI, updated Nov 12, 2024). Constants in `format.go`, `datatype.go`, and `value.go` cite that article where the on-disk value comes from it.

## Limitations

**DAQmx raw data is decoded for the layouts NI hardware writes, and rejected otherwise.** The supported index words, the layouts, and what still returns `ErrDAQmx` are in [DAQmx](daqmx.md).

**Extended float (`tdsTypeExtendedFloat`, `0x0B`) is spec-derived and unverified against NI.** The article does not give a width. This package uses 10 bytes, the x86 80-bit layout documented by other readers. No LabVIEW file in this repository checks that layout, and npTDMS does not write the type. The width is not changed on speculation.

Other limits are in the support matrix below: big-endian segments are read and not written, fixed-point returns `ErrUnsupported`, and an NI `*.tdms_index` (`TDSh`) file returns `ErrIndexFile`.

## Support

| Feature | Read | Write |
| --- | --- | --- |
| Version 4713 (TDMS 2.0) | yes | yes |
| Version 4712 (TDMS 1.0) | yes, same layout | no |
| Little-endian segments | yes | yes |
| Big-endian segments | yes | no |
| Interleaved fixed-size channels, including mixed widths | yes | yes, equal lengths |
| Contiguous channels, including unequal lengths | yes | yes |
| Several chunks in one segment | yes | one chunk per `Flush`; `Stream` is one declared-count chunk, fed in pieces |
| `Stream` of fixed-size channels, counts known up front | n/a | yes, seekable or not; memory follows the write buffer and the current chunk |
| `Stream` of string channels | n/a | error `ErrUnsupported` (offset table). Use `Write` |
| Segments with no metadata (repeated layout) | yes | yes |
| `kTocNewObjList` clear (append a channel, keep order) | yes | yes |
| `kTocNewObjList` set (channel removed or reordered) | yes | yes |
| Channels that appear or disappear across segments | yes | yes |
| Properties on file, group, and channel, including later overwrites | yes | yes |
| Names with quotes, slashes, spaces, non-ASCII | yes | yes |
| String channels (offset table) | yes | yes, contiguous; a single string channel may also be flagged interleaved |
| Several string channels, or strings mixed with numeric channels, interleaved | error `ErrLayout` | error `ErrLayout` |
| Timestamps (`tdsTypeTimeStamp`, 68 / 0x44) | `[]Timestamp` and `[]time.Time` | `[]Timestamp` or `[]time.Time` |
| Bool (`0x21`, stored as a byte; any non-zero byte reads as true) | yes | yes |
| String (`0x20`) | yes | yes |
| Float with unit `0x19`, `0x1A`, `0x1B` | numeric value; unit is the `unit_string` property, not an inline unit id | same |
| Extended float (`0x0B`) | 10-byte x86 80-bit, spec-derived, no NI fixture; `Float64` is best-effort | yes, as `[]Extended` |
| Complex float `0x08000c`, `0x10000d` | yes | yes |
| DAQmx raw data (`0x1269` and `0x1369` format-changing, `0x126A` digital lines) | yes, raw native values; linear and polynomial scales are opt-in. Other layouts return `ErrDAQmx` | not written |
| Fixed-point `0x4F` | error `ErrUnsupported` | not written |
| `*.tdms_index` (`TDSh`) | error `ErrIndexFile` | not written |
| Incomplete last segment (`next` offset all `0xFF`) | yes, whole values that are present; `File.Status` reports it | sentinel used only until `Flush` patches it |
| Final segment whose finite next offset runs past EOF | `ErrTruncated` by default. `WithLenientTail` keeps earlier segments and whole tail values and sets `File.Status.Truncated` | n/a |
| Torn tail after the last whole segment (short lead-in, metadata past EOF, zeros, garbage, a sentinel segment whose metadata does not parse) | The strict error (`ErrTruncated`, `ErrBadTag`, `ErrVersion`, `ErrCorrupt`) by default. `WithLenientTail` drops it, keeps the whole segments, and sets `Truncated`, `DroppedTail`, and `TailError`. `ErrUnsupported`, and any error inside a segment whose finite length lies within the file, is returned even with the option | n/a |
| `Follow` of a file still being written | new samples from finished segments only | n/a |
| Chunk min/max/mean/RMS | numeric channels, one pass, caller-chosen chunk | n/a |
| Segment-index sidecar (`.gtdx`) | opt-in `WithSegmentIndex`; ignored when stale or corrupt | not written by the TDMS writer |
| `name` property renaming an object (TDMS 2.0) | kept as a normal property; the path does not change | same |

Integer and float type codes are the usual 1–10 (`int8` through `float64`). The NI article's lead-in example labels bytes `0E 00 00 00` as mask `0x1110`. That label is the binary value `0b1110`; the bytes are the mask `0x0E` (metadata, new object list, and raw data). ToC itself is always little-endian. When `TocBigEndian` is set, every other numeric field in the segment is big-endian, including the version and both offsets in the lead-in.

## Format choices

A few details in the article are ambiguous. Each choice is tested.

**String channels.** The article says the raw block starts with "the offset of the first character of each string". Files written by LabVIEW, and the readers that open them (npTDMS, the Rust `tdms` crate), store the cumulative *end* offset of each string instead, with an implicit start of zero. Empty strings repeat the previous end offset. The total-size field in the index is `4*N +` the UTF-8 byte count. This package reads and writes that end-offset table. Property strings are different: a `uint32` byte length followed by the bytes, which is what the article specifies for every string that is not channel raw data.

**DAQmx.** The DAQmx index words, scaler layouts, and scale chains are in [DAQmx](daqmx.md#format-choices).

**Extended float.** The article does not give a width. Values are 10 bytes, the x86 80-bit layout other readers document: significand `uint64` (explicit integer bit), then a biased exponent in the low 15 bits of a `uint16` with the sign in bit 15. Little-endian stores significand then exponent. Big-endian stores exponent then significand, each field big-endian. This layout is derived from that description. It has not been checked against a file written by LabVIEW or another NI tool, and npTDMS does not produce the type, so this repository has no NI fixture for it. `Extended.Float64` rounds into float64 and does not preserve a 64-bit significand or a NaN payload. Round-trip `[]Extended` when the bits matter. Subnormals are not synthesized by `ExtendedFromFloat64`.

**Float with unit.** The article says a LabVIEW float-with-unit becomes a float channel plus a `unit_string` property. There is no unit id in the raw bytes. `TypeFloat32WithUnit`, `TypeFloat64WithUnit`, and `TypeExtendedFloatWithUnit` use the same payload as the plain types. The `DataType` value is preserved so a caller can tell them apart.

**Timestamps.** Seconds are `int64` from 1904-01-01 00:00:00 UTC, Gregorian, leap seconds ignored. The fraction is an unsigned `2^-64` second and is never negative. One half-second before the epoch is `Seconds == -1` and `Fraction == 2^63`. Little-endian stores fraction then seconds. Big-endian stores seconds then fraction (the order npTDMS accepts; the article does not spell it out). `time.Time` conversions round to a nanosecond and are checked to round-trip that nanosecond. Instants that do not fit in a Unix `time.Time` (a few seconds at the `int64` edges) make `Time` report false and `Times` return an error. `Timestamps` still returns the raw fields.

**Index length.** The `uint32` in front of a standard raw-data index includes itself. A fixed-size index is 20 bytes (length, type, dimension, count). A string index is 28 bytes (those fields plus total size). Dimension must be 1. `0xFFFFFFFF` means no raw data. `0x00000000` means the index matches the previous segment.

**Metadata reuse.** A later segment that repeats the same channel order may omit unchanged objects. A segment whose ToC has no metadata reuses the previous segment's indexes entirely; that is how an unchanged layout is appended. Adding a channel at the end does not set `kTocNewObjList`. Removing or reordering channels does, and the segment lists every channel that still has data. A metadata-only segment still updates that order: a full raw-data index is stored even when the segment has no raw bytes, `0x00000000` resumes the stored index, and `0xFFFFFFFF` drops the channel from the raw order without erasing the index. A following raw-only segment uses the order those declarations left behind. The raw-data section of a segment with raw bytes always contains every channel in the current order, including channels whose metadata was omitted. A raw section whose length is not a multiple of that chunk, and that is not an incomplete or lenient tail, is `ErrCorrupt`.

**Groups.** The writer emits a group object for every group, and a file object, on the first segment (and again when their properties change). The reader also creates a group implied by a channel path when the file never stored the group object. The NI worked example is like that, and `TestGoldenNIFirstSegment` decodes it.

**Strings and NUL.** The NI article says a length-prefixed string may be null-terminated, and that the null is ignored on read because the length is stored. This package drops one trailing `0` byte from property strings, object paths, property names, and raw channel strings. A `0` that is not the last byte is kept. The writer does not add a NUL. A value that ends in NUL does not round-trip that byte. npTDMS does not strip it, so the differential files in `testdata/nptdms` do not use a trailing NUL.
