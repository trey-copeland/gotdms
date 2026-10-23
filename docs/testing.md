# Testing

[Back to the docs index](README.md)

From a checkout of this repository:

```bash
go test ./...
go test -bench=. ./...
```

## Fuzzing

`go test -fuzz=FuzzReader` and `go test -fuzz='^FuzzSegmentIndex$'` run the fuzzers. The anchors matter for the second one: `-fuzz` is a regular expression, and an unanchored `FuzzSegmentIndex` also matches `FuzzSegmentIndexTrusted`, so `go test` refuses to fuzz. Fuzzing is not part of `go test` by default.

The other targets are `FuzzSegmentIndexTrusted` in the root package and `FuzzMergeRuns` in `dataset` (`go test -fuzz=FuzzMergeRuns ./dataset`). Their seed corpora run under plain `go test`.

## npTDMS differential tests (CI only)

`testdata/nptdms` is the differential harness against npTDMS. That package is LGPL-3.0-only, is pinned to 1.13.0, and runs only in CI; it is not a Go dependency. There is no LabVIEW here, so npTDMS is the external anchor. See [`testdata/nptdms/README.md`](../testdata/nptdms/README.md).

npTDMS is a test oracle only. It is installed with pip in the `differential test against npTDMS` CI job, is not imported or linked by the Go module, and is never shipped in a release.

## Golden tests and package layout

`format.go` is the lead-in. `datatype.go` is the type enum. `value.go` encodes properties and channel blocks. `reader.go` is the segment walk and `ReadAt` fetches. `writer.go` is segment assembly. Tests in `golden_test.go` build files with `encoding/binary` and the hex from the NI article, without calling `Writer`, so a bug shared by the writer and the reader cannot satisfy them.

## Mutation testing

A nightly [Gremlins](https://github.com/go-gremlins/gremlins) run checks the core package. It is a quality report, not a pull-request gate. The pinned command and how to read a survivor are in [mutation-testing.md](mutation-testing.md).

## Examples

Example functions run under `go test`: `Example_roundTrip`, `ExampleCreate`, `ExampleWithSync`, `ExampleWithLenientTail`, `ExampleReadWaveform`, `Example_stream`, and `ExampleChannel_ChunkStats` in the root package, and `Example`, `ExampleWithSharedClock`, and `ExampleWithoutTimes` in `dataset`. `examples/write` and `examples/read` are runnable programs.
