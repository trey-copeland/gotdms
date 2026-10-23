# Segment index

[Back to the docs index](README.md)

`WithSegmentIndex(sidecar)` stores the parsed segment layout beside the data file. `SegmentIndexPath(path)` is `path + ".gtdx"`. `BuildSegmentIndex(path, opts...)` parses lead-ins and metadata only, writes that sidecar atomically, and returns an error if the write fails. `Open` still ignores a write failure. `BuildSegmentIndexFile` is the same write and returns the sidecar path. `SegmentIndexValid` reports whether the sidecar matches, using the full fingerprint, and does not write. Pass `WithLenientTail` to both the build and a later open when the file's final next-segment offset runs past EOF; the fingerprint walk uses that option, and a truncated sidecar is a miss on a strict open. A file with a dropped torn tail never gets a sidecar: `Open` skips the write, and `BuildSegmentIndex` returns `ErrTruncated` asking for the file to be truncated first.

The file is not an NI `*.tdms_index` (`TDSh`) companion. Raw samples are not stored. By default a cache hit still reads every lead-in and the metadata bytes that follow it, in order, and recomputes the IEEE CRC-32 fingerprint. It does not decode object paths, raw-data indexes, or properties out of those bytes; the object tree and the spans come from the sidecar. It still reads sample bytes from the TDMS file, so an in-place edit of raw samples is visible when the spans still address those bytes. A span that addresses bytes outside the file, or that claims more samples than the file can hold, is a decode error and therefore a cache miss.

`WithSegmentIndexTrustIdentity` is opt-in. Combined with `WithSegmentIndex`, an open accepts the sidecar when size, modification time in Unix nanoseconds, device, and inode match, and it does not re-read lead-ins or metadata for the fingerprint. Use it when a heavily fragmented file is not being edited in place and the fingerprint walk dominates load time. An edit that preserves size and modification time, and that does not replace the inode, is missed: `os.Chtimes` after an in-place metadata rewrite is the usual case. The cached object tree is then stale. Bounds checks still run, so a span outside the file, or a channel whose samples would not fit in the file, is a cache miss, not an out-of-bounds read or a huge allocation. The default open does not set this option.

On Windows the device and inode are the volume serial number and the file index from `GetFileInformationByHandle`. A replaced file with the same size and the same timestamp is a miss, the same as on Linux. On a platform that still reports no device and inode, trust mode checks only size and modification time, and both identity fields are stored as 0. FAT records modification time in steps of 2 seconds, and exFAT in steps of 10 milliseconds, so two writes inside one step share a timestamp and an edit in that step is missed.

The sidecar is little-endian, version 2, magic `GTDX` (72-byte header, then the object tree). Version 1 sidecars still decode; a v0.1.0 reader accepts only version 1, so a version 2 sidecar is a miss there. The header holds the data file's size, modification time in Unix nanoseconds, device id and inode (0 when the platform does not report them), an IEEE CRC-32 of every lead-in followed by that segment's metadata, and an IEEE CRC-32 of the payload. Flags record an incomplete final segment, a partial tail, and a final segment recovered by `WithLenientTail` (bit 2). The payload is file properties, then groups, channels, and the spans `Values` uses to address samples. Consecutive spans of one channel that share a chunk layout and advance by a constant byte step are one run. A single segment is a 96-byte record. A run sets flag bit 5 and appends the step and count, 112 bytes in all. In memory the span stays 112 bytes either way. A run index selects the step and count in a side table, and index 0 is one segment. The index is not a pointer, so growing the span slice takes no write barrier. Version 2 is stored already merged, so open does not coalesce it again. Version 1 is one span per segment and is coalesced on open. `Values`, `Slice`, and `Cursor` walk a run and leave a single segment on the one-segment path. Strings, live segments, and partial tails stay one span. Properties use the ordinary TDMS property encoding.

An open uses the sidecar only when every one of those checks matches the file it is about to read. A missing sidecar, a size or time or identity mismatch, a fingerprint mismatch, a bad payload checksum, an unknown version, or a truncated file is ignored. `Open` still returns the parsed file. After a full parse the sidecar is replaced by writing a temporary file in the same directory and renaming it over the sidecar. A failure of that write does not fail the open. Passing the data path itself as the sidecar is ignored, so the TDMS file is not overwritten. `File.SegmentIndexUsed` reports whether this open restored the layout from the sidecar.

`dataset.WithSegmentIndex(true)` turns the same cache on for catalog and for later reads. The default is off. `dataset.WithSegmentIndexTrustIdentity` turns the cache on and skips the fingerprint walk; `dataset.WithSegmentIndex(false)` after it turns the cache back off.

