# Changelog

Nothing in this repository has been tagged or published. The notes below are the proposed text for the first tag, not a release.

The Go module proxy learns about a version only after a `vX.Y.Z` tag is pushed to `github.com/trey-copeland/gotdms`. See [docs/releasing.md](docs/releasing.md). That tag does not publish the Python package.

## Unreleased

Release-prep documents only. No library behavior change.

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

`bridge/` is in this tree and is still pending extraction to its own repository. It is a separate module, `github.com/trey-copeland/gotdms-python`, so `go test ./...` in the core module does not enter it. The Python distribution name in that tree is `gotdms`. Nothing has been registered or uploaded.

- Protocol version 1 between `gotdms-bridge` and the Python client. Sample columns are Arrow IPC, not JSON or base64.
- Polars frames and NumPy arrays. Raw samples unless `scaled` is set.
- Wheels and an sdist are described in `bridge/`, including a tag-gated PyPI job that does nothing until that repository and a trusted publisher exist.

Tagging `github.com/trey-copeland/gotdms` does not publish that Python package and does not create the `gotdms-python` repository.
