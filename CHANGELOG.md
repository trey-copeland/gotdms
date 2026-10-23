# Changelog

The Go module proxy learns about a version only after a `vX.Y.Z` tag is pushed to `github.com/trey-copeland/gotdms`. See [docs/releasing.md](docs/releasing.md). That tag does not publish the Python package.

## Unreleased

## v0.1.2 (2026-10-23)

`v0.1.2` is the changes since `v0.1.1` (`7d514a7`). The release-workflow checkout and the module-cache test skip landed in `e2bd37c` ([#18](https://github.com/trey-copeland/gotdms/pull/18)). The metadata-gap fix for NI HSAI DAQmx captures landed in `53fa09e` ([#19](https://github.com/trey-copeland/gotdms/pull/19)).

### Fixed

- A segment whose raw-data offset points past the end of its object list opens. The bytes between the last object and the offset are skipped, which is what npTDMS does and what the lead-in's raw-data offset defines. Before, the open failed with `N unused metadata bytes: corrupt TDMS structure`. NI DAQ high-speed captures (2621 and about 3607 bytes past the object list) hit this with DAQmx raw data. The skip starts only after the declared object count has been read in full: an object list that ends inside its metadata, or a raw-data offset past EOF, still fails the open. The sidecar fingerprint already covers those skipped bytes.
- `.github/workflows/test-and-publish-go-release.yml` checks out with `fetch-depth: 0` and `fetch-tags: true`, matching the module workflow, so a tag run can read `v0.1.0`. `TestSegmentIndexV010MissesVersion2` skips when the package directory is not inside a git work tree (`git rev-parse --is-inside-work-tree`). A module-cache `go test`, including `go test all`, no longer fails for the missing `.git`. When `CI` is set, a missing work tree or tag is still a failure and the message names that checkout.

## v0.1.1 (2026-10-22)

`v0.1.1` is the changes since `v0.1.0` (`622cb34`). Those changes landed on `main` in `2c3aa15` ([#16](https://github.com/trey-copeland/gotdms/pull/16)).

### Added

- `BuildSegmentIndex` parses lead-ins and metadata of a TDMS file and writes `SegmentIndexPath` (`path + ".gtdx"`) atomically. Raw samples are not read. A failure to write the sidecar is returned. `Open` still ignores that failure. `BuildSegmentIndexFile` returns the sidecar path. `WithLenientTail` is honored, so a sidecar built from a past-EOF tail matches a later lenient open and stays a miss on a strict open.
- `gotdms index [--force] [--check] file.tdms...` builds a sidecar for each file. A directory or a pattern is accepted. Without `--force`, a sidecar that already matches is left in place. `--check` reports whether each existing sidecar matches and does not write. The exit status is non-zero when any file fails. An argument that already exists is that path, including a name that contains `*`, `?`, or `[`. A repeated path is indexed once. A directory entry that is not a regular file is skipped.
- `SegmentIndexValid` reports whether the conventional sidecar matches under the full fingerprint check. It does not write.
- `WithSegmentIndexTrustIdentity` is opt-in. A sidecar is accepted when size, modification time in nanoseconds, device, and inode match, and the lead-in and metadata fingerprint is not recomputed. The default remains the full fingerprint check. An in-place edit that preserves size and modification time (including one whose modification time is restored with `os.Chtimes`) is missed. `dataset.WithSegmentIndexTrustIdentity` opts catalog and later reads into the same mode and implies the sidecar. `dataset.WithSegmentIndex(false)` turns it back off.
- On Windows the device and inode are the volume serial number and the file index from `GetFileInformationByHandle`, so a replaced file is a miss even when size and modification time match. On a platform that still reports no device and inode, trust mode checks only size and modification time and stores both identity fields as 0. FAT modification times step by 2 seconds, and exFAT by 10 milliseconds, so two writes in one step share a timestamp.
- Segment-index sidecars are version 2. Consecutive regular spans of one channel are one run (start position, step, repeat count). A single segment is 96 bytes on disk. A run sets flag bit 5 and appends the step and count, 112 bytes on disk. In memory the span stays 112 bytes and holds a run index; the step and count live in a side table, and index 0 is one segment. `Values`, `Slice`, and `Cursor` walk a run and leave a single segment on the one-segment path. Strings, live segments, partial tails, and any layout change stay exact single spans. Version 2 is stored already merged, so open does not coalesce it again. Version 1 sidecars still decode and are coalesced in memory. A v0.1.0 reader rejects any version other than 1, so a version 2 sidecar is a miss for that release. New writes are version 2.
- A span that would address bytes outside the file, or that claims more samples than the file can hold, is a decode error and therefore a cache miss. A run is checked the same way: `start + step*(count-1)` plus the chunk extent must lie in the file, and the multiplication is overflow-checked. Each channel's total sample bytes (each DAQmx scaler on its own) must also fit. Digital lines count one byte per sample, so several lines may share a raw byte. Strings, which are not merged into runs, are capped at one sample per four file bytes.
- The `.gtdx` flag word records a truncated final segment (bit 2) so a `WithLenientTail` open can restore `FileStatus.Truncated` from the sidecar. Any other header flag bit is a decode error.
- A regular DAQmx scaler layout uses the same run records as any other channel. The sidecar still appends the DAQmx block after the channel total.
- `BenchmarkSegmentIndexOpen` covers uniform, mixed, and alternating sample counts at 10,000 and 100,000 segments. Figures are in the README.

### Fixed

- `ReadValues` and `Group.Values` add each channel's base sample index when splitting an interleaved span. A channel whose interleaved data continued in a later segment was written at the start of its output. This also affects v0.1.0.
- Coalescing spans keeps the count and step of a span that is already a run. Clearing that index would drop the other repeats. Version 1 sidecars are the ones coalesced on decode.
- A sidecar is keyed to the stat of the open file handle taken before the parse. `BuildSegmentIndex` and `Open` refuse the write when size, modification time, device, or inode changes during the parse, so a path replaced while that handle is open is not published as a trust-identity hit for the new file.
- The sidecar's mode is the data file's read and write bits plus owner write. `os.CreateTemp` alone would leave it `0600`. A filesystem that rejects chmod still keeps the sidecar.
- Decoding a sidecar does not reserve a property slice for the full on-disk count. The count is bounded by the bytes left, and a `Property` is much larger than those four bytes.
- Replacing a sidecar removes an existing regular file only. A directory or other object at that path is left in place. After a successful rename the directory is synced on a best-effort basis.

## v0.1.0 (2026-10-22)

`v0.1.0` is `622cb34` ("Read DAQmx raw data from NI index layouts"). The notes below are the changelog that was on that tree: the reader and writer, analog waveforms, the dataset package, CI, and the changes through that commit. The core module is `github.com/trey-copeland/gotdms`, `go 1.22`, with no `require` block.

### Breaking

- `ChannelWriter.Write` copies its slice. On a little-endian host the first `Write` of a fixed-size numeric or complex slice used to retain the caller's memory, so refilling that slice and writing it again changed bytes already buffered. `Write` now copies. `WriteNoCopy` is the explicit opt-in that retains the slice; the caller must not mutate it until `Flush` or `Close` returns. Bool, timestamp, extended, and string values were already copied.

### Added

- DAQmx raw data is read. Index words `0x00001269` and `0x00001369` are format-changing scalers (five `uint32` fields). `0x0000126A` is a digital-line scaler and yields `[]uint8` of 0 and 1. The first segment may itself set `kTocDAQmxRawData`. `Values`, `Slice`, `Cursor`, `ReadValues`, and `Float64s` return the primary scaler's raw native values. `Channel.DAQmx` and `DAQmxSlice` expose the raw-data index and any other scaler. `NI_Scaling_*` and `NI_Scale[*]_*` stay metadata. `DAQmxScaledFloat64s` and `ApplyDAQmxScale` apply a linear or polynomial chain when the caller asks, with npTDMS's rules: the output is index `NI_Number_Of_Scales - 1` (a count of 0 applies nothing), and an index with no scale type is the DAQmx scaler with that scale id. Thermocouple, RTD, table, strain, and add/subtract are not applied. As in npTDMS, the index words decide the layout (a standard index under `kTocDAQmxRawData` reads as standard data), a DAQmx index may carry its one scaler's type instead of `0xFFFFFFFF`, and raw buffer widths and offsets may change between segments. An unsupported layout still returns `ErrDAQmx`. DAQmx channels are not written. The `.gtdx` sidecar stays version 1 and appends a DAQmx block after the channel total when the channel has one.
- Nightly mutation testing of the core package with Gremlins v0.6.0 (Apache-2.0). `.github/workflows/run-mutation-testing.yml` (`Run mutation testing on the core package`) runs at 06:00 UTC and on `workflow_dispatch`. It does not run on pull requests. A survived mutant does not fail the job. The JSON report is uploaded as an artifact. How to run it locally is in [docs/mutation-testing.md](docs/mutation-testing.md). Gremlins is not a module dependency.
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
- Removed `bridge/`. The Python bridge was developed in this repository and extracted to [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python) on 2026-10-19 (commit `ae5e15b`; the work spanned 2026-10-18/19). This repository no longer ships that tree. The package is not on PyPI. A tag of this module does not publish it.
- Renamed the GitHub Actions workflows. `.github/workflows/ci.yml` (`name: ci`) is now `.github/workflows/build-and-test-go-module.yml` (`Build and test Go module`). `.github/workflows/release.yml` (`name: release`) is now `.github/workflows/test-and-publish-go-release.yml` (`Test and publish Go module release`). Triggers and steps are unchanged. The release workflow still runs only on a `v*` tag.

### Core reader and writer

- Read segment metadata without loading samples. Groups, channels, and properties at file, group, and channel level.
- Typed channel reads and a cursor that streams samples in batches.
- A writer that flushes segments, including interleaved channels, and patches the lead-in on seekable files.
- Timestamps with full fraction fields, extended floats, and NI linear scale properties. Reads return raw samples. The scale is not applied unless the caller uses it.
- DAQmx raw data is read from the NI index layouts described under Added. An unsupported layout returns `ErrDAQmx`. DAQmx channels are not written.
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

`.github/workflows/build-and-test-go-module.yml` is on `main`. Push to `main` and pull requests run gofmt, vet, and `go test ./...` on Go 1.22 and 1.27. Go 1.22 also runs `go test -race` for the root package and for `dataset/`. Windows runs `go test ./...` on Go 1.22. The workflow does not build release binaries.

### Bridge

The Python bridge was developed in-tree under `bridge/` and merged at `795c8b1`. It was its own Go module, so `go test ./...` in this module never entered it. It was extracted to [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python) on 2026-10-19 (`ae5e15b`) and is no longer shipped from this repository. It is not on PyPI. A tag of `github.com/trey-copeland/gotdms` does not publish it.
