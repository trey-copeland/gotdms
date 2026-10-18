# gotdms

[![CI](https://github.com/trey-copeland/gotdms/actions/workflows/ci.yml/badge.svg)](https://github.com/trey-copeland/gotdms/actions/workflows/ci.yml)

A pure Go reader and writer for NI TDMS 2.0 files. No cgo. The Go package name is `tdms`; the module path is `github.com/trey-copeland/gotdms`.

TDMS (Technical Data Management Streaming) is the binary format LabVIEW and other National Instruments tools use for measurement data. A file is a sequence of segments. Each segment has a 28-byte lead-in, optional metadata (object paths, raw-data indexes, properties), and optional raw samples. Objects form a tree of one file (`/`), any number of groups (`/'Group'`), and channels (`/'Group'/'Channel'`). Only channels hold raw data.

The byte layout follows [TDMS File Format Internal Structure](https://www.ni.com/en/support/documentation/supplemental/07/tdms-file-format-internal-structure.html) (NI, updated Nov 12, 2024). Constants in `format.go`, `datatype.go`, and `value.go` cite that article where the on-disk value comes from it.

## Install

```bash
go get github.com/trey-copeland/gotdms
```

```go
import "github.com/trey-copeland/gotdms"
```

From a checkout of this repository:

```bash
go test ./...
go test -bench=. ./...
```

`go test -fuzz=FuzzReader` compiles the reader fuzzer. It is not part of `go test` by default.

## Write

```go
w, err := tdms.Create("run.tdms")
if err != nil {
    return err
}
defer w.Close()

ch, err := w.Group("acquisition").Channel("temperature", tdms.TypeFloat64)
if err != nil {
    return err
}
if err := ch.SetProperty(tdms.PropUnitString, "degC"); err != nil {
    return err
}
if err := ch.SetProperty(tdms.PropIncrement, 0.1); err != nil {
    return err
}
if err := ch.Write([]float64{21.5, 21.7, 22.0}); err != nil {
    return err
}
// Flush starts a new segment. Call it again as more samples arrive.
if err := w.Flush(); err != nil {
    return err
}
return ch.Write([]float64{22.4, 22.8})
```

`Create` and `NewWriter` buffer one segment. Repeated `Write` calls before `Flush` belong to the same chunk. `Close` flushes and, for `Create`, closes the file. An empty `Close` still writes a valid root segment.

`bytes.Buffer` and other non-seekable writers buffer the finished segment and write it once. A seekable writer (`*os.File`) publishes the NI incomplete-segment sentinel (`0xFFFFFFFFFFFFFFFF`) in the next-segment offset, then patches the real length after the raw bytes are on disk. A crash in between leaves a last segment the reader can consume up to EOF.

`SetInterleaved(true)` multiplexes fixed-size channels sample by sample. Every channel in that chunk must have the same number of values. See the matrix below for strings.

## Read

```go
f, err := tdms.Open("run.tdms")
if err != nil {
    return err
}
defer f.Close()

g, ok := f.Group("acquisition")
if !ok {
    return fmt.Errorf("group missing")
}
ch, ok := g.Channel("temperature")
if !ok {
    return fmt.Errorf("channel missing")
}
values, err := ch.Float64s()
if err != nil {
    return err
}
_ = values

// Stream without holding the whole channel.
cur := ch.Cursor(4096)
for cur.Next() {
    batch := cur.Values().([]float64)
    _ = batch
}
return cur.Err()
```

`Open` and `NewReader` parse every segment's metadata and do not load raw samples. `NewReader` accepts an `io.ReaderAt` that can report its length (`*os.File`, `*bytes.Reader`, `*io.SectionReader`, or any `io.Seeker`). Value reads use `ReadAt`, so concurrent reads are safe when the underlying reader is.

`Values` returns a freshly allocated slice in the channel's native Go type (`[]int32`, `[]float64`, `[]string`, `[]Timestamp`, `[]bool`, …). `Float64s` converts numeric channels. `Times` converts timestamp channels to UTC `time.Time` at nanosecond resolution. `Timestamps` keeps the full 2^-64 fraction.

Waveform properties are ordinary properties plus helpers: `Unit` (`unit_string`, then `NI_UnitDescription`), `Increment` (`wf_increment`), `StartTime` (`wf_start_time`), `SampleCount` (`wf_samples`). `LinearScale` reads the first `NI_Scale[0]`…`NI_Scale[8]` linear scale. NI's own example uses `NI_Scale[1]_…`. Nothing is applied automatically; call `Apply` on raw samples.

Runnable copies of the snippets live in `examples/write`, `examples/read`, and `Example_roundTrip` (checked by `go test`).

## API

| Piece | Role |
| --- | --- |
| `Open`, `NewReader`, `File` | Read a file. `Groups`, `Group`, `Properties` at file level. `Version`, `SegmentCount`. |
| `Group`, `Channel` | Names, paths, properties, `Values`, `Cursor`, typed helpers. |
| `Create`, `NewWriter`, `Writer` | `SetProperty`, `Group`, `SetInterleaved`, `Flush`, `Close`. |
| `GroupWriter`, `ChannelWriter` | `SetProperty`, `Channel`, `Write`. |
| `DataType` | NI `tdsDataType` codes. `String`, `Size`. |
| `Property`, `Timestamp`, `Extended`, `LinearScale` | Values and conversions. |
| `Error` and `Err*` | Wrapped errors. Use `errors.Is`. |

Channel order inside a segment is creation order on the writer, which is the order objects first appear on the reader. `Write` does not reorder channels.

A channel that has never had a raw-data index has no type on disk (`DataType` is void, `Len` is 0). The type is stored only in a raw-data index.

## Support

| Feature | Read | Write |
| --- | --- | --- |
| Version 4713 (TDMS 2.0) | yes | yes |
| Version 4712 (TDMS 1.0) | yes, same layout | no |
| Little-endian segments | yes | yes |
| Big-endian segments | yes | no |
| Interleaved fixed-size channels, including mixed widths | yes | yes, equal lengths |
| Contiguous channels, including unequal lengths | yes | yes |
| Several chunks in one segment | yes | one chunk per `Flush` |
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
| Extended float (`0x0B`) | 10-byte x86 80-bit; `Float64` is best-effort | yes, as `[]Extended` |
| Complex float `0x08000c`, `0x10000d` | yes | yes |
| DAQmx raw data (ToC bit `0x80`, index `0x1269`, `0x1369`, or `0x126A`) | error `ErrDAQmx` | not written |
| Fixed-point `0x4F` | error `ErrUnsupported` | not written |
| `*.tdms_index` (`TDSh`) | error `ErrIndexFile` | not written |
| Incomplete last segment (`next` offset all `0xFF`) | yes, whole values that are present | sentinel used only until `Flush` patches it |
| `name` property renaming an object (TDMS 2.0) | kept as a normal property; the path does not change | same |

Integer and float type codes are the usual 1–10 (`int8` through `float64`). The NI article's lead-in example labels bytes `0E 00 00 00` as mask `0x1110`. That label is the binary value `0b1110`; the bytes are the mask `0x0E` (metadata, new object list, and raw data). ToC itself is always little-endian. When `TocBigEndian` is set, every other numeric field in the segment is big-endian, including the version and both offsets in the lead-in.

## Format choices

A few details in the article are ambiguous. Each choice is tested.

**String channels.** The article says the raw block starts with "the offset of the first character of each string". Files written by LabVIEW, and the readers that open them (npTDMS, the Rust `tdms` crate), store the cumulative *end* offset of each string instead, with an implicit start of zero. Empty strings repeat the previous end offset. The total-size field in the index is `4*N +` the UTF-8 byte count. This package reads and writes that end-offset table. Property strings are different: a `uint32` byte length followed by the bytes, which is what the article specifies for every string that is not channel raw data.

**DAQmx.** The article's format-changing scaler index is the bytes `69 12 00 00`, little-endian `0x00001269` (decimal 4713, the same number as the version, in a different field). The digital-line scaler is documented as `69 13 00 00` (`0x00001369`). npTDMS reports that NI-produced files use `6A 12 00 00` (`0x0000126A`) instead, and that `0x1369` does not appear. There is no `0x12C9` (decimal 4809) in the article. All three markers, and the `kTocDAQmxRawData` bit, return `ErrDAQmx`. Format-changing and digital scalers are not decoded.

**Extended float.** The article does not give a width. Values are 10 bytes, the x86 80-bit layout LabVIEW uses on Windows: significand `uint64` (explicit integer bit), then a biased exponent in the low 15 bits of a `uint16` with the sign in bit 15. Little-endian stores significand then exponent. Big-endian stores exponent then significand, each field big-endian. `Extended.Float64` rounds into float64 and does not preserve a 64-bit significand or a NaN payload. Round-trip `[]Extended` when the bits matter. Subnormals are not synthesized by `ExtendedFromFloat64`.

**Float with unit.** The article says a LabVIEW float-with-unit becomes a float channel plus a `unit_string` property. There is no unit id in the raw bytes. `TypeFloat32WithUnit`, `TypeFloat64WithUnit`, and `TypeExtendedFloatWithUnit` use the same payload as the plain types. The `DataType` value is preserved so a caller can tell them apart.

**Timestamps.** Seconds are `int64` from 1904-01-01 00:00:00 UTC, Gregorian, leap seconds ignored. The fraction is an unsigned `2^-64` second and is never negative. One half-second before the epoch is `Seconds == -1` and `Fraction == 2^63`. Little-endian stores fraction then seconds. Big-endian stores seconds then fraction (the order npTDMS accepts; the article does not spell it out). `time.Time` conversions round to a nanosecond and are checked to round-trip that nanosecond. Instants that do not fit in a Unix `time.Time` (a few seconds at the `int64` edges) make `Time` report false and `Times` return an error. `Timestamps` still returns the raw fields.

**Index length.** The `uint32` in front of a standard raw-data index includes itself. A fixed-size index is 20 bytes (length, type, dimension, count). A string index is 28 bytes (those fields plus total size). Dimension must be 1. `0xFFFFFFFF` means no raw data. `0x00000000` means the index matches the previous segment.

**Metadata reuse.** A later segment that repeats the same channel order may omit unchanged objects. A segment whose ToC has no metadata reuses the previous segment's indexes entirely; that is how an unchanged layout is appended. Adding a channel at the end does not set `kTocNewObjList`. Removing or reordering channels does, and the segment lists every channel that still has data. A segment with no raw bytes does not change channel order; it only overlays properties. The raw-data section of a segment with raw bytes always contains every channel in the current order, including channels whose metadata was omitted.

**Groups.** The writer emits a group object for every group, and a file object, on the first segment (and again when their properties change). The reader also creates a group implied by a channel path when the file never stored the group object. The NI worked example is like that, and `TestGoldenNIFirstSegment` decodes it.

**Strings and NUL.** The length is authoritative. A trailing NUL that was counted in the length is preserved. The writer does not add one.

## Errors

Public operations return `*Error` with an operation name and, when there is one, an object or file path. `Unwrap` yields a sentinel (`ErrBadTag`, `ErrTruncated`, `ErrCorrupt`, `ErrDAQmx`, `ErrUnsupported`, `ErrType`, `ErrLayout`, `ErrVersion`, `ErrIndexFile`, `ErrClosed`, `ErrArgument`) or the underlying I/O error. Malformed, truncated, and unknown input returns an error. The reader does not panic on it. `FuzzReader` is the regression target.

## Performance

Metadata parsing reads the lead-in and the metadata bytes of each segment. Raw samples stay on the `io.ReaderAt` until `Values`, a typed helper, or `Cursor`. A cursor yields at most `batch` values per `Next` (default 4096) and does not retain earlier batches.

The writer holds the current segment's samples until `Flush`. Flush often for large channels. Non-seekable writers also hold the encoded segment for the duration of the flush. Seekable writers write the bytes once and patch eight bytes of the lead-in.

`BenchmarkWriteFloat64`, `BenchmarkReadFloat64`, and `BenchmarkStreamFloat64` move 100,000 `float64` values. `go test -bench=. -benchmem` reports throughput on the machine running them.

## Layout of the package

`format.go` is the lead-in. `datatype.go` is the type enum. `value.go` encodes properties and channel blocks. `reader.go` is the segment walk and `ReadAt` fetches. `writer.go` is segment assembly. Tests in `golden_test.go` build files with `encoding/binary` and the hex from the NI article, without calling `Writer`, so a bug shared by the writer and the reader cannot satisfy them.

## License

MIT. See [LICENSE](LICENSE).
