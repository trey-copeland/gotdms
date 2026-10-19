# gotdms

[![Build and test Go module](https://github.com/trey-copeland/gotdms/actions/workflows/build-and-test-go-module.yml/badge.svg)](https://github.com/trey-copeland/gotdms/actions/workflows/build-and-test-go-module.yml)

A pure Go reader and writer for NI TDMS 2.0 files. No cgo. The Go package name is `tdms`; the module path is `github.com/trey-copeland/gotdms`.

TDMS (Technical Data Management Streaming) is the binary format LabVIEW and other National Instruments tools use for measurement data. A file is a sequence of segments. Each segment has a 28-byte lead-in, optional metadata (object paths, raw-data indexes, properties), and optional raw samples. Objects form a tree of one file (`/`), any number of groups (`/'Group'`), and channels (`/'Group'/'Channel'`). Only channels hold raw data.

The byte layout follows [TDMS File Format Internal Structure](https://www.ni.com/en/support/documentation/supplemental/07/tdms-file-format-internal-structure.html) (NI, updated Nov 12, 2024). Constants in `format.go`, `datatype.go`, and `value.go` cite that article where the on-disk value comes from it.

## Limitations

**DAQmx raw data is rejected, not decoded.** A segment whose table-of-contents has `kTocDAQmxRawData` (bit `0x80`), or whose raw-data index is `0x1269`, `0x1369`, or `0x126A`, returns `ErrDAQmx`. Format-changing scalers and digital scalers are not applied. A large share of NI logging uses DAQmx scaling, and those files do not open with this package.

**Extended float (`tdsTypeExtendedFloat`, `0x0B`) is spec-derived and unverified against NI.** The article does not give a width. This package uses 10 bytes, the x86 80-bit layout documented by other readers. No LabVIEW file in this repository checks that layout, and npTDMS does not write the type. The width is not changed on speculation.

Other limits are in the support matrix below: big-endian segments are read and not written, fixed-point returns `ErrUnsupported`, and an NI `*.tdms_index` (`TDSh`) file returns `ErrIndexFile`.

## Install

```bash
go get github.com/trey-copeland/gotdms
```

```go
import "github.com/trey-copeland/gotdms"
```

The `gotdms` command reads a file and writes CSV. From a checkout:

```bash
go run ./cmd/gotdms info run.tdms
go run ./cmd/gotdms info --json run.tdms
go run ./cmd/gotdms convert -o run.csv run.tdms
go run ./cmd/gotdms convert --group acquisition --channel temperature -o temp.csv run.tdms
```

`info` prints the file, each group, and each channel with its data type, sample count, and properties at every level. `--json` prints that tree as JSON. The status object has `complete`, `incomplete`, `truncated`, `partial_tail`, and `unused_tail_bytes`. The command calls `Open` without `WithLenientTail`, so a next-segment offset past EOF is still an error and prints no JSON. `convert` writes one CSV column per channel and a header row. The header cell is the channel's TDMS path (`/'group'/'channel'`). With no `--group` and no `--channel`, every channel is a column. `--group` alone writes that group. `--channel` selects one channel and needs `--group` when the name is used in more than one group. Timestamp values are UTC RFC3339Nano because the data type is `tdsTypeTimeStamp`. A channel named `Time` that stores floats is written as numbers. A present empty string is an empty field. A row past the end of a shorter channel is the cell `#N/A`. A string whose value is the characters `#N/A` is written the same way; CSV has no separate missing-value type.

From a checkout of this repository:

```bash
go test ./...
go test -bench=. ./...
```

`go test -fuzz=FuzzReader` and `go test -fuzz=FuzzSegmentIndex` compile the fuzz targets. They are not part of `go test` by default. `testdata/nptdms` is the differential harness against npTDMS. That package is LGPL and runs only in CI; it is not a Go dependency. There is no LabVIEW here, so npTDMS is the external anchor. See `testdata/nptdms/README.md`.

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

`bytes.Buffer` and other non-seekable writers write the lead-in with the finished length, then stream the raw bytes. A seekable writer (`*os.File`) publishes the NI incomplete-segment sentinel (`0xFFFFFFFFFFFFFFFF`) in the next-segment offset, then patches the real length after the raw bytes are on disk. A crash in between leaves a last segment the reader can consume up to EOF. Output goes through a 1 MiB buffer (`WithWriteBuffer`). `Write` copies the caller's slice into the channel buffer, so refilling that slice and writing again does not change bytes already buffered. `WriteNoCopy` skips that copy for a fixed-size numeric or complex slice on a little-endian host, and for a one-byte slice on any host. The caller must not mutate the retained slice until `Flush` or `Close` returns. Bool, timestamp, extended, and string values are copied either way. A big-endian host copies multi-byte numeric slices too, because the file is stored little-endian. Flush streams the channel buffer and does not allocate a second copy of a contiguous segment.

`Stream` is the path that does not hold a large channel until `Flush`. The caller declares how many values each channel will receive. The writer emits the lead-in and the raw-data index immediately, then each `Stream.Write` copies that chunk to the underlying writer. Resident memory is the write buffer plus the chunk in hand, not the total number of samples. `Finish` (or `Close`, when every declared value has been written) patches a seekable lead-in. String channels are not streamed: their offset table needs every string length, so they stay on `Write`. A stream that stops short is not closed out as a finished segment. On a seekable file the lead-in stays the incomplete-segment sentinel and the reader keeps the whole values that were written. On a non-seekable writer the lead-in already names the full length, so the short file returns `ErrTruncated`.

```go
ch, err := w.Group("acquisition").Channel("temperature", tdms.TypeFloat64)
s, err := ch.Stream(1_000_000) // total values, not the chunk size
for more {
    err = s.Write(ch, chunk) // chunk may be reused after Write returns
}
err = s.Finish()
```

Contiguous channels in one stream are written in creation order, each count finished before the next channel starts. Interleaved channels must share one count; `WriteInterleaved` takes one equal-length slice per channel and mixes that chunk immediately. `SetProperty` during an open stream returns an error, because the segment metadata is already written. `WriteWaveform` still buffers Y. `StreamWaveform` sets the waveform properties and the declared count, then Y is written with `Stream.Write`.

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

`Open` and `NewReader` parse every segment's metadata and do not load raw samples. `WithSegmentIndex` can reuse a sidecar of that layout on a later open; see [Segment index](#segment-index). `NewReader` accepts an `io.ReaderAt` that can report its length (`*os.File`, `*bytes.Reader`, `*io.SectionReader`, or any `io.Seeker`). `Values` and the typed helpers read through `ReadAt`, so concurrent reads are safe when the underlying reader is. `Cursor` scans sequentially through a 256 KiB buffer (`WithReadBuffer`).

`Values` returns a freshly allocated slice in the channel's native Go type (`[]int32`, `[]float64`, `[]string`, `[]Timestamp`, `[]bool`, …). `Float64s` converts numeric channels. `Times` converts timestamp channels to UTC `time.Time` at nanosecond resolution. `Timestamps` keeps the full 2^-64 fraction.

Waveform properties are ordinary properties plus helpers: `Unit` (`unit_string`, then `NI_UnitDescription`), `Increment` (`wf_increment`), `StartTime` (`wf_start_time`), `SampleCount` (`wf_samples`). `LinearScale` reads the first `NI_Scale[0]`…`NI_Scale[8]` linear scale. NI's own example uses `NI_Scale[1]_…`. Nothing is applied automatically; call `Apply`, `ApplyScale`, or `ScaledWaveform` when engineering units are wanted. See [Waveforms](#waveforms).

Runnable copies of the snippets live in `examples/write`, `examples/read`, `Example_roundTrip`, and `ExampleReadWaveform` (checked by `go test`).

## Waveforms

LabVIEW does not store an analog waveform as its own TDMS type. A waveform is a channel's Y samples plus `wf_start_time`, `wf_increment` (float64 seconds), `wf_samples`, and `unit_string`. `Waveform[T]` is that bundle. `T` is `float64`, `float32`, or an integer width (`int8` through `uint64`). Bool, string, timestamp, extended, and complex channels are not waveforms.

`Start` is a `Timestamp`, not a `time.Time`. That keeps the full 2^-64 second fraction across a round trip. Call `Start.Time` for UTC at nanosecond resolution. `Increment` is the stored `wf_increment`. `Dt` rounds it to the nearest `time.Duration` and is only a convenience.

A missing `wf_start_time` or `wf_increment` is not an error. The field is the zero value and `HasStart` or `HasIncrement` is false. A stored zero is a real value: `HasStart` true with a zero `Timestamp` is the TDMS epoch (1904-01-01 UTC), and `HasIncrement` true with increment 0 means every sample is at t0. `TimeAt(i)` computes t0 + i*dt in one step, not by adding dt over and over. `Times` returns one `time.Time` per sample.

`ReadWaveform[T]` and `Channel.Waveform` (float64 only) load every sample, same as `Values`. They do not apply a linear scale. `ApplyScale` and `ScaledWaveform` do, and they return `Waveform[float64]`. `WriteWaveform` writes Y unchanged — it does not invert a scale — and sets `wf_start_time`, `wf_increment`, `wf_samples` (`len(Y)`), and, when the unit is non-empty, `unit_string`, in that order. An empty unit does not clear a `unit_string` already set on the channel.

`ReadWaveform` is the wrong call for a multi-gigabyte channel. Take `WaveformHeader` (no samples) and walk the channel with `Cursor`. `WaveformHeader.TimeAt` takes the channel's absolute sample index, so a chunk that starts at sample 4096 is `header.TimeAt(4096 + i)`.

```go
start := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
err = ch.WriteWaveform(tdms.Waveform[float64]{
    Start:     tdms.NewTimestamp(start),
    Increment: 0.001,
    Y:         []float64{0.1, 0.2, 0.3},
    Unit:      "V",
})
// ...
wf, err := tdms.ReadWaveform[float64](c)
t1, _ := wf.TimeAt(1) // 2024-06-01T12:00:00.001Z
```

`ExampleReadWaveform` is the compile-checked copy.

## API

| Piece | Role |
| --- | --- |
| `Open`, `NewReader`, `File` | Read a file. `Groups`, `Group`, `Properties` at file level. `Version`, `SegmentCount`. |
| `Group`, `Channel` | Names, paths, properties, `Values`, `Slice`, `Cursor`, `CursorAt`, typed helpers, `Waveform`, `WaveformHeader`. |
| `ReadValues`, `Group.Values` | Read several channels at once. A shared interleaved segment is split in one pass. |
| `Int64s`, `Float64s`, `Cursor.Float64sCopy` | Numeric conversions. `Float64sCopy` is an independent copy of the current cursor batch. |
| `Create`, `NewWriter`, `Writer` | `SetProperty`, `Group`, `SetInterleaved`, `Flush`, `Close`, `Stream`. `WithWriteBuffer` (default 1 MiB). |
| `GroupWriter`, `ChannelWriter` | `SetProperty`, `Channel`, `Write`, `WriteNoCopy`, `WriteWaveform`, `Stream`, `StreamWaveform`. |
| `Stream` | Declared-count segment. `Write`, `WriteInterleaved`, `Finish`. |
| `Waveform[T]`, `ReadWaveform`, `ScaledWaveform` | Analog waveform. `ApplyScale` applies a `LinearScale` explicitly. |
| `WaveformHeader` | t0, dt, unit, and `wf_samples` without loading Y. `TimeAt` timestamps cursor batches. |
| `DataType` | NI `tdsDataType` codes. `String`, `Size`. |
| `Property`, `Timestamp`, `Extended`, `LinearScale` | Values and conversions. |
| `WithReadBuffer` | Sequential buffer for `Cursor` (default 256 KiB). `Values` stays on `ReadAt`. |
| `Channel.ChunkStats` | Count, min, max, mean, and RMS per chunk of a numeric channel, one Cursor pass. |
| `File.Status` | Clean end, an incomplete final segment, or (`WithLenientTail`) a truncated final segment, plus any short tail. |
| `WithLenientTail` | Open a file whose last finite next-segment offset runs past EOF. Whole tail values are readable and are not committed. |
| `Follow`, `WithTailInterval`, `WithTailBatch` | Poll a file that is still being written. Skip an incomplete sentinel and a past-EOF tail. |
| `WithSegmentIndex`, `SegmentIndexPath` | Sidecar of the segment layout. Validated by size, modification time, identity, and a metadata fingerprint. |
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
| DAQmx raw data (ToC bit `0x80`, index `0x1269`, `0x1369`, or `0x126A`) | error `ErrDAQmx` | not written |
| Fixed-point `0x4F` | error `ErrUnsupported` | not written |
| `*.tdms_index` (`TDSh`) | error `ErrIndexFile` | not written |
| Incomplete last segment (`next` offset all `0xFF`) | yes, whole values that are present; `File.Status` reports it | sentinel used only until `Flush` patches it |
| Final segment whose finite next offset runs past EOF | `ErrTruncated` by default. `WithLenientTail` keeps earlier segments and whole tail values and sets `File.Status.Truncated` | n/a |
| `Follow` of a file still being written | new samples from finished segments only | n/a |
| Chunk min/max/mean/RMS | numeric channels, one pass, caller-chosen chunk | n/a |
| Segment-index sidecar (`.gtdx`) | opt-in `WithSegmentIndex`; ignored when stale or corrupt | not written by the TDMS writer |
| `name` property renaming an object (TDMS 2.0) | kept as a normal property; the path does not change | same |

Integer and float type codes are the usual 1–10 (`int8` through `float64`). The NI article's lead-in example labels bytes `0E 00 00 00` as mask `0x1110`. That label is the binary value `0b1110`; the bytes are the mask `0x0E` (metadata, new object list, and raw data). ToC itself is always little-endian. When `TocBigEndian` is set, every other numeric field in the segment is big-endian, including the version and both offsets in the lead-in.

## Format choices

A few details in the article are ambiguous. Each choice is tested.

**String channels.** The article says the raw block starts with "the offset of the first character of each string". Files written by LabVIEW, and the readers that open them (npTDMS, the Rust `tdms` crate), store the cumulative *end* offset of each string instead, with an implicit start of zero. Empty strings repeat the previous end offset. The total-size field in the index is `4*N +` the UTF-8 byte count. This package reads and writes that end-offset table. Property strings are different: a `uint32` byte length followed by the bytes, which is what the article specifies for every string that is not channel raw data.

**DAQmx.** The article's format-changing scaler index is the bytes `69 12 00 00`, little-endian `0x00001269` (decimal 4713, the same number as the version, in a different field). The digital-line scaler is documented as `69 13 00 00` (`0x00001369`). npTDMS reports that NI-produced files use `6A 12 00 00` (`0x0000126A`) instead, and that `0x1369` does not appear. There is no `0x12C9` (decimal 4809) in the article. All three markers, and the `kTocDAQmxRawData` bit, return `ErrDAQmx`. Format-changing and digital scalers are not decoded.

**Extended float.** The article does not give a width. Values are 10 bytes, the x86 80-bit layout other readers document: significand `uint64` (explicit integer bit), then a biased exponent in the low 15 bits of a `uint16` with the sign in bit 15. Little-endian stores significand then exponent. Big-endian stores exponent then significand, each field big-endian. This layout is derived from that description. It has not been checked against a file written by LabVIEW or another NI tool, and npTDMS does not produce the type, so this repository has no NI fixture for it. `Extended.Float64` rounds into float64 and does not preserve a 64-bit significand or a NaN payload. Round-trip `[]Extended` when the bits matter. Subnormals are not synthesized by `ExtendedFromFloat64`.

**Float with unit.** The article says a LabVIEW float-with-unit becomes a float channel plus a `unit_string` property. There is no unit id in the raw bytes. `TypeFloat32WithUnit`, `TypeFloat64WithUnit`, and `TypeExtendedFloatWithUnit` use the same payload as the plain types. The `DataType` value is preserved so a caller can tell them apart.

**Timestamps.** Seconds are `int64` from 1904-01-01 00:00:00 UTC, Gregorian, leap seconds ignored. The fraction is an unsigned `2^-64` second and is never negative. One half-second before the epoch is `Seconds == -1` and `Fraction == 2^63`. Little-endian stores fraction then seconds. Big-endian stores seconds then fraction (the order npTDMS accepts; the article does not spell it out). `time.Time` conversions round to a nanosecond and are checked to round-trip that nanosecond. Instants that do not fit in a Unix `time.Time` (a few seconds at the `int64` edges) make `Time` report false and `Times` return an error. `Timestamps` still returns the raw fields.

**Index length.** The `uint32` in front of a standard raw-data index includes itself. A fixed-size index is 20 bytes (length, type, dimension, count). A string index is 28 bytes (those fields plus total size). Dimension must be 1. `0xFFFFFFFF` means no raw data. `0x00000000` means the index matches the previous segment.

**Metadata reuse.** A later segment that repeats the same channel order may omit unchanged objects. A segment whose ToC has no metadata reuses the previous segment's indexes entirely; that is how an unchanged layout is appended. Adding a channel at the end does not set `kTocNewObjList`. Removing or reordering channels does, and the segment lists every channel that still has data. A metadata-only segment still updates that order: a full raw-data index is stored even when the segment has no raw bytes, `0x00000000` resumes the stored index, and `0xFFFFFFFF` drops the channel from the raw order without erasing the index. A following raw-only segment uses the order those declarations left behind. The raw-data section of a segment with raw bytes always contains every channel in the current order, including channels whose metadata was omitted. A raw section whose length is not a multiple of that chunk, and that is not an incomplete or lenient tail, is `ErrCorrupt`.

**Groups.** The writer emits a group object for every group, and a file object, on the first segment (and again when their properties change). The reader also creates a group implied by a channel path when the file never stored the group object. The NI worked example is like that, and `TestGoldenNIFirstSegment` decodes it.

**Strings and NUL.** The NI article says a length-prefixed string may be null-terminated, and that the null is ignored on read because the length is stored. This package drops one trailing `0` byte from property strings, object paths, property names, and raw channel strings. A `0` that is not the last byte is kept. The writer does not add a NUL. A value that ends in NUL does not round-trip that byte. npTDMS does not strip it, so the differential files in `testdata/nptdms` do not use a trailing NUL.

## Errors

Public operations return `*Error` with an operation name and, when there is one, an object or file path. `Unwrap` yields a sentinel (`ErrBadTag`, `ErrTruncated`, `ErrCorrupt`, `ErrDAQmx`, `ErrUnsupported`, `ErrType`, `ErrLayout`, `ErrVersion`, `ErrIndexFile`, `ErrClosed`, `ErrArgument`) or the underlying I/O error. Malformed, truncated, and unknown input returns an error. The reader does not panic on it. `FuzzReader` is the regression target.

## Performance

Metadata parsing reads the lead-in and the metadata bytes of each segment. Raw samples stay on the `io.ReaderAt` until `Values`, a typed helper, or `Cursor`.

`Values` issues `ReadAt` for the bytes it needs. A contiguous numeric channel on a little-endian machine is copied straight into the result slice: there is no intermediate `[]byte` and no per-sample `binary.Read`. Big-endian segments are byte-swapped in place after that read. A big-endian host uses the scalar decoder instead of the copy; `decodePackedScalar` is checked against the bulk path in tests. Bools, timestamps, and 80-bit extended values stay on the scalar path everywhere, because their Go layout is not the on-disk layout.

`Cursor` does not use that one-shot `ReadAt`. It reads ahead through a pooled buffer, 256 KiB by default (`WithReadBuffer`), and reuses the batch slice (`Values` is valid only until the next `Next`). A read at least as large as the buffer skips the cache and goes to `ReadAt`. This package does not mmap. A mapping delivers `SIGBUS` if the file is truncated, and it is a poorer default on 32-bit and non-Unix targets.

Interleaved fixed-size channels are split out in about 1 MiB windows rather than by allocating the whole multiplexed span once per channel. String channels and mixed-width layouts use the same windowed copy; the string offset table is unchanged.

`Write` copies into the channel buffer. On a little-endian machine the numeric encode is a bulk copy of the caller's bytes. `WriteNoCopy` is the opt-in that keeps the caller's backing array until the next write or flush. `Flush` computes the raw size (including a string offset table) and streams metadata, then raw bytes, through a 1 MiB `bufio.Writer` (`WithWriteBuffer`). A contiguous flush does not allocate a second copy of the segment. Interleaved output is mixed in a window of about 1 MiB. Seekable writers still publish the incomplete-segment sentinel and patch eight bytes of the lead-in after the buffer is flushed. The channel buffer itself is one copy of the encoded samples until `Flush`; call `Flush` before a chunk outgrows memory. String payload is streamed at flush. Per-string headers are still allocated on read; a CPU profile of numeric multi-hundred-megabyte files did not show that path, so it was left alone.

There is no mmap switch. The defaults above are the fast path; the options only change buffer size.

`BenchmarkChunkStatsFloat64` walks 1,048,576 `float64` samples (8 MiB) in chunks of 4096. On this cloud VM (Linux amd64, 4 vCPU, Intel Xeon, Go 1.22, synthetic data, median of ten `-benchtime=100ms -benchmem` runs) it was 3224 MB/s, 32984 B/op, and 4 allocs/op. That is one 32 KiB chunk plus a few small headers, not a copy of the channel.

`BenchmarkWriteFloat64`, `BenchmarkReadFloat64`, and `BenchmarkStreamFloat64` move 100,000 `float64` values. `BenchmarkLargeReadFloat64`, `BenchmarkLargeReadFloat64File`, `BenchmarkLargeCursorFile`, `BenchmarkLargeInterleaved`, `BenchmarkLargeWriteFile`, and `BenchmarkLargeWriteMem` move 128 MiB (four float64 channels for the interleaved case). `BenchmarkLargeStreamFile` writes that 128 MiB through `Stream` in 64 KiB chunks. `BenchmarkLargeWriteDiscard` and `BenchmarkLargeStreamDiscard` allocate the samples inside the timed loop and discard the bytes, so `B/op` is the writer's footprint. They run only under `-bench`.

On this VM (Linux amd64, 4 vCPU, Intel Xeon, Go 1.22, `-count=10 -benchtime=100ms`, median), `BenchmarkLargeWriteFile` was 1403 MB/s, 129.00 MiB/op, and 47 allocs/op. The same benchmark before `Write` copied (it aliased the caller's slice on this little-endian host) was 2989 MB/s and 1.00 MiB/op. The copy adds 128 MiB and one allocation, and the median time for one 128 MiB file write went from 44.9 ms to 95.7 ms. `BenchmarkLargeWriteMem` (`bytes.Buffer`) was 1545 MB/s, 258.01 MiB/op, and 43 allocs/op: the encoded copy plus the destination buffer. Before the copy it was 3762 MB/s and 130.01 MiB/op. `BenchmarkLargeStreamFile` does not use that path: 2770 MB/s, 1.05 MiB/op, 2102 allocs/op, one small interface header per 64 KiB chunk. `BenchmarkLargeWriteDiscard` was 4625 MB/s and 257.00 MiB/op (the 128 MiB slice allocated inside the loop, plus the copy). `BenchmarkLargeStreamDiscard` was 1.11 MiB/op.

```bash
go test -bench=BenchmarkLarge -benchmem -count=10 -benchtime=100ms
```

The After column is the median of those ten runs. benchstat is not vendored, so the median is taken by hand from the ten `-benchmem` lines. "Before" is the same benchmark shape on the library as first imported, measured on this VM before the buffered reader, bulk copy, and streaming writer. That column was not re-run for the copy change. The numbers are indicative, not a hardware claim.

| Benchmark | Before | After |
| --- | --- | --- |
| Contiguous float64 `Values`, in memory | 2092 MB/s, 256 MiB/op, 30 allocs/op | 3953 MB/s, 128.00 MiB/op, 30 allocs/op |
| Contiguous float64 `Values`, `os.File` | 1833 MB/s, 256 MiB/op, 34 allocs/op | 3892 MB/s, 128.00 MiB/op, 33 allocs/op |
| `Cursor` over `os.File`, batch 4096 | 1927 MB/s, 256 MiB/op, 12319 allocs/op | 13281 MB/s, 33.9 KiB/op, 33 allocs/op |
| 4 interleaved float64 channels, in memory | 348 MB/s, 768 MiB/op, 65 allocs/op | 1781 MB/s, 128.01 MiB/op, 571 allocs/op |
| Write one float64 channel, `os.File` | 985 MB/s, 512 MiB/op, 44 allocs/op | 1403 MB/s, 129.00 MiB/op, 47 allocs/op |
| Write one float64 channel, `bytes.Buffer` | 804 MB/s, 640 MiB/op, 41 allocs/op | 1545 MB/s, 258.01 MiB/op, 43 allocs/op |

`b.SetBytes` is the raw sample payload (128 MiB, or 128 MiB across the four interleaved channels). The cursor's allocation is the reused batch buffer: one scan does not allocate a fresh slice per batch. The file write's 129 MiB is the 128 MiB copy plus the 1 MiB write buffer. The in-memory write allocates that copy plus the destination `bytes.Buffer`.

## Layout of the package

`format.go` is the lead-in. `datatype.go` is the type enum. `value.go` encodes properties and channel blocks. `reader.go` is the segment walk and `ReadAt` fetches. `writer.go` is segment assembly. Tests in `golden_test.go` build files with `encoding/binary` and the hex from the NI article, without calling `Writer`, so a bug shared by the writer and the reader cannot satisfy them.

## Chunk statistics

`Channel.ChunkStats(samplesPerChunk)` walks a numeric channel once and returns count, min, max, mean, and RMS for each chunk. The last chunk is shorter when the length is not a multiple of the size. The walk uses `Cursor` and does not retain earlier samples. Mean is the arithmetic mean. RMS is the square root of the mean of the squared samples. Values are accumulated as float64, with the same integer and extended-float conversion as `Float64s`. A NaN sample makes min, max, mean, and RMS NaN.

String, boolean, timestamp, and complex channels return `ErrType`. The check is the channel's `DataType`. A boolean is not coerced to 0 and 1 here, and a channel is not numeric because of its name.

## File status and follow

`File.Status` reports whether the file ended on a finished segment, on the NI incomplete-segment sentinel (`0xFFFFFFFFFFFFFFFF` in the next-segment offset), or on a truncated tail recovered by `WithLenientTail`. `PartialTail` is set when that segment's raw bytes do not end on a chunk boundary. `UnusedTail` is the number of leftover raw bytes that were not a whole value. `Open` of a well-formed file is unchanged. `Open` of an incomplete file is also unchanged: `Values` still returns every whole value in the tail. A next-segment offset that runs past EOF returns `ErrTruncated` unless `WithLenientTail` is set. With that option the earlier segments open, whole values in the partial tail are readable, and `Status` reports `Truncated`. Those tail samples are not committed. A trailing fragment shorter than a lead-in, or a final segment whose metadata itself runs past EOF, is still `ErrTruncated` with or without the option. A file `Open` rejects has no `FileStatus`.

`Follow` is for a file another process is still writing. It polls at `WithTailInterval` (default 100 ms) and returns batches of at most `WithTailBatch` samples (default 4096). It opens with `WithLenientTail`. Samples come from segments whose next-segment offset is a real length and whose bytes are fully present. A segment that still carries the sentinel is skipped, including whole values already flushed into it. A final segment whose finite length runs past EOF is also skipped until a later poll sees those bytes. A missing file, or a read that still returns `ErrTruncated` (a short trailing lead-in, or metadata past EOF), is treated as "not finished yet" and polled again. Any other error stops the tail. Cancel the context to stop; `Err` is that context error. `Close` unblocks `Next` and leaves `Err` nil. The channel is selected by group name and channel name. A channel named `Time` is returned in its stored type.

## Segment index

`WithSegmentIndex(sidecar)` stores the parsed segment layout beside the data file. `SegmentIndexPath(path)` is `path + ".gtdx"`. The file is not an NI `*.tdms_index` (`TDSh`) companion. Raw samples are not stored. A cache hit still reads every lead-in and the metadata bytes that follow it, in order, and recomputes the IEEE CRC-32 fingerprint. It does not decode object paths, raw-data indexes, or properties out of those bytes; the object tree and the spans come from the sidecar. It still reads sample bytes from the TDMS file, so an in-place edit of raw samples is visible. A span that addresses bytes outside the file, or that claims more samples than the file can hold, is a decode error and therefore a cache miss.

The sidecar is little-endian, version 1, magic `GTDX` (72-byte header, then the object tree). The header holds the data file's size, modification time in Unix nanoseconds, device id and inode (0 when the platform does not report them), an IEEE CRC-32 of every lead-in followed by that segment's metadata, and an IEEE CRC-32 of the payload. Flags record an incomplete final segment and a partial tail. The payload is file properties, then groups, channels, and the spans `Values` uses to address samples. Properties use the ordinary TDMS property encoding.

An open uses the sidecar only when every one of those checks matches the file it is about to read. A missing sidecar, a size or time or identity mismatch, a fingerprint mismatch, a bad payload checksum, an unknown version, or a truncated file is ignored. `Open` still returns the parsed file. After a full parse the sidecar is replaced by writing a temporary file in the same directory and renaming it over the sidecar. A failure of that write does not fail the open. Passing the data path itself as the sidecar is ignored, so the TDMS file is not overwritten. `File.SegmentIndexUsed` reports whether this open restored the layout from the sidecar.

`dataset.WithSegmentIndex(true)` turns the same cache on for catalog and for later reads. The default is off.

## Datasets

`github.com/trey-copeland/gotdms/dataset` reads a set of TDMS files as one catalog. You declare which channel carries time. Waveform properties are only a fallback, and an undeclared channel with no waveform timing is untimed on purpose.

`WithTimestampChannelName("Time")` says every group has a timestamp channel of that name. `WithTimestampChannel(group, name)` overrides that for one group. An empty name opts the group out of the global name: that group does not use a timestamp channel, and waveform properties may still apply. A later call for the same group replaces the earlier one. The declared channel must be in the same group and file as the data and have the same sample count. If it is missing, the wrong length, or not a timestamp or numeric type, that data channel is untimed and catalog still succeeds. Find and Search mark the declared channel with `IsTimestamp`. It is not used as its own time source.

Timestamp-typed channels are absolute times. Numeric channels (int or float) are seconds since `WithNumericEpoch`. There is no default epoch. Open fails if a declared timestamp channel is numeric and the epoch was not set, so a value of 0 is not silently treated as the Unix epoch. Pass `time.Unix(0, 0).UTC()` when the numbers are Unix seconds. The numeric epoch is not the plotting basis.

Relative time is `sample_time - basis` in seconds. It is zero only at the basis, positive after, and negative before. `WithBasisTime` sets the dataset basis. If it is omitted, the basis is the earliest timed sample in the catalog, chosen once at `Open`. Cropping a window does not move it, so the first returned sample is not re-zeroed. `WithReadBasis` overrides that basis for one `Read`, `ReadWindow`, or `ReadMany`; `ReadMany` applies the same override to every series and leaves `Dataset.Basis` unchanged. `Series.Basis` is the basis that call used. `RelativeTimes` subtracts it. `RelativeTimesFrom` subtracts any other time and does not modify the series. The basis does not have to be a sample. An untimed series ignores it and returns sample indexes. `Resample` keeps absolute times and copies `Basis`.

A step backwards in a timestamp channel returns `ErrMonotonic`. Equal timestamps stay in file order inside a file; across files they follow the overlap policy. The same channel name in several groups returns `*AmbiguousError` unless `Selector.Group` is set. Files are merged in time order. The window is half-open (`start <= t < end`). `OverlapKeepFirst` is the default. Gaps are reported and are not filled with NaN. `Resample` (`InterpLinear`, `InterpPrevious`) is opt-in and does not extrapolate.

When a group has no timestamp declaration, `StartTime` and `Increment` (`wf_start_time`, `wf_increment`) supply the axis. Sample `i` is that start plus `i` times the increment, rounded to the nearest nanosecond. A waveform window and a declared clock whose catalog step is positive read that sample range with `CursorAt` and `Slice`. Samples before the window are not decoded. A declared clock that does not match the step is decoded in full, so a backwards step is still `ErrMonotonic`. Catalog reads the first two samples of a clock whose first step is positive and derives the span from that step; it does not scan the axis for monotonicity. `ReadMany` opens each file once and decodes a shared timestamp channel once per call. `WithReadWorkers` caps how many files that call opens (default 4).

On this VM, `BenchmarkReadWindowTail` (last 64 samples of a 1<<20 timestamp axis) dropped from a median 32.0 ms and 40.01 MiB/op to 25.4 µs and 8025 B/op. `BenchmarkReadWindowTailWaveform` (the same window on a `wf_*` axis) dropped from 536 µs and 69.4 KiB/op to 13.1 µs and 7065 B/op. Both are medians of ten `-benchtime=100ms` runs. The earlier figures decoded the prefix.

```go
ds, err := dataset.OpenGlob(dir, "*.tdms",
    dataset.WithTimestampChannelName("Time"),
    dataset.WithNumericEpoch(epoch),
)
_ = ds.Find("ai0") // group name not required
trigger := epoch.Add(10500 * time.Millisecond) // not required to be a sample
series, err := ds.ReadMany(
    []dataset.Selector{{Name: "ai0"}, {Name: "ai1"}},
    start, end,
    dataset.WithReadBasis(trigger),
)
for _, s := range series {
    _ = s.RelativeTimes() // seconds since trigger; negative before it
}
```

`Example` in package `dataset` is the compile-checked copy: 100 files, a global timestamp channel, find without a group, one window, and relative times against an explicit trigger. Benchmarks run only under `go test -bench`. `BenchmarkCatalog` indexes 100 one-sample files. `BenchmarkCatalogSharedClock` and `BenchmarkReadManySharedClock` use a 2^20-sample clock. `BenchmarkReadWindowTail` times the same clock's last 64 samples through `Slice` and `CursorAt`.

## Python

The Python and Polars package is [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python). It is not on PyPI yet.

## Releasing

Nothing is tagged or published yet. The proposed first module tag is `v0.1.0`. The notes are in [CHANGELOG.md](CHANGELOG.md). The tag checklist is in [docs/releasing.md](docs/releasing.md). A tag of this module does not publish the Python package.

## License

MIT. See [LICENSE](LICENSE). The core module has no third-party Go dependencies.
