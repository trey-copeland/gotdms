# Performance and benchmarks

[Back to the docs index](README.md)

## Read path

Metadata parsing reads the lead-in and the metadata bytes of each segment. Raw samples stay on the `io.ReaderAt` until `Values`, a typed helper, or `Cursor`.

`Values` issues `ReadAt` for the bytes it needs. A contiguous numeric channel on a little-endian machine is copied straight into the result slice: there is no intermediate `[]byte` and no per-sample `binary.Read`. Big-endian segments are byte-swapped in place after that read. A big-endian host uses the scalar decoder instead of the copy; `decodePackedScalar` is checked against the bulk path in tests. Bools, timestamps, and 80-bit extended values stay on the scalar path everywhere, because their Go layout is not the on-disk layout.

`Cursor` does not use that one-shot `ReadAt`. It reads ahead through a pooled buffer, 256 KiB by default (`WithReadBuffer`), and reuses the batch slice (`Values` is valid only until the next `Next`). A read at least as large as the buffer skips the cache and goes to `ReadAt`. This package does not mmap. A mapping delivers `SIGBUS` if the file is truncated, and it is a poorer default on 32-bit and non-Unix targets.

Interleaved fixed-size channels are split out in about 1 MiB windows rather than by allocating the whole multiplexed span once per channel. String channels and mixed-width layouts use the same windowed copy; the string offset table is unchanged.

## Write path

`Write` copies into the channel buffer. On a little-endian machine the numeric encode is a bulk copy of the caller's bytes. `WriteNoCopy` is the opt-in that keeps the caller's backing array until the next write or flush. `Flush` computes the raw size (including a string offset table) and streams metadata, then raw bytes, through a 1 MiB `bufio.Writer` (`WithWriteBuffer`). A contiguous flush does not allocate a second copy of the segment. Interleaved output is mixed in a window of about 1 MiB. Seekable writers still publish the incomplete-segment sentinel and patch eight bytes of the lead-in after the buffer is flushed. The channel buffer itself is one copy of the encoded samples until `Flush`; call `Flush` before a chunk outgrows memory. String payload is streamed at flush. Per-string headers are still allocated on read; a CPU profile of numeric multi-hundred-megabyte files did not show that path, so it was left alone.

There is no mmap switch. The defaults above are the fast path; the options only change buffer size.

## Benchmarks

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

The segment-index open benchmark is in [Segment index](segment-index.md#benchmark), and the dataset tail-window benchmark is in [Datasets](dataset.md#benchmarks).
