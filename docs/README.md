# gotdms documentation

The [README](../README.md) is the short tour. These pages are the reference. The Go API reference, with runnable examples, is on [pkg.go.dev](https://pkg.go.dev/github.com/trey-copeland/gotdms).

| Page | What it covers |
| --- | --- |
| [Reading](reading.md) | `Open`, `NewReader`, `Values`, `Cursor`, typed helpers, waveform properties, chunk statistics. |
| [Writing](writing.md) | `Create`, `NewWriter`, `Flush`, `Write` copy semantics, crash safety and `WithSync` durable writes, `Stream`, interleaved channels. |
| [Defragment](defragment.md) | `Defragment`, `DefragmentToFile`, `DefragmentFile`: rewrite a fragmented file as one contiguous segment. |
| [Waveforms](waveforms.md) | `Waveform[T]`, `ReadWaveform`, `WaveformHeader`, scales, `WriteWaveform`. |
| [File status, torn tails, and Follow](file-status.md) | `File.Status`, `WithLenientTail`, `DroppedTail` and `TailError`, `Follow` of a file still being written. |
| [Datasets](dataset.md) | The `dataset` package: declaring time, basis, `WithSharedClock`, `WithoutTimes`, merging files, windows, benchmarks. |
| [DAQmx](daqmx.md) | DAQmx raw-data index words, layouts, scaling, and what returns `ErrDAQmx`. |
| [API](api.md) | Table of the exported API, channel order, and errors. |
| [Format and support](format.md) | TDMS layout, limitations, the support matrix, and format choices where the NI article is ambiguous. |
| [CLI](cli.md) | `gotdms info`, `convert`, `index`, and `defragment`. |
| [Segment index](segment-index.md) | The `.gtdx` sidecar, its validation, trust-identity mode, and its benchmark. |
| [Performance and benchmarks](performance.md) | Read and write paths, buffers, and benchmark figures. |
| [Testing](testing.md) | `go test`, fuzzing, the CI-only npTDMS oracle, golden tests, mutation testing, examples. |
| [Mutation testing](mutation-testing.md) | The nightly Gremlins run. |
| [Releasing](releasing.md) | The tag checklist. |
