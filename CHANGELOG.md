# Changelog

Nothing in this repository has been tagged or published. The notes below are the proposed text for the first tag, not a release.

The Go module proxy learns about a version only after a `vX.Y.Z` tag is pushed to `github.com/trey-copeland/gotdms`. See [docs/releasing.md](docs/releasing.md). That tag does not publish the Python package.

## Unreleased

### Breaking

- `ChannelWriter.Write` copies its slice. On a little-endian host the first `Write` of a fixed-size numeric or complex slice used to retain the caller's memory, so refilling that slice and writing it again changed bytes already buffered. `Write` now copies. `WriteNoCopy` is the explicit opt-in that retains the slice; the caller must not mutate it until `Flush` or `Close` returns. Bool, timestamp, extended, and string values were already copied. Nothing in this module has been tagged, so the change is in the unreleased tree.

### Added

- `WithLenientTail` opens a file whose final segment declares a finite next-segment offset past EOF. Earlier segments are kept, and whole values that fit in the partial tail are readable. `File.Status.Truncated` reports that recovery. `PartialTail` and `UnusedTail` describe the tail the same way they describe an incomplete sentinel. The default `Open` still returns `ErrTruncated`. A short trailing lead-in, or metadata that itself runs past EOF, is still `ErrTruncated`.
- `Follow` opens with `WithLenientTail`. Samples in that past-EOF tail are not committed, so `Next` delivers earlier finished segments and waits for the declared length.
- `cmd/gotdms convert` writes `#N/A` when a channel has no sample on that row. A present empty string stays an empty field. A string value that is itself `#N/A` looks the same.
- `cmd/gotdms info --json` includes `status.truncated`.
- CI job `differential test against npTDMS` writes files with npTDMS and reads them with gotdms, and the other way around. npTDMS is LGPL-2.1, installed with pip in that job only, and is not linked or shipped. The harness is `testdata/nptdms`. There is no LabVIEW in this environment, so npTDMS is the external anchor.
- `FuzzSegmentIndex` fuzzes `.gtdx` decoding. `FuzzReader` seeds now include a multi-segment file, a string channel, and an interleaved segment.
- `TestGoldenNIFiveSegments` decodes the NI article's full five-segment worked example from the article's hex.

### Fixed

- A metadata-only segment that declares a raw-data index, or that changes the object list, updates the order a following raw-only segment uses. A channel that drops out is not given another channel's samples. A raw section whose length is not a multiple of the current chunk, and that is not an incomplete or lenient tail, returns `ErrCorrupt`.
- Length-prefixed strings drop one trailing NUL on read, which is what the TDMS article specifies. A NUL that is not the last byte is kept. The writer does not add one. A string that ends in NUL does not round-trip that byte.
- Dataset waveform windows and positive-step timestamp windows read the sample range with `CursorAt` and `Slice`. Samples before the window are not decoded. A clock that does not match the catalog step is still decoded in full.
- A `.gtdx` span that addresses bytes outside the file, or that claims more samples than the file can hold, is a decode error. `Open` treats that as a cache miss.

### Changed

- README benchmark table remeasured with `-count=10 -benchtime=100ms`. Medians are by hand (benchstat is not vendored). `Write` of 128 MiB to a file is 1403 MB/s and 129.00 MiB/op after the copy (2989 MB/s and 1.00 MiB/op when the first slice was aliased).
- Dataset tail-window README figures are medians of 10 runs (`-count=10`, default benchtime) on a 4 vCPU cloud VM, a synthetic 1,048,576-sample file, and a 64-sample window. `BenchmarkReadWindowTail` is 27.32 µs, 8,398 B/op, and 66 allocs/op (was 28.46 ms, 41,948,728 B/op, and 60 allocs/op). `BenchmarkReadWindowTailWaveform` is 14.80 µs, 6,909 B/op, and 63 allocs/op (was 599.6 µs, 71,040 B/op, and 60 allocs/op).
- Extended-float comments state that the 10-byte layout is spec-derived and has no NI fixture. The bytes are unchanged.

