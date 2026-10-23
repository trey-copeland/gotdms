# Datasets

[Back to the docs index](README.md)

`github.com/trey-copeland/gotdms/dataset` reads a set of TDMS files as one catalog. You declare which channel carries time. Waveform properties are only a fallback, and an undeclared channel with no waveform timing is untimed on purpose.

## Declaring time

`WithTimestampChannelName("Time")` says every group has a timestamp channel of that name. `WithTimestampChannel(group, name)` overrides that for one group. An empty name opts the group out of the global name: that group does not use a timestamp channel, and waveform properties may still apply. A later call for the same group replaces the earlier one. The declared channel must be in the same group and file as the data and have the same sample count. If it is missing, the wrong length, or not a timestamp or numeric type, that data channel is untimed and catalog still succeeds. Find and Search mark the declared channel with `IsTimestamp`. It is not used as its own time source.

Timestamp-typed channels are absolute times. Numeric channels (int or float) are seconds since `WithNumericEpoch`. There is no default epoch. Open fails if a declared timestamp channel is numeric and the epoch was not set, so a value of 0 is not silently treated as the Unix epoch. Pass `time.Unix(0, 0).UTC()` when the numbers are Unix seconds. The numeric epoch is not the plotting basis.

## Basis and relative time

Relative time is `sample_time - basis` in seconds. It is zero only at the basis, positive after, and negative before. `WithBasisTime` sets the dataset basis. If it is omitted, the basis is the earliest timed sample in the catalog, chosen once at `Open`. Cropping a window does not move it, so the first returned sample is not re-zeroed. `WithReadBasis` overrides that basis for one `Read`, `ReadWindow`, or `ReadMany`; `ReadMany` applies the same override to every series and leaves `Dataset.Basis` unchanged. `Series.Basis` is the basis that call used. `RelativeTimes` subtracts it. `RelativeTimesFrom` subtracts any other time and does not modify the series. The basis does not have to be a sample. An untimed series ignores it and returns sample indexes. `Resample` keeps absolute times and copies `Basis`.

## Shared clocks (`WithSharedClock`)

`WithSharedClock` is a read option that stores the time axis once per clock instead of once per channel. Each timed series gets `Clock` (`[]int64` Unix nanoseconds, 8 bytes per sample) instead of `Times` (24 bytes per sample). Series in one `Read`, `ReadWindow`, or `ReadMany` call that come from the same clock share one `*Clock`: channels read against one decoded timestamp channel, and waveform channels with the same `wf_start_time`, `wf_increment`, and sample range. `RelativeTimes` returns the same values. `AbsoluteTimes` builds `[]time.Time` from either form, and `Clock.Time(i)` returns one sample in UTC. A series whose times do not fit in `int64` nanoseconds (before 1678 or after 2262) keeps `Times`, and a series merged from several files gets its own `Clock`. The `Clock` is shared, so do not modify `Ns`.

`ExampleWithSharedClock` is the compile-checked copy.

## Values only (`WithoutTimes`)

`WithoutTimes` is a read option for callers that need only the samples. A timed series comes back with `Times` and `Clock` both nil, and no time axis is built or kept: a waveform's sample times are never computed, and a timestamp channel is decoded only as far as the read needs it (the window, the monotonic check, `Dt`, a merge across files) and then dropped. `Y`, `Files`, `Timed`, `Source`, `IsTimestamp`, `Dt`, `Gaps`, and `Basis` match the same read without the option, and so do its errors, including overlap errors. `Resample` returns `ErrArgument` for such a series, and `RelativeTimes`, `RelativeTimesFrom`, and `AbsoluteTimes` return nil rather than one value per sample. `WithoutTimes` overrides `WithSharedClock`.

`ExampleWithoutTimes` is the compile-checked copy.

## Merging, windows, and waveform timing

A step backwards in a timestamp channel returns `ErrMonotonic`. Equal timestamps stay in file order inside a file; across files they follow the overlap policy. The same channel name in several groups returns `*AmbiguousError` unless `Selector.Group` is set. Files are merged in time order. The window is half-open (`start <= t < end`). `OverlapKeepFirst` is the default. Gaps are reported and are not filled with NaN. `Resample` (`InterpLinear`, `InterpPrevious`) is opt-in and does not extrapolate.

When a group has no timestamp declaration, `StartTime` and `Increment` (`wf_start_time`, `wf_increment`) supply the axis. Sample `i` is that start plus `i` times the increment, rounded to the nearest nanosecond. A waveform window and a declared clock whose catalog step is positive read that sample range with `CursorAt` and `Slice`. Samples before the window are not decoded. A declared clock that does not match the step is decoded in full, so a backwards step is still `ErrMonotonic`. Catalog reads the first two samples of a clock whose first step is positive and derives the span from that step; it does not scan the axis for monotonicity. `ReadMany` opens each file once and decodes a shared timestamp channel once per call. `WithReadWorkers` caps how many files that call opens (default 4).

## Torn files and the segment index

`dataset.WithLenientTail` opens every file with `tdms.WithLenientTail`, so a torn tail is dropped instead of failing the catalog, and `Dataset.FileStatus` reports what each file dropped. The rules are in [File status, torn tails, and Follow](file-status.md).

`dataset.WithSegmentIndex` and `dataset.WithSegmentIndexTrustIdentity` turn on the `.gtdx` sidecar cache for catalog and reads (off by default); see [Segment index](segment-index.md).

## Example

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

## Benchmarks

`BenchmarkReadWindowTail` times a 64-sample tail of a synthetic 1,048,576-sample file on a declared timestamp axis. The figures are medians of 10 runs (`go test -run '^$' -bench 'BenchmarkReadWindowTail' -benchmem -count=10 ./dataset`, default benchtime; each median is the average of the 5th and 6th sorted runs) on a 4 vCPU Intel Xeon cloud VM, Go 1.22.2. Before the range read that benchmark was 28.46 ms, 41,948,728 B/op (~40.0 MiB), and 60 allocs/op. It is now 27.32 µs, 8,398 B/op, and 66 allocs/op, about 1041× in time and about 4995× in bytes. The old path decoded all 1,048,576 samples of two channels plus a million `time.Time` values, about 42 MB allocated. The new path seeks and decodes 64 clock samples and 64 data samples. `BenchmarkReadWindowTailWaveform` is that same 64-sample tail on a `wf_*` axis: 599.6 µs, 71,040 B/op, and 60 allocs/op before, and 14.80 µs, 6,909 B/op, and 63 allocs/op after (about 40.5×).
