# gotdms

[![Build and test Go module](https://github.com/trey-copeland/gotdms/actions/workflows/build-and-test-go-module.yml/badge.svg)](https://github.com/trey-copeland/gotdms/actions/workflows/build-and-test-go-module.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/trey-copeland/gotdms.svg)](https://pkg.go.dev/github.com/trey-copeland/gotdms)

A pure Go reader and writer for NI TDMS 2.0 files. No cgo. The Go package name is `tdms`; the module path is `github.com/trey-copeland/gotdms`.

TDMS is the binary format LabVIEW and other National Instruments tools use for measurement data. The `dataset` subpackage reads a set of TDMS files as one time-aligned catalog. The byte layout and the format choices are in [docs/format.md](docs/format.md).

## Install

```bash
go get github.com/trey-copeland/gotdms
```

```go
import "github.com/trey-copeland/gotdms"
```

## Write and read

```go
w, err := tdms.Create("run.tdms")
if err != nil {
	return err
}
defer w.Close() // a second Close is a no-op

ch, err := w.Group("acquisition").Channel("temperature", tdms.TypeFloat64)
if err != nil {
	return err
}
if err := ch.SetProperty(tdms.PropUnitString, "degC"); err != nil {
	return err
}
if err := ch.Write([]float64{21.5, 21.7, 22.0, 22.4}); err != nil {
	return err
}
// Flush ends a segment. Call it again as more samples arrive.
if err := w.Flush(); err != nil {
	return err
}
if err := ch.Write([]float64{22.8}); err != nil {
	return err
}
return w.Close()
```

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
fmt.Println("values:", values)

// Stream without holding the whole channel.
cur := ch.Cursor(4)
for cur.Next() {
	fmt.Println("batch:", cur.Values().([]float64))
}
return cur.Err()
```

Both snippets are `ExampleCreate` in [example_docs_test.go](example_docs_test.go), checked by `go test`. `Create(path, tdms.WithSync())` fsyncs each segment for power-loss safety; see [docs/writing.md](docs/writing.md).

## Command line

```bash
go run ./cmd/gotdms info run.tdms              # groups, channels, properties, file status
go run ./cmd/gotdms info --lenient run.tdms    # open a torn file and print the size to truncate to
go run ./cmd/gotdms convert -o run.csv run.tdms
go run ./cmd/gotdms index run.tdms             # write a .gtdx segment-index sidecar
```

Flags and output are in [docs/cli.md](docs/cli.md).

## Features

- Reads and writes TDMS 2.0 (version 4713); reads TDMS 1.0 (4712) and big-endian segments. [Format and support](docs/format.md)
- Lazy reads: `Open` parses metadata only; `Values`, `Slice`, `Cursor`, and typed helpers fetch samples on demand. [Reading](docs/reading.md)
- Buffered segment writes, declared-count `Stream` writes, interleaved channels, and `WithSync` durable writes. [Writing](docs/writing.md)
- Waveforms with full-resolution `Timestamp` t0 and explicit linear scales. [Waveforms](docs/waveforms.md)
- DAQmx raw data in the layouts NI hardware writes, with opt-in linear and polynomial scaling. [DAQmx](docs/daqmx.md)
- Incomplete and torn-tail recovery (`WithLenientTail`), file status, and `Follow` of a file still being written. [File status](docs/file-status.md)
- Multi-file datasets with declared time channels, merging, windows, shared clocks (`WithSharedClock`), and values-only reads (`WithoutTimes`). [Datasets](docs/dataset.md)
- Per-chunk count, min, max, mean, and RMS in one pass. [Reading](docs/reading.md#chunk-statistics)
- An opt-in `.gtdx` segment-index sidecar for fast reopen of fragmented files. [Segment index](docs/segment-index.md)
- The `gotdms` command: `info`, `convert` to CSV, and `index`. [CLI](docs/cli.md)
- Read and write paths, buffers, and benchmark figures. [Performance](docs/performance.md)
- Fuzzed, golden-tested against the NI article's hex, differentially tested against npTDMS in CI, and mutation-tested nightly. [Testing](docs/testing.md)

Not supported: fixed-point (`ErrUnsupported`), writing DAQmx raw data, writing big-endian segments, and NI `*.tdms_index` files (`ErrIndexFile`). Extended float is spec-derived and unverified against NI. See [Limitations](docs/format.md#limitations). The full reference index is [docs/README.md](docs/README.md).

## Python

The Python and Polars package is [github.com/trey-copeland/gotdms-python](https://github.com/trey-copeland/gotdms-python). It is not on PyPI yet.

## Releasing

`v0.2.0` is the latest tag. Notes for each release are in [CHANGELOG.md](CHANGELOG.md). The tag checklist is in [docs/releasing.md](docs/releasing.md). A tag of this module does not publish the Python package.

## Contributing

Branches, pull requests, and the required checks on `main` are in [CONTRIBUTING.md](CONTRIBUTING.md). The module declares `go 1.22`; CI tests Go 1.22 and 1.27.

## License

MIT. See [LICENSE](LICENSE). The core module has no third-party Go dependencies.

No copyleft code is linked or shipped. npTDMS (LGPL-3.0-only) is used only as a CI test oracle: CI installs it with pip in one job, the Go module never imports it, and no release contains it. See [docs/testing.md](docs/testing.md#nptdms-differential-tests-ci-only).
