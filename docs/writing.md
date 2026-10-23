# Writing

[Back to the docs index](README.md)

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

This is the write half of `ExampleCreate` in [example_docs_test.go](../example_docs_test.go), so `go test` compiles and runs it. Returning `w.Close()` reports a failed final flush; the deferred `Close` only covers early returns.

`Create` and `NewWriter` buffer one segment. Repeated `Write` calls before `Flush` belong to the same chunk. `Close` flushes and, for `Create`, closes the file. An empty `Close` still writes a valid root segment.

## Crash safety and durable writes (`WithSync`)

`bytes.Buffer` and other non-seekable writers write the lead-in with the finished length, then stream the raw bytes. A seekable writer (`*os.File`) publishes the NI incomplete-segment sentinel (`0xFFFFFFFFFFFFFFFF`) in the next-segment offset, then patches the real length after the raw bytes have been written to the file. If the process crashes in between, the last segment is one the reader can consume up to EOF. By default the writer never calls fsync, so that guarantee covers a process crash, not a power loss or OS crash: the kernel may put the patched lead-in on disk before the raw bytes it names, and the file then reads as truncated or with wrong samples. `WithSync` (for example `Create(path, tdms.WithSync())`) closes that gap. Each segment is written with the sentinel, fsynced, patched, and fsynced again, and `Create` syncs the parent directory once (best effort; skipped on Windows). That guarantee is per synced segment: a power loss while a later segment is being written can leave a torn tail (a partial lead-in, zero-filled blocks, or cut-off metadata). A strict `Open` rejects that file. `Open(path, tdms.WithLenientTail())` reads the synced segments, drops the torn bytes, and reports them in `Status().DroppedTail` and `Status().TailError`. Truncating the file to its size minus `DroppedTail` makes it open strictly again; `gotdms info --lenient file.tdms` prints that size. Raw bytes of the segment that was being written when power failed were never synced and may read as zeros; they sit in an incomplete segment and are not committed samples. `Writer.Sync` writes the pending segment and fsyncs, with or without the option, and during a `Stream` it commits the values streamed so far. Every fsync is a device flush, so durable writes favor fewer, larger segments. Output goes through a 1 MiB buffer (`WithWriteBuffer`). `Write` copies the caller's slice into the channel buffer, so refilling that slice and writing again does not change bytes already buffered. `WriteNoCopy` skips that copy for a fixed-size numeric or complex slice on a little-endian host, and for a one-byte slice on any host. The caller must not mutate the retained slice until `Flush` or `Close` returns. Bool, timestamp, extended, and string values are copied either way. A big-endian host copies multi-byte numeric slices too, because the file is stored little-endian. Flush streams the channel buffer and does not allocate a second copy of a contiguous segment.

`ExampleWithSync` is the compile-checked copy of a `WithSync` write. Torn-tail recovery on the read side is in [File status, torn tails, and Follow](file-status.md).

## Stream

`Stream` is the path that does not hold a large channel until `Flush`. The caller declares how many values each channel will receive. The writer emits the lead-in and the raw-data index immediately, then each `Stream.Write` copies that chunk to the underlying writer. Resident memory is the write buffer plus the chunk in hand, not the total number of samples. `Finish` (or `Close`, when every declared value has been written) patches a seekable lead-in. String channels are not streamed: their offset table needs every string length, so they stay on `Write`. A stream that stops short is not closed out as a finished segment. On a seekable file the lead-in stays the incomplete-segment sentinel and the reader keeps the whole values that were written. On a non-seekable writer the lead-in already names the full length, so the short file returns `ErrTruncated`.

```go
ch, err := w.Group("acquisition").Channel("temperature", tdms.TypeFloat64)
s, err := ch.Stream(1_000_000) // total values, not the chunk size
for more {
    err = s.Write(ch, chunk) // chunk may be reused after Write returns
}
err = s.Finish()
```

Contiguous channels in one stream are written in creation order, each count finished before the next channel starts. Interleaved channels must share one count; `WriteInterleaved` takes one equal-length slice per channel and mixes that chunk immediately. `SetProperty` during an open stream returns an error, because the segment metadata is already written. `WriteWaveform` still buffers Y. `StreamWaveform` sets the waveform properties and the declared count, then Y is written with `Stream.Write`.

## Interleaved channels

`SetInterleaved(true)` multiplexes fixed-size channels sample by sample. Every channel in that chunk must have the same number of values. See the [support matrix](format.md#support) for strings.

`Example_stream` is the compile-checked copy of a `Stream` write. Waveform writes (`WriteWaveform`, `StreamWaveform`) are in [Waveforms](waveforms.md).
