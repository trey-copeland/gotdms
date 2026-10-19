# gotdms

[![Build and test Go module](https://github.com/trey-copeland/gotdms/actions/workflows/build-and-test-go-module.yml/badge.svg)](https://github.com/trey-copeland/gotdms/actions/workflows/build-and-test-go-module.yml)

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

`bytes.Buffer` and other non-seekable writers write the lead-in with the finished length, then stream the raw bytes. A seekable writer (`*os.File`) publishes the NI incomplete-segment sentinel (`0xFFFFFFFFFFFFFFFF`) in the next-segment offset, then patches the real length after the raw bytes are on disk. A crash in between leaves a last segment the reader can consume up to EOF. Output goes through a 1 MiB buffer (`WithWriteBuffer`). Samples passed to `Write` stay in the channel buffer until `Flush`; flush does not allocate a second copy of a contiguous segment.

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

`Open` and `NewReader` parse every segment's metadata and do not load raw samples. `NewReader` accepts an `io.ReaderAt` that can report its length (`*os.File`, `*bytes.Reader`, `*io.SectionReader`, or any `io.Seeker`). `Values` and the typed helpers read through `ReadAt`, so concurrent reads are safe when the underlying reader is. `Cursor` scans sequentially through a 256 KiB buffer (`WithReadBuffer`).

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
| `Group`, `Channel` | Names, paths, properties, `Values`, `Cursor`, typed helpers, `Waveform`, `WaveformHeader`. |
| `Create`, `NewWriter`, `Writer` | `SetProperty`, `Group`, `SetInterleaved`, `Flush`, `Close`, `Stream`. `WithWriteBuffer` (default 1 MiB). |
| `GroupWriter`, `ChannelWriter` | `SetProperty`, `Channel`, `Write`, `WriteWaveform`, `Stream`, `StreamWaveform`. |
| `Stream` | Declared-count segment. `Write`, `WriteInterleaved`, `Finish`. |
| `Waveform[T]`, `ReadWaveform`, `ScaledWaveform` | Analog waveform. `ApplyScale` applies a `LinearScale` explicitly. |
| `WaveformHeader` | t0, dt, unit, and `wf_samples` without loading Y. `TimeAt` timestamps cursor batches. |
| `DataType` | NI `tdsDataType` codes. `String`, `Size`. |
| `Property`, `Timestamp`, `Extended`, `LinearScale` | Values and conversions. |
| `WithReadBuffer` | Sequential buffer for `Cursor` (default 256 KiB). `Values` stays on `ReadAt`. |
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

Metadata parsing reads the lead-in and the metadata bytes of each segment. Raw samples stay on the `io.ReaderAt` until `Values`, a typed helper, or `Cursor`.

`Values` issues `ReadAt` for the bytes it needs. A contiguous numeric channel on a little-endian machine is copied straight into the result slice: there is no intermediate `[]byte` and no per-sample `binary.Read`. Big-endian segments are byte-swapped in place after that read. A big-endian host uses the scalar decoder instead of the copy; `decodePackedScalar` is checked against the bulk path in tests. Bools, timestamps, and 80-bit extended values stay on the scalar path everywhere, because their Go layout is not the on-disk layout.

`Cursor` does not use that one-shot `ReadAt`. It reads ahead through a pooled buffer, 256 KiB by default (`WithReadBuffer`), and reuses the batch slice (`Values` is valid only until the next `Next`). A read at least as large as the buffer skips the cache and goes to `ReadAt`. This package does not mmap. A mapping delivers `SIGBUS` if the file is truncated, and it is a poorer default on 32-bit and non-Unix targets.

Interleaved fixed-size channels are split out in about 1 MiB windows rather than by allocating the whole multiplexed span once per channel. String channels and mixed-width layouts use the same windowed copy; the string offset table is unchanged.

`Write` encodes into the channel buffer. On a little-endian machine that encode is a bulk copy. `Flush` computes the raw size (including a string offset table) and streams metadata, then raw bytes, through a 1 MiB `bufio.Writer` (`WithWriteBuffer`). A contiguous flush does not allocate a second copy of the segment. Interleaved output is mixed in a window of about 1 MiB. Seekable writers still publish the incomplete-segment sentinel and patch eight bytes of the lead-in after the buffer is flushed. The channel buffer itself is one copy of the encoded samples until `Flush`; call `Flush` before a chunk outgrows memory. String payload is streamed at flush. Per-string headers are still allocated on read; a CPU profile of numeric multi-hundred-megabyte files did not show that path, so it was left alone.

There is no mmap switch. The defaults above are the fast path; the options only change buffer size.

`BenchmarkWriteFloat64`, `BenchmarkReadFloat64`, and `BenchmarkStreamFloat64` move 100,000 `float64` values. `BenchmarkLargeReadFloat64`, `BenchmarkLargeReadFloat64File`, `BenchmarkLargeCursorFile`, `BenchmarkLargeInterleaved`, `BenchmarkLargeWriteFile`, and `BenchmarkLargeWriteMem` move 128 MiB (four float64 channels for the interleaved case). `BenchmarkLargeStreamFile` writes that 128 MiB through `Stream` in 64 KiB chunks. `BenchmarkLargeWriteDiscard` and `BenchmarkLargeStreamDiscard` allocate the samples inside the timed loop and discard the bytes, so `B/op` is the writer's footprint. They run only under `-bench`.

On this VM (Linux amd64, 4 vCPU, Go 1.22, `benchtime=300ms`), `BenchmarkLargeWriteFile` was 2223 MB/s and 1.00 MiB/op. `BenchmarkLargeStreamFile` was 3418 MB/s and 1.05 MiB/op. The stream reports about 2100 allocs/op: one small interface header per 64 KiB chunk, not a copy of the chunk and not a metadata rebuild. `BenchmarkLargeWriteDiscard` was 129 MiB/op (the 128 MiB slice allocated for that call and retained until `Close`). `BenchmarkLargeStreamDiscard` was 1.11 MiB/op (one 64 KiB chunk plus the 1 MiB write buffer).

```bash
go test -bench=BenchmarkLarge -benchmem -count=3
```

The numbers below are the median of three `-benchtime=1x` runs on one KVM virtual machine: Intel Xeon (family 6, model 207), 4 vCPU, linux/amd64, Go 1.22.2. They are indicative, not a hardware claim. "Before" is the same benchmark shape on the library as first imported, measured on this VM before the buffered reader, bulk copy, and streaming writer.

| Benchmark | Before | After |
| --- | --- | --- |
| Contiguous float64 `Values`, in memory | 2092 MB/s, 256 MiB/op, 30 allocs/op | 5285 MB/s, 128 MiB/op, 30 allocs/op |
| Contiguous float64 `Values`, `os.File` | 1833 MB/s, 256 MiB/op, 34 allocs/op | 4268 MB/s, 128 MiB/op, 34 allocs/op |
| `Cursor` over `os.File`, batch 4096 | 1927 MB/s, 256 MiB/op, 12319 allocs/op | 7343 MB/s, 386 KiB/op, 4135 allocs/op |
| 4 interleaved float64 channels, in memory | 348 MB/s, 768 MiB/op, 65 allocs/op | 1113 MB/s, 128 MiB/op, 573 allocs/op |
| Write one float64 channel, `os.File` | 985 MB/s, 512 MiB/op, 44 allocs/op | 2221 MB/s, 129 MiB/op, 44 allocs/op |
| Write one float64 channel, `bytes.Buffer` | 804 MB/s, 640 MiB/op, 41 allocs/op | 2477 MB/s, 258 MiB/op, 40 allocs/op |

`b.SetBytes` is the raw sample payload (128 MiB, or 128 MiB across the four interleaved channels). The cursor's allocation drop is the reused batch buffer: one scan no longer allocates a fresh slice per batch. The in-memory write still allocates the encoded channel buffer plus the destination `bytes.Buffer`.

## Layout of the package

`format.go` is the lead-in. `datatype.go` is the type enum. `value.go` encodes properties and channel blocks. `reader.go` is the segment walk and `ReadAt` fetches. `writer.go` is segment assembly. Tests in `golden_test.go` build files with `encoding/binary` and the hex from the NI article, without calling `Writer`, so a bug shared by the writer and the reader cannot satisfy them.

## Datasets

`github.com/trey-copeland/gotdms/dataset` reads a set of TDMS files as one catalog. You declare which channel carries time. Waveform properties are only a fallback, and an undeclared channel with no waveform timing is untimed on purpose.

`WithTimestampChannelName("Time")` says every group has a timestamp channel of that name. `WithTimestampChannel(group, name)` overrides that for one group. An empty name opts the group out of the global name: that group does not use a timestamp channel, and waveform properties may still apply. A later call for the same group replaces the earlier one. The declared channel must be in the same group and file as the data and have the same sample count. If it is missing, the wrong length, or not a timestamp or numeric type, that data channel is untimed and catalog still succeeds. Find and Search mark the declared channel with `IsTimestamp`. It is not used as its own time source.

Timestamp-typed channels are absolute times. Numeric channels (int or float) are seconds since `WithNumericEpoch`. There is no default epoch. Open fails if a declared timestamp channel is numeric and the epoch was not set, so a value of 0 is not silently treated as the Unix epoch. Pass `time.Unix(0, 0).UTC()` when the numbers are Unix seconds. The numeric epoch is not the plotting basis.

Relative time is `sample_time - basis` in seconds. It is zero only at the basis, positive after, and negative before. `WithBasisTime` sets the dataset basis. If it is omitted, the basis is the earliest timed sample in the catalog, chosen once at `Open`. Cropping a window does not move it, so the first returned sample is not re-zeroed. `WithReadBasis` overrides that basis for one `Read`, `ReadWindow`, or `ReadMany`; `ReadMany` applies the same override to every series and leaves `Dataset.Basis` unchanged. `Series.Basis` is the basis that call used. `RelativeTimes` subtracts it. `RelativeTimesFrom` subtracts any other time and does not modify the series. The basis does not have to be a sample. An untimed series ignores it and returns sample indexes. `Resample` keeps absolute times and copies `Basis`.

A step backwards in a timestamp channel returns `ErrMonotonic`. Equal timestamps stay in file order inside a file; across files they follow the overlap policy. The same channel name in several groups returns `*AmbiguousError` unless `Selector.Group` is set. Files are merged in time order. The window is half-open (`start <= t < end`). `OverlapKeepFirst` is the default. Gaps are reported and are not filled with NaN. `Resample` (`InterpLinear`, `InterpPrevious`) is opt-in and does not extrapolate.

When a group has no timestamp declaration, `StartTime` and `Increment` (`wf_start_time`, `wf_increment`) supply the axis. Sample `i` is that start plus `i` times the increment, rounded to the nearest nanosecond. `Slice` reads a sample range. Dataset waveform windows do not call it yet, so a waveform window still decodes from the first sample and stops at the window. Per-segment sample bounds are not exported. A tail window still pays for the prefix. Declared timestamp values are decoded in full on read, because the times are the samples. Catalog reads the first two samples of a clock whose first step is positive and derives the span from that step; it does not scan the axis for monotonicity. Read returns `ErrMonotonic` if a later sample steps backwards. `ReadMany` opens each file once and decodes a shared timestamp channel once per call. `WithReadWorkers` caps how many files that call opens (default 4).

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

`Example` in package `dataset` is the compile-checked copy: 100 files, a global timestamp channel, find without a group, one window, and relative times against an explicit trigger. Benchmarks run only under `go test -bench`. `BenchmarkCatalog` indexes 100 one-sample files. `BenchmarkCatalogSharedClock` and `BenchmarkReadManySharedClock` use a 2^20-sample clock. `BenchmarkReadWindowTail` times the same clock's last 64 samples and still decodes the prefix; it is the cost of having no core range read, not a fast path.

## Python

The Python and Polars package is [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python). It is not on PyPI yet.

## Releasing

Nothing is tagged or published yet. The proposed first module tag is `v0.1.0`. The notes are in [CHANGELOG.md](CHANGELOG.md). The tag checklist is in [docs/releasing.md](docs/releasing.md). A tag of this module does not publish the Python package.

## License

MIT. See [LICENSE](LICENSE). The core module has no third-party Go dependencies.