- `cmd/gotdms` prints file structure (`info`, optionally `--json`) and writes channel samples as CSV (`convert`). The CSV has a header row. Timestamps are UTC RFC3339Nano because the channel type is a timestamp, not because of the channel name. A missing sample is `#N/A`. An empty string is an empty field.
- `Channel.ChunkStats` reports count, min, max, mean, and RMS for each chunk of a numeric channel in one Cursor pass. The caller chooses the chunk size in samples. String, boolean, timestamp, and complex channels return `ErrType`.
- `File.Status` reports a clean end, an incomplete final segment (the NI `0xFFFFFFFFFFFFFFFF` next-segment offset), or a truncated tail opened with `WithLenientTail`. Reads of files that open still return whole values in that tail.
- `Follow` polls a file that is still being written and yields new samples for one channel. Samples that belong to a segment whose lead-in is still the incomplete sentinel, and samples recovered from a past-EOF tail, are not delivered. The context cancels the wait. `WithTailInterval` and `WithTailBatch` set the poll period and the batch size.
- `WithSegmentIndex` restores segment layout from a sidecar when the file's size, modification time, device and inode (when the platform has them), and an IEEE CRC-32 of the lead-ins and metadata still match. Raw samples are not cached. A missing, stale, or corrupt sidecar is ignored and the file is parsed. The sidecar is replaced by a temporary file and `rename`. `SegmentIndexPath` is the conventional `path + ".gtdx"`. `dataset.WithSegmentIndex` opts the catalog and its reads into that cache.
- `Writer.Stream`, `ChannelWriter.Stream`, and `ChannelWriter.StreamWaveform` write one segment whose sample counts are declared up front. Chunks are copied to the underlying writer as they arrive, so a large channel is not held until `Flush`. `Flush` still streams the channel buffer. `Write` copies; see the breaking note above.
- String channels return `ErrUnsupported` from `Stream` (the offset table needs every length). A short or failed stream is left incomplete on a seekable writer and truncated on a non-seekable writer; it is not patched into a finished segment.
- Removed `bridge/`. The Python bridge was developed in this repository and extracted to [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python) on 2026-10-19 (commit `ae5e15b`; the work spanned 2026-10-18/19). This repository no longer ships that tree. The package is not on PyPI, and nothing here has been tagged.
- Renamed the GitHub Actions workflows. `.github/workflows/ci.yml` (`name: ci`) is now `.github/workflows/build-and-test-go-module.yml` (`Build and test Go module`). `.github/workflows/release.yml` (`name: release`) is now `.github/workflows/test-and-publish-go-release.yml` (`Test and publish Go module release`). Triggers and steps are unchanged. The release workflow still runs only on a `v*` tag.

## 0.1.0 (proposed, not tagged)

Proposed notes for `main` at `795c8b1` ("Add a Polars bridge off npTDMS onto the dataset layer"). That commit is the tree after the reader and writer, analog waveforms, the dataset layer, CI, and the Python bridge were merged. Nothing is tagged. The core module is still `github.com/trey-copeland/gotdms`, `go 1.22`, with no `require` block.

### Core reader and writer

- Read segment metadata without loading samples. Groups, channels, and properties at file, group, and channel level.
- Typed channel reads and a cursor that streams samples in batches.
- A writer that flushes segments, including interleaved channels, and patches the lead-in on seekable files.
- Timestamps with full fraction fields, extended floats, and NI linear scale properties. Reads return raw samples. The scale is not applied unless the caller uses it.
- DAQmx raw data is rejected rather than decoded.
- `Example_roundTrip` in `example_test.go` is the pkg.go.dev example.
- MIT license. The core `go.mod` has no third-party dependencies.

### Analog waveforms and range reads

- `Waveform[T]`, `ReadWaveform`, `Channel.Waveform`, and `WaveformHeader` for `wf_start_time`, `wf_increment`, `wf_samples`, and `unit_string`. Scaling stays explicit (`ApplyScale`, `ScaledWaveform`).
- `Channel.Slice` reads a sample range. `CursorAt` streams one. Both use `ReadAt` and do not walk earlier samples.
- `ReadValues` and `Group.Values` read several channels at once and split a shared interleaved segment in one pass.

### Dataset

`github.com/trey-copeland/gotdms/dataset` is in this module.

- Multi-file catalog (`OpenFiles`, `OpenGlob`) with a declared timestamp channel, globally or per group. An empty per-group name opts that group out. Waveform properties are only the fallback when no timestamp channel was declared.
- Numeric timestamp channels are seconds since `WithNumericEpoch`. There is no default epoch.
- Relative time is sample time minus a basis. `WithBasisTime` fixes the dataset basis. A window does not re-zero it. `WithReadBasis` overrides the basis for one read. `ReadMany` shares one clock payload across the channels in that call.
- `Example` in package `dataset` is the compile-checked copy.

### CI

`.github/workflows/ci.yml` is on `main`. Push to `main` and pull requests run gofmt, vet, and `go test ./...` on Go 1.22 and 1.27. Go 1.22 also runs `go test -race` for the root package and for `dataset/`. Windows runs `go test ./...` on Go 1.22. The workflow does not build release binaries.

### Bridge

The Python bridge was developed in-tree under `bridge/` and merged at `795c8b1`. It was its own Go module, so `go test ./...` in this module never entered it. It was extracted to [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python) on 2026-10-19 (`ae5e15b`) and is no longer shipped from this repository. It is not on PyPI. A tag of `github.com/trey-copeland/gotdms` does not publish it.