`gotdms index` builds sidecars from the command line; see [CLI](cli.md#index).

## Benchmark

`BenchmarkSegmentIndexOpen` opens a synthetic file of many segments three ways: no sidecar, a default sidecar hit, and a trust-identity hit. Each segment after the first is a lead-in plus four `float64` samples (one per channel). Uniform files write one sample per channel every segment. Mixed files write two samples on every 1000th segment, so the run splitter has to break. Alternating files write two samples on every even segment and one on every odd segment, so no run can form. Files and sidecars are built once, outside the timed loop. Figures are medians of 10 runs (`go test -run '^$' -bench 'BenchmarkSegmentIndexOpen' -benchmem -count=10`, default benchtime). Each median is the average of the 5th and 6th sorted runs. The host is a 4 vCPU Intel Xeon cloud VM, Linux amd64, 16 GiB, Go 1.22.2.

| Segments | Layout | TDMS | Sidecar | Open | Median time | B/op | allocs/op |
| --- | --- | ---: | ---: | --- | ---: | ---: | ---: |
| 10,000 | uniform | 600,187 | 703 | no sidecar | 6.149 ms | 3,203,285 | 60,051 |
| 10,000 | uniform | 600,187 | 703 | sidecar | 2.829 ms | 325,040 | 10,052 |
| 10,000 | uniform | 600,187 | 703 | trust identity | 8.854 µs | 4,832 | 50 |
| 10,000 | mixed | 603,471 | 8,575 | no sidecar | 6.153 ms | 3,241,587 | 60,349 |
| 10,000 | mixed | 603,471 | 8,575 | sidecar | 2.893 ms | 347,408 | 10,083 |
| 10,000 | mixed | 603,471 | 8,575 | trust identity | 18.503 µs | 24,160 | 62 |
| 10,000 | alternating | 2,320,031 | 3,840,255 | no sidecar | 22.101 ms | 24,807,818 | 200,105 |
| 10,000 | alternating | 2,320,031 | 3,840,255 | sidecar | 10.116 ms | 10,254,873 | 20,047 |
| 10,000 | alternating | 2,320,031 | 3,840,255 | trust identity | 3.399 ms | 8,334,819 | 46 |
| 100,000 | uniform | 6,000,187 | 703 | no sidecar | 63.058 ms | 32,003,346 | 600,051 |
| 100,000 | uniform | 6,000,187 | 703 | sidecar | 28.165 ms | 3,205,055 | 100,052 |
| 100,000 | uniform | 6,000,187 | 703 | trust identity | 8.959 µs | 4,832 | 50 |
| 100,000 | mixed | 6,034,431 | 83,455 | no sidecar | 62.911 ms | 32,348,856 | 602,895 |
| 100,000 | mixed | 6,034,431 | 83,455 | sidecar | 29.109 ms | 3,440,288 | 100,275 |
| 100,000 | mixed | 6,034,431 | 83,455 | trust identity | 101.404 µs | 208,225 | 74 |
| 100,000 | alternating | 23,200,031 | 38,400,255 | no sidecar | 232.001 ms | 284,451,712 | 2,000,145 |
| 100,000 | alternating | 23,200,031 | 38,400,255 | sidecar | 101.602 ms | 102,434,320 | 200,047 |
| 100,000 | alternating | 23,200,031 | 38,400,255 | trust identity | 29.826 ms | 83,234,272 | 46 |

A uniform file's sidecar stays 703 bytes at both sizes: four channels become four run records, and the repeat count is one integer. On the 100,000-segment uniform file the no-sidecar open takes 2.24× as long as a default sidecar hit. Trust identity is 8.959 µs, about 7,000× faster than parsing and about 3,100× faster than the default hit. The default hit still walks every lead-in to recompute the fingerprint (100,052 allocs/op). Trust skips that walk (50 allocs/op) and decodes the 703-byte sidecar. The mixed file breaks a run every 1000 segments, so its sidecar is 83,455 bytes at 100,000 segments and trust takes 101 µs. An alternating file cannot form a run. Its sidecar is one 96-byte span per segment per channel, 38,400,255 bytes at 100,000 segments, and the no-sidecar open allocates 284,451,712 bytes. The fingerprint walk still dominates a default hit, which is why the mixed and uniform sidecar times stay close. No-sidecar allocs on a uniform file stay near one per segment because the lead-in walk allocates and the span slice collapses to the runs.

```
go test -run '^$' -bench 'BenchmarkSegmentIndexOpen' -benchmem -count=10
```
