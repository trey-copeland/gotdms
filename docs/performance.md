# Performance and benchmarks

[Back to the docs index](README.md)

## Read path

Metadata parsing reads the lead-in and the metadata bytes of each segment. Raw samples stay on the `io.ReaderAt` until `Values`, a typed helper, or `Cursor`.

`Values` issues `ReadAt` for the bytes it needs. A contiguous numeric channel on a little-endian machine is copied straight into the result slice: there is no intermediate `[]byte` and no per-sample `binary.Read`. Big-endian segments are byte-swapped in place after that read. A big-endian host uses the scalar decoder instead of the copy; `decodePackedScalar` is checked against the bulk path in tests. Bools, timestamps, and 80-bit extended values stay on the scalar path everywhere, because their Go layout is not the on-disk layout.

`Cursor` does not use that one-shot `ReadAt`. It reads ahead through a pooled buffer, 256 KiB by default (`WithReadBuffer`), and reuses the batch slice (`Values` is valid only until the next `Next`). A read at least as large as the buffer skips the cache and goes to `ReadAt`. A contiguous channel of a fixed-size numeric type also reads each batch straight into the batch slice with `ReadAt` once the batch is at least 32 KiB (4096 `float64` samples), as long as the read buffer is the default size or smaller; smaller batches, other layouts, and a larger `WithReadBuffer` keep the buffered path. This package does not mmap. A mapping delivers `SIGBUS` if the file is truncated, and it is a poorer default on 32-bit and non-Unix targets.

Interleaved fixed-size channels are split out in about 1 MiB windows rather than by allocating the whole multiplexed span once per channel. `ReadValues` and `Group.Values` read several channels of one interleaved segment in a single pass over 128 KiB windows, so the multiplexed bytes are read once instead of once per channel. String channels and mixed-width layouts use the same windowed copy; the string offset table is unchanged.

## Write path

`Write` copies into the channel buffer. On a little-endian machine the numeric encode is a bulk copy of the caller's bytes. `WriteNoCopy` is the opt-in that keeps the caller's backing array until the next write or flush. `Flush` computes the raw size (including a string offset table) and streams metadata, then raw bytes, through a 1 MiB `bufio.Writer` (`WithWriteBuffer`). A contiguous flush does not allocate a second copy of the segment. Interleaved output is mixed in a window of about 1 MiB. Seekable writers still publish the incomplete-segment sentinel and patch eight bytes of the lead-in after the buffer is flushed. The channel buffer itself is one copy of the encoded samples until `Flush`; call `Flush` before a chunk outgrows memory. String payload is streamed at flush. Per-string headers are still allocated on read; a CPU profile of numeric multi-hundred-megabyte files did not show that path, so it was left alone.

There is no mmap switch. The defaults above are the fast path; the options only change buffer size.

## Benchmarks

`BenchmarkWriteFloat64`, `BenchmarkReadFloat64`, and `BenchmarkStreamFloat64` move 100,000 `float64` values. `BenchmarkLargeReadFloat64`, `BenchmarkLargeReadFloat64File`, `BenchmarkLargeCursorFile`, `BenchmarkLargeInterleaved`, `BenchmarkLargeWriteFile`, and `BenchmarkLargeWriteMem` move 128 MiB (four float64 channels for the interleaved case). `BenchmarkLargeInterleavedOnce` reads the same four interleaved channels in one `ReadValues` pass instead of one `Float64s` call per channel. `BenchmarkLargeInterleavedMany`, `BenchmarkLargeInterleavedManySeparate`, and `BenchmarkLargeInterleavedManyOnce` are the wide case: 32 interleaved channels of 65,536 samples (16 MiB), read per channel or in one `ReadValues` pass. `BenchmarkLargeStreamFile` writes 128 MiB through `Stream` in 64 KiB chunks. `BenchmarkLargeWriteDiscard` and `BenchmarkLargeStreamDiscard` allocate the samples inside the timed loop and discard the bytes, so `B/op` is the writer's footprint. `BenchmarkChunkStatsFloat64` walks 1,048,576 `float64` samples (8 MiB) in chunks of 4096. They run only under `-bench`.

```bash
go test -run '^$' -bench 'BenchmarkLarge|BenchmarkChunkStatsFloat64' -benchmem -count=10 -benchtime=100ms
```

The figures below are medians of those ten runs (the average of the 5th and 6th sorted runs) on an 8 vCPU Intel Xeon cloud VM, Linux amd64, Go 1.27.3, at `v0.2.0`, with synthetic data. `B/op` was the same, or within a few bytes, in every run, and `allocs/op` within two. Throughput was not: on this shared VM the fastest of the ten runs of one benchmark was 1.05× to 4.9× the slowest, and a second set of ten runs on the same VM gave medians from 0.69× (`LargeWriteDiscard`) to 1.63× (`LargeReadFloat64`) of the ones below. Read MB/s as an order of magnitude for this machine, not a hardware claim. `b.SetBytes` is the raw sample payload.

| Benchmark | Median MB/s | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Contiguous float64 `Values`, in memory (`LargeReadFloat64`) | 1515 | 128.00 MiB | 26 |
| Contiguous float64 `Values`, `os.File` (`LargeReadFloat64File`) | 1794 | 128.00 MiB | 29 |
| `Cursor` over `os.File`, batch 4096 (`LargeCursorFile`) | 5642 | 33.9 KiB | 29 |
| 4 interleaved channels, per channel (`LargeInterleaved`) | 1020 | 128.02 MiB | 568 |
| 4 interleaved channels, one pass (`LargeInterleavedOnce`) | 2072 | 128.13 MiB | 59 |
| 32 interleaved channels, per channel (`LargeInterleavedMany`) | 471 | 16.04 MiB | 829 |
| 32 interleaved channels, per channel, on the `ManyOnce` fixture (`LargeInterleavedManySeparate`) | 454 | 16.04 MiB | 829 |
| 32 interleaved channels, one pass (`LargeInterleavedManyOnce`) | 3724 | 16.16 MiB | 325 |
| Write one channel, `os.File` (`LargeWriteFile`) | 1062 | 129.00 MiB | 47 |
| Write one channel, `bytes.Buffer` (`LargeWriteMem`) | 1471 | 258.01 MiB | 43 |
| `Stream` to `os.File`, 64 KiB chunks (`LargeStreamFile`) | 2041 | 1.05 MiB | 2101 |
| Write, discarded (`LargeWriteDiscard`) | 3618 | 257.00 MiB | 41 |
| `Stream`, discarded (`LargeStreamDiscard`) | 43250 | 1.11 MiB | 2095 |
| Chunk statistics, batch 4096 (`ChunkStatsFloat64`) | 3034 | 32,984 B | 4 |

The cursor's allocation is the reused 32 KiB batch: one scan does not allocate a fresh slice per batch, and at that size it reads with `ReadAt` straight into the batch. Chunk statistics is likewise one 32 KiB chunk plus a few small headers, not a copy of the channel. `Write` copies the caller's samples, so the file write's 129 MiB is the 128 MiB copy plus the 1 MiB write buffer, and the in-memory write is that copy plus the destination `bytes.Buffer`. `WriteDiscard` adds the 128 MiB sample slice allocated inside the loop. `Stream` does not copy the samples: one small interface header per 64 KiB chunk. Reading interleaved channels in one pass is faster than one call per channel because each channel read walks the shared span again.

The segment-index open benchmark is in [Segment index](segment-index.md#benchmark), and the dataset tail-window benchmark is in [Datasets](dataset.md#benchmarks).
