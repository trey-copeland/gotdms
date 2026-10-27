# API

[Back to the docs index](README.md)

| Piece | Role |
| --- | --- |
| `Open`, `NewReader`, `File` | Read a file. `Groups`, `Group`, `Properties` at file level. `Version`, `SegmentCount`. |
| `Group`, `Channel` | Names, paths, properties, `Values`, `Slice`, `Cursor`, `CursorAt`, typed helpers, `Waveform`, `WaveformHeader`. |
| `ReadValues`, `Group.Values` | Read several channels at once. A shared interleaved segment is split in one pass. |
| `Int64s`, `Float64s`, `Cursor.Float64sCopy` | Numeric conversions. `Float64sCopy` is an independent copy of the current cursor batch. |
| `Create`, `NewWriter`, `Writer` | `SetProperty`, `Group`, `SetInterleaved`, `Flush`, `Sync`, `Close`, `Stream`. `WithWriteBuffer` (default 1 MiB). `WithSync` (fsync each segment around the lead-in patch). |
| `GroupWriter`, `ChannelWriter` | `SetProperty`, `Channel`, `Write`, `WriteNoCopy`, `WriteWaveform`, `Stream`, `StreamWaveform`. |
| `Stream` | Declared-count segment. `Write`, `WriteInterleaved`, `Finish`. |
| `Defragment`, `DefragmentToFile`, `DefragmentFile` | Rewrite a fragmented file as one contiguous segment. In place (`DefragmentFile`) is atomic. Rejects DAQmx (`ErrDAQmx`) and big-endian (`ErrUnsupported`). See [Defragment](defragment.md). |
| `Waveform[T]`, `ReadWaveform`, `ScaledWaveform` | Analog waveform. `ApplyScale` applies a `LinearScale` explicitly. |
| `WaveformHeader` | t0, dt, unit, and `wf_samples` without loading Y. `TimeAt` timestamps cursor batches. |
| `DataType` | NI `tdsDataType` codes. `String`, `Size`. |
| `Property`, `Timestamp`, `Extended`, `LinearScale` | Values and conversions. |
| `Channel.DAQmx`, `DAQmxSlice` | DAQmx raw-data index, and one scaler's raw samples by scale id. |
| `DAQmxScaling`, `DAQmxScaledFloat64s`, `ApplyDAQmxScale` | `NI_Scale` metadata. Linear and polynomial chains apply only through these calls. |
| `WithReadBuffer` | Sequential buffer for `Cursor` (default 256 KiB). `Values` stays on `ReadAt`. |
| `Channel.ChunkStats` | Count, min, max, mean, and RMS per chunk of a numeric channel, one Cursor pass. |
| `File.Status` | Clean end, an incomplete final segment, or (`WithLenientTail`) a truncated final segment or dropped torn tail (`DroppedTail`, `TailError`), plus any short tail. |
| `WithLenientTail` | Open a file whose last finite next-segment offset runs past EOF (whole tail values are readable and are not committed), or that ends in a torn tail after its last whole segment (the torn bytes are dropped). |
| `Follow`, `WithTailInterval`, `WithTailBatch` | Poll a file that is still being written. Skip an incomplete sentinel and a past-EOF tail. |
| `WithSegmentIndex`, `SegmentIndexPath`, `BuildSegmentIndex` | Sidecar of the segment layout. `BuildSegmentIndex` writes `path + ".gtdx"` and returns a write error. Validated by size, modification time, identity, and a metadata fingerprint. |
| `WithSegmentIndexTrustIdentity` | Opt-in. Accept the sidecar when size, modification time, device, and inode match, and skip the fingerprint walk. An edit that preserves those values is missed. |
| `Error` and `Err*` | Wrapped errors. Use `errors.Is`. |

Channel order inside a segment is creation order on the writer, which is the order objects first appear on the reader. `Write` does not reorder channels.

A channel that has never had a raw-data index has no type on disk (`DataType` is void, `Len` is 0). The type is stored only in a raw-data index.

## Errors

Public operations return `*Error` with an operation name and, when there is one, an object or file path. `Unwrap` yields a sentinel (`ErrBadTag`, `ErrTruncated`, `ErrCorrupt`, `ErrDAQmx`, `ErrUnsupported`, `ErrType`, `ErrLayout`, `ErrVersion`, `ErrIndexFile`, `ErrClosed`, `ErrArgument`) or the underlying I/O error. `ErrDAQmx` means an unsupported DAQmx layout, not the presence of DAQmx data. Malformed, truncated, and unknown input returns an error. The reader does not panic on it. `FuzzReader` is the regression target.
