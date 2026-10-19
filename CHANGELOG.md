# Changelog

Nothing in this repository has been tagged or published. The notes below are the proposed text for the first tag, not a release.

The Go module proxy learns about a version only after a `vX.Y.Z` tag is pushed to `github.com/trey-copeland/gotdms`. See [docs/releasing.md](docs/releasing.md). That tag does not publish the Python package.

## Unreleased

- `Writer.Stream`, `ChannelWriter.Stream`, and `ChannelWriter.StreamWaveform` write one segment whose sample counts are declared up front. Chunks are copied to the underlying writer as they arrive, so a large channel is not held until `Flush`. `Write` and `Flush` are unchanged.
- String channels return `ErrUnsupported` from `Stream` (the offset table needs every length). A short or failed stream is left incomplete on a seekable writer and truncated on a non-seekable writer; it is not patched into a finished segment.
- Removed `bridge/`. The Python bridge was developed in this repository and extracted to [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python) on 2026-10-19 (commit `ae5e15b`; the work spanned 2026-10-18/19). This repository no longer ships that tree. The package is not on PyPI, and nothing here has been tagged.

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
